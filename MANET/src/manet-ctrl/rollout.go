package main

// Staged offline rollout. Pushing a package to the whole fleet at once has
// every node pull it from one source over the mesh simultaneously, which
// gets slow, and fragile over HaLow, as the fleet grows. A rollout instead
// runs on the source node and updates the fleet in waves:
//
//   - the nodes this one reaches fastest go first;
//   - a node that finishes holds the package (node-update keeps what it
//     installs) and becomes a source for the next ones;
//   - each source feeds one node at a time, and each starting node fetches
//     from the free source it reaches fastest (pickOfflineSource), so waves
//     roughly double and far nodes pull from a neighbour.
//
// The plan lives here, on disk, so it does not depend on a browser staying
// open and survives a manet-ctrl restart. The source node itself is not
// updated: it is updated on its own once the rollout is done.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

var RolloutStateFile = "/var/lib/manet-rollout.json"

const (
	rolloutTick = 10 * time.Second
	// rolloutNodeTimeout: fetch (60 min limit) plus install and reboot.
	rolloutNodeTimeout = 90 * time.Minute
	// rolloutIdleFail: a node back to idle on the old version this long
	// has failed: its fetch or install stopped (its logs say why).
	rolloutIdleFail = 2 * time.Minute
)

type rolloutNode struct {
	IP       string `json:"ip"`
	Hostname string `json:"hostname"`
	// State: pending, updating, done or failed.
	State    string `json:"state"`
	From     string `json:"from,omitempty"`
	Started  int64  `json:"started,omitempty"`
	Finished int64  `json:"finished,omitempty"`
	Error    string `json:"error,omitempty"`
	// Serves: done and holding the package, so a source for the next.
	Serves    bool  `json:"serves,omitempty"`
	IdleSince int64 `json:"idle_since,omitempty"`
}

type rolloutState struct {
	ID       int64             `json:"id"`
	Channel  string            `json:"channel"`
	Versions map[string]string `json:"versions"`
	Source   string            `json:"source"`
	Running  bool              `json:"running"`
	Stopped  bool              `json:"stopped"`
	Nodes    []*rolloutNode    `json:"nodes"`
}

var (
	rolloutMu sync.Mutex
	rollout   *rolloutState
)

// The mesh as the rollout sees it; vars so tests can fake it.
var (
	rolloutPeerStatus = func(ip string) map[string]interface{} { return getPeerUpdateStatus(ip, 5*time.Second) }
	rolloutPeerStore  = func(ip string) map[string]interface{} {
		return getPeerJSON(ip, "/api/offline-update", 5*time.Second)
	}
	rolloutStartNode = startPeerOfflineInstall
	// rolloutFleet is every other registry node as (ip, hostname), and
	// this node's own ip.
	rolloutFleet = registryFleet
	rolloutGo    = func(id int64) { go rolloutLoop(id) }
)

func rolloutChannels(channel string) []string {
	if channel == "both" {
		return offlineChannels
	}
	return []string{channel}
}

// statusLocal is a channel's installed version in an update-status reply.
func statusLocal(st map[string]interface{}, ch string) string {
	c, _ := st[ch].(map[string]interface{})
	v, _ := c["local"].(string)
	return v
}

// rolloutNeeds reports whether a node with update status st is older than
// the rollout's version on any of its channels.
func rolloutNeeds(versions map[string]string, st map[string]interface{}) bool {
	for ch, v := range versions {
		if versionNewer(ch, v, statusLocal(st, ch)) {
			return true
		}
	}
	return false
}

// rolloutHolds reports whether an offline store reply holds exactly the
// rollout's versions.
func rolloutHolds(versions map[string]string, store map[string]interface{}) bool {
	for ch, v := range versions {
		p, _ := store[ch].(map[string]interface{})
		if got, _ := p["version"].(string); got != v {
			return false
		}
	}
	return true
}

func registryFleet() (nodes [][2]string, self string) {
	myMAC := getMyMAC()
	for _, rn := range parseRegistry() {
		ip := rn["IPV4_ADDRESS"]
		if ip == "" {
			continue
		}
		if normMAC(rn["MAC_ADDRESS"]) == myMAC {
			self = ip
			continue
		}
		nodes = append(nodes, [2]string{ip, rn["HOSTNAME"]})
	}
	return nodes, self
}

func offlineChannelName(ch string) string {
	if ch == "overlay" {
		return "Kernel/Drivers"
	}
	return "MANET"
}

// startRollout plans a rollout of channel's package from this node's store
// to every reachable node that is older, fastest path first, and starts it.
func startRollout(channel string) (*rolloutState, error) {
	rolloutMu.Lock()
	defer rolloutMu.Unlock()
	if rollout != nil && rollout.Running {
		return nil, errors.New("a rollout is already running from this node")
	}
	store := readOfflineStore()
	versions := map[string]string{}
	for _, ch := range rolloutChannels(channel) {
		p := store.Software
		if ch == "overlay" {
			p = store.Overlay
		}
		if p == nil {
			return nil, fmt.Errorf("this node holds no %s package", offlineChannelName(ch))
		}
		versions[ch] = p.Version
	}
	fleet, self := rolloutFleet()
	if self == "" {
		return nil, errors.New("this node is not in the mesh registry yet")
	}

	statuses := make([]map[string]interface{}, len(fleet))
	var wg sync.WaitGroup
	for i, n := range fleet {
		wg.Add(1)
		go func(i int, ip string) {
			defer wg.Done()
			statuses[i] = rolloutPeerStatus(ip)
		}(i, n[0])
	}
	wg.Wait()

	scores := pathScores()
	var nodes []*rolloutNode
	for i, n := range fleet {
		switch st := statuses[i]; {
		case st == nil:
			nodes = append(nodes, &rolloutNode{IP: n[0], Hostname: n[1], State: "failed", Error: "unreachable when the rollout started"})
		case rolloutNeeds(versions, st):
			nodes = append(nodes, &rolloutNode{IP: n[0], Hostname: n[1], State: "pending"})
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		// Pending first, fastest path from here first.
		if pi, pj := nodes[i].State == "pending", nodes[j].State == "pending"; pi != pj {
			return pi
		}
		return scores[nodes[i].IP] > scores[nodes[j].IP]
	})
	if len(nodes) == 0 || nodes[0].State != "pending" {
		return nil, errors.New("no reachable node is older than this node's package")
	}

	rollout = &rolloutState{
		ID: time.Now().Unix(), Channel: channel, Versions: versions,
		Source: self, Running: true, Nodes: nodes,
	}
	saveRollout(rollout)
	log.Printf("rollout %d: %s %v from %s to %d node(s)", rollout.ID, channel, versions, self, len(nodes))
	rolloutGo(rollout.ID)
	return copyRollout(rollout), nil
}

// rolloutLoop advances rollout id every rolloutTick until it is done or
// replaced. Each step works on a copy, so slow peers never hold the lock
// the status endpoint needs.
func rolloutLoop(id int64) {
	for {
		rolloutMu.Lock()
		if rollout == nil || rollout.ID != id || !rollout.Running {
			rolloutMu.Unlock()
			return
		}
		rs := copyRollout(rollout)
		rolloutMu.Unlock()

		rolloutStep(rs, time.Now().Unix())

		rolloutMu.Lock()
		if rollout == nil || rollout.ID != id {
			rolloutMu.Unlock()
			return
		}
		// A Stop that arrived during the step still counts.
		rs.Stopped = rs.Stopped || rollout.Stopped
		if rs.Stopped && !rolloutAnyIn(rs, "updating") {
			rs.Running = false
		}
		rollout = rs
		saveRollout(rs)
		running := rs.Running
		rolloutMu.Unlock()
		if !running {
			log.Printf("rollout %d finished", id)
			return
		}
		time.Sleep(rolloutTick)
	}
}

func rolloutAnyIn(rs *rolloutState, state string) bool {
	for _, n := range rs.Nodes {
		if n.State == state {
			return true
		}
	}
	return false
}

// rolloutStep checks on the nodes being updated, then starts the next
// pending ones on every free source.
func rolloutStep(rs *rolloutState, now int64) {
	for _, n := range rs.Nodes {
		if n.State != "updating" {
			continue
		}
		fail := func(msg string) {
			n.State, n.Error, n.Finished = "failed", msg, now
			log.Printf("rollout %d: %s (%s) failed: %s", rs.ID, n.Hostname, n.IP, msg)
		}
		elapsed := time.Duration(now-n.Started) * time.Second
		st := rolloutPeerStatus(n.IP)
		if st == nil {
			// Rebooting into the update, most likely.
			n.IdleSince = 0
			if elapsed > rolloutNodeTimeout {
				fail("unreachable for too long")
			}
			continue
		}
		if !rolloutNeeds(rs.Versions, st) {
			n.State, n.Finished, n.IdleSince = "done", now, 0
			n.Serves = rolloutHolds(rs.Versions, rolloutPeerStore(n.IP))
			log.Printf("rollout %d: %s (%s) updated (serves the next: %v)", rs.ID, n.Hostname, n.IP, n.Serves)
			continue
		}
		if phase, _ := st["phase"].(string); phase == "" || phase == "idle" {
			if n.IdleSince == 0 {
				n.IdleSince = now
			} else if time.Duration(now-n.IdleSince)*time.Second >= rolloutIdleFail {
				fail("stopped without updating; see its manet-ctrl and node-update logs")
				continue
			}
		} else {
			n.IdleSince = 0
		}
		if elapsed > rolloutNodeTimeout {
			fail("timed out")
		}
	}

	if !rs.Stopped {
		busy := map[string]bool{}
		for _, n := range rs.Nodes {
			if n.State == "updating" {
				busy[n.From] = true
			}
		}
		var free []string
		if !busy[rs.Source] {
			free = append(free, rs.Source)
		}
		for _, n := range rs.Nodes {
			if n.State == "done" && n.Serves && !busy[n.IP] {
				free = append(free, n.IP)
			}
		}
		for _, n := range rs.Nodes {
			if len(free) == 0 {
				break
			}
			if n.State != "pending" {
				continue
			}
			from, err := rolloutStartNode(n.IP, rs.Channel, free)
			if err != nil {
				n.State, n.Error, n.Finished = "failed", "could not start: "+err.Error(), now
				log.Printf("rollout %d: %s (%s): %v", rs.ID, n.Hostname, n.IP, err)
				continue
			}
			n.State, n.From, n.Started = "updating", from, now
			log.Printf("rollout %d: %s (%s) fetching from %s", rs.ID, n.Hostname, n.IP, from)
			for i, s := range free {
				if s == from {
					free = append(free[:i], free[i+1:]...)
					break
				}
			}
		}
	}
	if !rolloutAnyIn(rs, "pending") && !rolloutAnyIn(rs, "updating") {
		rs.Running = false
	}
}

// startPeerOfflineInstall asks the node at ip to install channel from the
// best of sources (its update-now endpoint), and returns the one it took.
// Authenticated like a relayed fleet request: the rollout was started by a
// logged-in user on this node. "source" goes along for nodes that predate
// "sources": they would otherwise take the request as an online update.
func startPeerOfflineInstall(ip, channel string, sources []string) (string, error) {
	body, _ := json.Marshal(map[string]interface{}{"channel": channel, "source": sources[0], "sources": sources})
	req, err := http.NewRequest("POST", "https://"+ip+"/api/admin/update-now", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := mintFleetPeerToken(ip, FleetPeerAuthDomainAPI); token != "" {
		req.Header.Set(FleetPeerAuthHeader, token)
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: peerTLSConfig}}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool   `json:"ok"`
		Source string `json:"source"`
		Error  string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&out)
	if !out.OK {
		if out.Error == "" {
			out.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", errors.New(out.Error)
	}
	if out.Source == "" {
		out.Source = sources[0]
	}
	return out.Source, nil
}

func copyRollout(rs *rolloutState) *rolloutState {
	data, _ := json.Marshal(rs)
	var c rolloutState
	json.Unmarshal(data, &c)
	return &c
}

func saveRollout(rs *rolloutState) {
	data, err := json.Marshal(rs)
	if err != nil {
		return
	}
	tmp := RolloutStateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("rollout: saving state: %v", err)
		return
	}
	if err := os.Rename(tmp, RolloutStateFile); err != nil {
		log.Printf("rollout: saving state: %v", err)
	}
}

// resumeRollout picks up a rollout that was running when manet-ctrl
// stopped.
func resumeRollout() {
	data, err := os.ReadFile(RolloutStateFile)
	if err != nil {
		return
	}
	var rs rolloutState
	if json.Unmarshal(data, &rs) != nil {
		return
	}
	rolloutMu.Lock()
	rollout = &rs
	rolloutMu.Unlock()
	if rs.Running {
		log.Printf("rollout %d: resuming", rs.ID)
		rolloutGo(rs.ID)
	}
}

func currentRollout() *rolloutState {
	rolloutMu.Lock()
	defer rolloutMu.Unlock()
	if rollout == nil {
		return nil
	}
	return copyRollout(rollout)
}

// apiRollout: GET is the current or last rollout run from this node (no
// auth, like update-status); POST {"channel"} (auth) starts one.
func apiRollout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		if rs := currentRollout(); rs != nil {
			writeJSON(w, 200, rs)
		} else {
			writeJSON(w, 200, map[string]interface{}{"running": false})
		}
		return
	}
	if !checkAuth(w, r) {
		return
	}
	channel := jsonStr(readBody(r), "channel", "")
	if channel != "software" && channel != "overlay" && channel != "both" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "channel must be software, overlay or both"})
		return
	}
	rs, err := startRollout(channel)
	if err != nil {
		writeJSON(w, 409, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "rollout": rs})
}

// apiRolloutStop stops starting new nodes; ones already updating finish.
func apiRolloutStop(w http.ResponseWriter, r *http.Request) {
	rolloutMu.Lock()
	running := rollout != nil && rollout.Running
	if running {
		rollout.Stopped = true
		saveRollout(rollout)
	}
	rolloutMu.Unlock()
	if !running {
		writeJSON(w, 409, map[string]interface{}{"ok": false, "error": "no rollout is running from this node"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}
