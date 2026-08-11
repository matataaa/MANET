package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	alfredRadioType    = 71
	alfredRadioAckType = 72
	pendingFile        = "/var/run/mesh_pending_radio_state.json"
	ackVersionFile     = "/var/run/mesh_radio_ack_version"
	appliedVersionFile = "/var/run/mesh_applied_radio_version"
	currentStateFile   = "/var/lib/mesh_radio_state.json"
)

var validIfaces = map[string]bool{"wlan0": true, "wlan1": true, "wlan2": true}
var validStates = map[string]bool{"up": true, "down": true}
var batIfRE = regexp.MustCompile(`^\s*([^:\s]+):\s+active\b`)

type radioPkg struct {
	Kind       string            `json:"kind"`
	Version    string            `json:"version"`
	Desired    map[string]string `json:"desired"`
	Targets    interface{}       `json:"targets"`
	ActivateAt int64             `json:"activate_at"`
	IssuedAt   int64             `json:"issued_at"`
}

func run(timeout time.Duration, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = os.Environ()
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.CombinedOutput()
		close(done)
	}()
	select {
	case <-done:
		return string(out), err
	case <-time.After(timeout):
		cmd.Process.Kill()
		return "", fmt.Errorf("timeout after %v", timeout)
	}
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

func readText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func writeText(path, value string) {
	os.WriteFile(path, []byte(value), 0644)
}

func readJSON(path string, v interface{}) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

func writeJSON(path string, v interface{}) {
	data, _ := json.Marshal(v)
	tmp := path + ".tmp"
	os.WriteFile(tmp, data, 0644)
	os.Rename(tmp, path)
}

func isHalowIface(iface string) bool {
	if data := readText("/var/lib/halow_if"); data != "" {
		for _, name := range strings.Fields(data) {
			if name == iface {
				return true
			}
		}
	}
	out, err := run(3*time.Second, "ethtool", "-i", iface)
	if err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "driver:") && strings.Contains(strings.ToLower(line), "morse") {
				return true
			}
		}
	}
	return false
}

func serviceForIface(iface string) string {
	if isHalowIface(iface) {
		return "wpa_supplicant-s1g-" + iface + ".service"
	}
	return "wpa_supplicant@" + iface + ".service"
}

func activeBatIfaces() map[string]bool {
	out, err := run(5*time.Second, "batctl", "if")
	if err != nil {
		return nil
	}
	active := make(map[string]bool)
	for _, line := range strings.Split(out, "\n") {
		if m := batIfRE.FindStringSubmatch(line); m != nil {
			active[m[1]] = true
		}
	}
	return active
}

func batHasIface(iface string) bool {
	out, err := run(5*time.Second, "batctl", "if")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), iface+":") {
			return true
		}
	}
	return false
}

func applyIface(iface, state string) error {
	svc := serviceForIface(iface)
	log.Printf("Applying %s=%s using %s", iface, state, svc)

	if state == "down" {
		run(10*time.Second, "batctl", "if", "del", iface)
		if out, err := run(20*time.Second, "systemctl", "stop", svc); err != nil {
			return fmt.Errorf("systemctl stop %s: %s", svc, strings.TrimSpace(out))
		}
		if out, err := run(10*time.Second, "ip", "link", "set", iface, "down"); err != nil {
			return fmt.Errorf("ip link set %s down: %s", iface, strings.TrimSpace(out))
		}
		return nil
	}

	if out, err := run(10*time.Second, "ip", "link", "set", iface, "up"); err != nil {
		return fmt.Errorf("ip link set %s up: %s", iface, strings.TrimSpace(out))
	}
	if out, err := run(25*time.Second, "systemctl", "start", svc); err != nil {
		return fmt.Errorf("systemctl start %s: %s", svc, strings.TrimSpace(out))
	}
	time.Sleep(2 * time.Second)
	if !batHasIface(iface) {
		if out, err := run(10*time.Second, "batctl", "if", "add", iface); err != nil {
			return fmt.Errorf("batctl if add %s: %s", iface, strings.TrimSpace(out))
		}
	}
	return nil
}

func targetMatches(pkg *radioPkg) bool {
	if pkg.Targets == nil {
		return true
	}
	switch t := pkg.Targets.(type) {
	case string:
		if t == "all" || t == "" {
			return true
		}
		return t == hostname()
	case []interface{}:
		h := hostname()
		for _, v := range t {
			if fmt.Sprintf("%v", v) == h {
				return true
			}
		}
	}
	return false
}

func validatePkg(pkg *radioPkg) (bool, string) {
	if pkg.Kind != "radio_state" {
		return false, "not a radio_state package"
	}
	if pkg.Version == "" {
		return false, "missing version"
	}
	if len(pkg.Desired) == 0 {
		return false, "missing desired state"
	}
	for iface, state := range pkg.Desired {
		if !validIfaces[iface] || !validStates[state] {
			return false, fmt.Sprintf("invalid desired state %s=%s", iface, state)
		}
	}
	if targetMatches(pkg) {
		post := activeBatIfaces()
		if post == nil {
			post = make(map[string]bool)
		}
		for iface, state := range pkg.Desired {
			if state == "down" {
				delete(post, iface)
			} else {
				post[iface] = true
			}
		}
		if len(post) == 0 {
			return false, "refusing to leave node without an active batman-adv radio"
		}
	}
	return true, ""
}

func sendAlfred(typeID int, payload interface{}) bool {
	data, _ := json.Marshal(payload)
	cmd := exec.Command("alfred", "-s", strconv.Itoa(typeID))
	cmd.Stdin = strings.NewReader(string(data))
	err := cmd.Run()
	return err == nil
}

func publishAck(version string, ok bool, errMsg string, target bool) {
	payload := map[string]interface{}{
		"kind":     "radio_ack",
		"version":  version,
		"hostname": hostname(),
		"ok":       ok,
		"error":    errMsg,
		"target":   target,
		"ts":       time.Now().Unix(),
	}
	sendAlfred(alfredRadioAckType, payload)
}

func extractAlfredPayloads(raw string) []radioPkg {
	var pkgs []radioPkg
	tryAdd := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		var p radioPkg
		if json.Unmarshal([]byte(s), &p) == nil && (p.Kind == "radio_state" || p.Kind == "radio_cancel") {
			pkgs = append(pkgs, p)
		}
	}

	tryAdd(raw)

	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &obj) == nil {
		for _, v := range obj {
			var s string
			if json.Unmarshal(v, &s) == nil {
				tryAdd(s)
			} else {
				tryAdd(string(v))
			}
		}
	}

	var arr []json.RawMessage
	if json.Unmarshal([]byte(raw), &arr) == nil {
		for _, v := range arr {
			var s string
			if json.Unmarshal(v, &s) == nil {
				tryAdd(s)
			} else {
				tryAdd(string(v))
			}
		}
	}

	for _, line := range strings.Split(raw, "\n") {
		tryAdd(line)
	}

	return pkgs
}

func latestRadioPackage() *radioPkg {
	out, err := run(5*time.Second, "alfred", "-r", strconv.Itoa(alfredRadioType))
	if err != nil {
		return nil
	}
	pkgs := extractAlfredPayloads(out)
	if len(pkgs) == 0 {
		return nil
	}
	best := pkgs[0]
	for _, p := range pkgs[1:] {
		if p.IssuedAt > best.IssuedAt || (p.IssuedAt == best.IssuedAt && p.Version > best.Version) {
			best = p
		}
	}
	return &best
}

func clearPending(version string) {
	if version != "" {
		var pending radioPkg
		if readJSON(pendingFile, &pending) && pending.Version != version {
			return
		}
	}
	os.Remove(pendingFile)
	os.Remove(ackVersionFile)
}

func recordCurrentState(pkg *radioPkg) {
	var data map[string]interface{}
	readJSON(currentStateFile, &data)
	if data == nil {
		data = make(map[string]interface{})
	}
	desired, _ := data["desired"].(map[string]interface{})
	if desired == nil {
		desired = make(map[string]interface{})
	}
	for k, v := range pkg.Desired {
		desired[k] = v
	}
	data["desired"] = desired
	data["version"] = pkg.Version
	data["updated_at"] = time.Now().Unix()
	writeJSON(currentStateFile, data)
}

func applyPackage(pkg *radioPkg) error {
	if !targetMatches(pkg) {
		log.Printf("Version %s is not targeted at this node; no-op apply", pkg.Version)
		return nil
	}
	ok, errMsg := validatePkg(pkg)
	if !ok {
		return fmt.Errorf("%s", errMsg)
	}
	for iface, state := range pkg.Desired {
		if err := applyIface(iface, state); err != nil {
			return err
		}
	}
	recordCurrentState(pkg)
	return nil
}

func syncOnce() int {
	pkg := latestRadioPackage()
	var pending radioPkg
	hasPending := readJSON(pendingFile, &pending)

	if pkg == nil {
		if hasPending && pending.Version != "" {
			publishAck(pending.Version, true, "", targetMatches(&pending))
		}
		return 0
	}

	if pkg.Kind == "radio_cancel" {
		clearPending(pkg.Version)
		publishAck(pkg.Version, true, "cancelled", false)
		log.Printf("Cancelled pending radio state %s", pkg.Version)
		return 0
	}

	ok, errMsg := validatePkg(pkg)
	target := targetMatches(pkg)
	if !ok {
		publishAck(pkg.Version, false, errMsg, target)
		log.Printf("Rejected radio state %s: %s", pkg.Version, errMsg)
		return 1
	}

	alreadyApplied := readText(appliedVersionFile) == pkg.Version
	if pkg.ActivateAt > 0 && alreadyApplied {
		clearPending(pkg.Version)
		publishAck(pkg.Version, true, "applied", target)
		return 0
	}

	if !hasPending || pending.Version != pkg.Version || pending.ActivateAt != pkg.ActivateAt {
		writeJSON(pendingFile, pkg)
		log.Printf("Staged radio state %s: %v targets=%v activate_at=%d",
			pkg.Version, pkg.Desired, pkg.Targets, pkg.ActivateAt)
	}

	writeText(ackVersionFile, pkg.Version)
	publishAck(pkg.Version, true, "", target)

	if pkg.ActivateAt > 0 && time.Now().Unix() >= pkg.ActivateAt && !alreadyApplied {
		if err := applyPackage(pkg); err != nil {
			publishAck(pkg.Version, false, err.Error(), target)
			log.Printf("Apply failed for radio state %s: %v", pkg.Version, err)
			return 1
		}
		writeText(appliedVersionFile, pkg.Version)
		clearPending(pkg.Version)
		publishAck(pkg.Version, true, "applied", target)
		log.Printf("Applied radio state %s", pkg.Version)
	}
	return 0
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("[mesh-radio-state] ")

	cmd := "sync"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "sync":
		os.Exit(syncOnce())
	case "apply":
		path := pendingFile
		if len(os.Args) > 2 {
			path = os.Args[2]
		}
		var pkg radioPkg
		if !readJSON(path, &pkg) {
			fmt.Fprintf(os.Stderr, "No radio package at %s\n", path)
			os.Exit(1)
		}
		if err := applyPackage(&pkg); err != nil {
			fmt.Fprintf(os.Stderr, "Apply failed: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "usage: mesh-radio-state [sync|apply [path]]\n")
		os.Exit(2)
	}
}
