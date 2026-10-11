package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeFleet simulates nodes for the rollout: each starts on 0.6.0; once
// started it is "fetching" until the next step, then holds and runs 0.6.1.
type fakeFleet struct {
	installed map[string]string
	phase     map[string]string
	holds     map[string]bool
	started   map[string]int // step a node was started at
	from      map[string]string
	refuse    map[string]bool // fails to start
	stall     map[string]bool // starts, then goes idle on the old version
	down      map[string]bool // unreachable
	step      int
}

func withFakeFleet(t *testing.T, ips []string, scores map[string]float64) *fakeFleet {
	t.Helper()
	withOfflineStore(t, true)
	os.MkdirAll(PackageDir, 0755)
	os.WriteFile(filepath.Join(PackageDir, "cm4-tools.tar.gz"), []byte("tools"), 0644)
	os.WriteFile(filepath.Join(PackageDir, "manet_release_version.txt"), []byte("0.6.1\n"), 0644)

	f := &fakeFleet{installed: map[string]string{}, phase: map[string]string{}, holds: map[string]bool{},
		started: map[string]int{}, from: map[string]string{}, refuse: map[string]bool{}, stall: map[string]bool{}, down: map[string]bool{}}
	for _, ip := range ips {
		f.installed[ip] = "0.6.0"
		f.phase[ip] = "idle"
	}
	origStatus, origStore, origStart, origFleet, origScores, origGo, origFile :=
		rolloutPeerStatus, rolloutPeerStore, rolloutStartNode, rolloutFleet, pathScores, rolloutGo, RolloutStateFile
	t.Cleanup(func() {
		rolloutPeerStatus, rolloutPeerStore, rolloutStartNode, rolloutFleet, pathScores, rolloutGo, RolloutStateFile =
			origStatus, origStore, origStart, origFleet, origScores, origGo, origFile
		rolloutMu.Lock()
		rollout = nil
		rolloutMu.Unlock()
	})
	// The tests drive rolloutStep by hand.
	rolloutGo = func(int64) {}
	RolloutStateFile = filepath.Join(t.TempDir(), "rollout.json")
	pathScores = func() map[string]float64 { return scores }
	rolloutFleet = func() ([][2]string, string) {
		var nodes [][2]string
		for _, ip := range ips {
			nodes = append(nodes, [2]string{ip, "node-" + ip})
		}
		return nodes, "10.30.2.1"
	}
	rolloutPeerStatus = func(ip string) map[string]interface{} {
		if f.down[ip] {
			return nil
		}
		return map[string]interface{}{
			"software": map[string]interface{}{"local": f.installed[ip]},
			"phase":    f.phase[ip],
		}
	}
	rolloutPeerStore = func(ip string) map[string]interface{} {
		if !f.holds[ip] {
			return map[string]interface{}{"software": nil}
		}
		return map[string]interface{}{"software": map[string]interface{}{"version": "0.6.1"}}
	}
	rolloutStartNode = func(ip, channel string, sources []string) (string, error) {
		if f.refuse[ip] {
			return "", errors.New("refused")
		}
		f.started[ip] = f.step
		f.from[ip] = sources[0]
		f.phase[ip] = "fetching offline package from " + sources[0]
		return sources[0], nil
	}
	return f
}

// advance moves the simulated nodes on: a node started in an earlier step
// has finished, unless it stalls.
func (f *fakeFleet) advance() {
	for ip, s := range f.started {
		if s < f.step && f.installed[ip] == "0.6.0" {
			if f.stall[ip] {
				f.phase[ip] = "idle"
				continue
			}
			f.installed[ip], f.phase[ip], f.holds[ip] = "0.6.1", "idle", true
		}
	}
	f.step++
}

func TestRolloutCascadesFastestFirst(t *testing.T) {
	ips := []string{"10.30.2.11", "10.30.2.12", "10.30.2.13", "10.30.2.14", "10.30.2.15", "10.30.2.16", "10.30.2.17"}
	scores := map[string]float64{}
	for i, ip := range ips {
		scores[ip] = float64(10 * (i + 1)) // .17 is the fastest
	}
	f := withFakeFleet(t, ips, scores)
	rs, err := startRollout("software")
	if err != nil {
		t.Fatal(err)
	}
	if rs.Nodes[0].IP != "10.30.2.17" || rs.Versions["software"] != "0.6.1" {
		t.Fatalf("first node %s, versions %v; want the fastest first, 0.6.1", rs.Nodes[0].IP, rs.Versions)
	}

	now := int64(1000)
	for i := 0; i < 20 && rs.Running; i++ {
		rolloutStep(rs, now)
		f.advance()
		now += 10
	}
	if rs.Running {
		t.Fatal("rollout still running after 20 steps")
	}
	for _, n := range rs.Nodes {
		if n.State != "done" || !n.Serves {
			t.Errorf("%s ended %s (serves %v, %s)", n.IP, n.State, n.Serves, n.Error)
		}
	}
	if f.from["10.30.2.17"] != "10.30.2.1" || f.started["10.30.2.17"] != 0 {
		t.Errorf("fastest node: started at step %d from %s", f.started["10.30.2.17"], f.from["10.30.2.17"])
	}
	// Every node after the first was fed by the source or a node updated
	// before it.
	for ip, src := range f.from {
		if src != "10.30.2.1" && f.started[src] >= f.started[ip] {
			t.Errorf("%s fetched from %s, which was not updated before it", ip, src)
		}
	}
	// Waves double: 1, 2, 4 starts -- 7 nodes over 3 rounds, not 7.
	perRound := map[int]int{}
	for _, s := range f.started {
		perRound[s]++
	}
	if len(perRound) != 3 || perRound[0] != 1 || perRound[2] != 2 || perRound[4] != 4 {
		t.Errorf("starts per step %v, want 1, 2, 4", perRound)
	}
}

func TestRolloutFailuresAndStop(t *testing.T) {
	ips := []string{"10.30.2.11", "10.30.2.12", "10.30.2.13"}
	f := withFakeFleet(t, ips, map[string]float64{"10.30.2.11": 30, "10.30.2.12": 20, "10.30.2.13": 10})
	f.refuse["10.30.2.11"] = true
	f.stall["10.30.2.12"] = true
	rs, err := startRollout("software")
	if err != nil {
		t.Fatal(err)
	}

	now := int64(1000)
	rolloutStep(rs, now) // .11 refuses, .12 starts from the source
	if rs.Nodes[0].State != "failed" || rs.Nodes[1].State != "updating" {
		t.Fatalf("after step 1: %s %s", rs.Nodes[0].State, rs.Nodes[1].State)
	}
	f.advance()
	// .12 sits idle on the old version: failed after rolloutIdleFail,
	// which frees the source for .13.
	for i := 0; i < 20 && rs.Nodes[1].State == "updating"; i++ {
		now += 10
		rolloutStep(rs, now)
		f.advance()
	}
	if rs.Nodes[1].State != "failed" {
		t.Fatalf("stalled node: %s", rs.Nodes[1].State)
	}
	if s := rs.Nodes[2].State; s != "updating" && s != "done" {
		t.Fatalf("next node after the failure: %s", s)
	}

	// Stop: nothing new starts.
	rs.Stopped = true
	rs.Nodes = append(rs.Nodes, &rolloutNode{IP: "10.30.2.19", State: "pending"})
	rolloutStep(rs, now+10)
	if rs.Nodes[3].State != "pending" {
		t.Errorf("a stopped rollout started %s", rs.Nodes[3].IP)
	}
}

func TestRolloutSkipsUnreachableAndUpToDate(t *testing.T) {
	ips := []string{"10.30.2.11", "10.30.2.12", "10.30.2.13"}
	f := withFakeFleet(t, ips, nil)
	f.down["10.30.2.11"] = true
	f.installed["10.30.2.12"] = "0.6.1"
	rs, err := startRollout("software")
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Nodes) != 2 || rs.Nodes[0].IP != "10.30.2.13" || rs.Nodes[1].State != "failed" {
		t.Fatalf("plan = %+v ... %+v", rs.Nodes[0], rs.Nodes[len(rs.Nodes)-1])
	}
}

func TestRolloutRefusals(t *testing.T) {
	withFakeFleet(t, []string{"10.30.2.11"}, nil)
	if _, err := startRollout("overlay"); err == nil {
		t.Error("started an overlay rollout without an overlay package")
	}
	if _, err := startRollout("software"); err != nil {
		t.Fatal(err)
	}
	if _, err := startRollout("software"); err == nil {
		t.Error("a second rollout started while one runs")
	}
}

func TestPickOfflineSource(t *testing.T) {
	orig := pathScores
	pathScores = func() map[string]float64 { return map[string]float64{"10.30.2.11": 5, "10.30.2.12": 50} }
	t.Cleanup(func() { pathScores = orig })
	if got := pickOfflineSource([]string{"10.30.2.11", "10.30.2.12"}); got != "10.30.2.12" {
		t.Errorf("picked %s, want the faster path", got)
	}
	if got := pickOfflineSource([]string{"10.30.2.12", "127.0.0.1"}); got != "127.0.0.1" {
		t.Errorf("picked %s, want this node's own store", got)
	}
	if got := pickOfflineSource([]string{"10.30.2.40", "10.30.2.41"}); got != "10.30.2.40" {
		t.Errorf("unknown paths: picked %s, want the first", got)
	}
}
