package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	confFile           = "/etc/mesh.conf"
	releaseVersionFile = "/etc/manet_release_version.txt"
	overlayVersionFile = "/etc/manet_overlay_version.txt"
	statusFile         = "/var/run/manet_update_status.json"
	triggerFile        = "/run/manet-update-trigger"
	tarballPath        = "/root/tools.tar.gz"
	overlayTarballPath = "/root/sbc-overlay.tar.gz"
	defaultInterval    = 6 * time.Hour
	cooldown           = 1 * time.Hour
	httpTimeout        = 30 * time.Second
	downloadTimeout    = 10 * time.Minute
	startupDelay       = 5 * time.Minute
	minRebootJitter    = 1 * time.Minute
	maxRebootJitter    = 15 * time.Minute
	defaultMinMbps     = 10
)

var (
	lastCheck time.Time
	mu        sync.Mutex
	// client is for tiny version-check text fetches — keep its timeout short.
	client = &http.Client{Timeout: httpTimeout}
	// downloadClient is for full tarball GETs, which can be tens of MB over a
	// mesh link well under 10 Mbps; httpTimeout's 30s is a hard deadline on
	// the whole body read and reliably clips real downloads on slow uplinks.
	downloadClient = &http.Client{Timeout: downloadTimeout}
	Version        = "dev"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(Version)
		return
	}

	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("[node-update] ")
	log.Printf("starting (version %s)", Version)

	board := boardType()
	if board == "unknown" {
		log.Fatal("unknown board type")
	}
	log.Printf("board: %s", board)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGUSR1)

	timer := time.NewTimer(startupDelay)

	for {
		select {
		case <-timer.C:
			runChecks(board, trigger{})
			timer.Reset(defaultInterval)
		case s := <-sig:
			switch s {
			case syscall.SIGHUP:
				mu.Lock()
				since := time.Since(lastCheck)
				mu.Unlock()
				if since < cooldown {
					log.Printf("SIGHUP: cooldown (%s since last check)", since.Round(time.Second))
					continue
				}
				log.Println("SIGHUP: checking for updates")
				runChecks(board, trigger{})
				timer.Reset(defaultInterval)
			case syscall.SIGUSR1:
				t := readAndClearTrigger()
				if !t.software && !t.overlay {
					log.Println("SIGUSR1: no trigger file, ignoring")
					continue
				}
				log.Printf("SIGUSR1: manual update requested (software=%v overlay=%v)", t.software, t.overlay)
				runChecks(board, t)
				timer.Reset(defaultInterval)
			default:
				log.Println("shutting down")
				return
			}
		}
	}
}

// trigger marks which channel(s) a manual "Update Now" / fleet force-update
// request wants applied right now, bypassing the auto_update flags and the
// bandwidth gate — the UI already warned the operator before sending this.
type trigger struct {
	software bool
	overlay  bool
}

func readAndClearTrigger() trigger {
	data, err := os.ReadFile(triggerFile)
	os.Remove(triggerFile)
	if err != nil {
		return trigger{}
	}
	switch strings.TrimSpace(string(data)) {
	case "software":
		return trigger{software: true}
	case "overlay":
		return trigger{overlay: true}
	case "both":
		return trigger{software: true, overlay: true}
	}
	return trigger{}
}

// channelStatus is one channel's entry in the status file manet-ctrl reads
// to drive the update-available banner.
type channelStatus struct {
	Local     string `json:"local"`
	Remote    string `json:"remote"`
	Available bool   `json:"available"`
}

type updateStatus struct {
	Software    channelStatus `json:"software"`
	Overlay     channelStatus `json:"overlay"`
	UplinkMbps  float64       `json:"uplink_mbps"`
	UplinkType  string        `json:"uplink_type"`
	LastChecked string        `json:"last_checked"`
	// Phase reports what an in-progress apply is actually doing right now
	// ("idle" the rest of the time) — the UI polls this to show real
	// progress instead of a static "triggered" toast with no further
	// feedback until the node disappears to reboot.
	Phase string `json:"phase"`
	// RebootAt is set only while Phase == "rebooting" — the RFC3339 time
	// the jittered `shutdown -r` will actually fire, so the UI can render
	// a real countdown instead of a static "rebooting" message.
	RebootAt string `json:"reboot_at,omitempty"`
}

// runChecks always detects on both channels (regardless of the auto_update
// flags — that's the point: the operator gets notified even when automatic
// apply is off) and writes the status file, then applies at most one
// channel per cycle: a manual trigger applies unconditionally if available;
// otherwise automatic apply requires both the relevant auto_update flag and
// a passing bandwidth gate.
func runChecks(board string, manual trigger) {
	mu.Lock()
	lastCheck = time.Now()
	mu.Unlock()

	baseURL := strings.TrimRight(confValue("update_url"), "/")
	uplinkMbps, uplinkType := queryUplink()

	status := updateStatus{
		UplinkMbps:  uplinkMbps,
		UplinkType:  uplinkType,
		LastChecked: time.Now().UTC().Format(time.RFC3339),
		Phase:       "idle",
	}

	if baseURL == "" {
		log.Println("OTA disabled (update_url not set in mesh.conf)")
		writeStatus(status)
		return
	}

	swLocal, swRemote, swAvailable, swErr := detectSoftware(baseURL)
	status.Software = channelStatus{Local: swLocal, Remote: swRemote, Available: swAvailable}
	if swErr != nil {
		log.Printf("release version check failed: %v", swErr)
	}

	ovLocal, ovRemote, ovAvailable, ovErr := detectOverlay(baseURL, board)
	status.Overlay = channelStatus{Local: ovLocal, Remote: ovRemote, Available: ovAvailable}
	if ovErr != nil {
		log.Printf("overlay version check failed: %v", ovErr)
	}

	writeStatus(status)

	autoUpdate := isAffirmative(confValue("auto_update"), true)
	autoUpdateOverlay := isAffirmative(confValue("auto_update_overlay"), false)
	gateOK := bandwidthOK(uplinkMbps, uplinkType)

	// Software and overlay are evaluated independently — NOT a single
	// either/or choice. They used to share one switch where the first
	// matching case (always software, by declaration order) won and the
	// other channel was silently skipped for the cycle. That meant a
	// manual/fleet "both" trigger only ever actually applied software,
	// with overlay dropped unless auto_update_overlay also happened to be
	// on. Both can apply in the same cycle now; applySoftware/applyOverlay
	// each end in scheduleReboot(), and calling that twice is harmless —
	// the later call just reschedules the pending shutdown, so this is
	// still at most one reboot, not two.
	switch {
	case manual.software && swAvailable:
		log.Printf("manual update: release v%s -> v%s", swLocal, swRemote)
		applySoftware(baseURL, board, swRemote, &status)
	case swAvailable && autoUpdate && gateOK:
		log.Printf("auto update: release v%s -> v%s", swLocal, swRemote)
		applySoftware(baseURL, board, swRemote, &status)
	case swAvailable && autoUpdate && !gateOK:
		log.Printf("release v%s available but uplink (%.1f Mbps, %s) is below the bandwidth gate — skipping automatic apply", swRemote, uplinkMbps, uplinkType)
	}

	switch {
	case manual.overlay && ovAvailable:
		log.Printf("manual overlay update: v%s -> v%s", ovLocal, ovRemote)
		applyOverlay(baseURL, board, ovRemote, &status)
	case ovAvailable && autoUpdateOverlay && gateOK:
		log.Printf("auto overlay update: v%s -> v%s", ovLocal, ovRemote)
		applyOverlay(baseURL, board, ovRemote, &status)
	case ovAvailable && autoUpdateOverlay && !gateOK:
		log.Printf("overlay v%s available but uplink (%.1f Mbps, %s) is below the bandwidth gate — skipping automatic apply", ovRemote, uplinkMbps, uplinkType)
	}
}

// isAffirmative parses a mesh.conf y/n-style value. An unset/empty value
// falls back to defaultOn — true for auto_update (today's opt-out default,
// preserved for compatibility with already-provisioned nodes), false for
// auto_update_overlay (opt-in — a missing value must never mean "enabled"
// for something with no rollback).
func isAffirmative(v string, defaultOn bool) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return defaultOn
	}
	if defaultOn {
		return v != "n" && v != "no" && v != "0"
	}
	return v == "y" || v == "yes" || v == "1"
}

// bandwidthOK gates automatic apply only — a manual trigger bypasses this
// entirely (the UI already showed the warning before the operator confirmed).
// An unreachable/unreadable manet-ctrl ("unknown") fails closed: no signal
// means no automatic download, not an assumed-fine default.
func bandwidthOK(uplinkMbps float64, uplinkType string) bool {
	if uplinkType == "wired" {
		return true
	}
	if uplinkType == "unknown" {
		return false
	}
	min, err := strconv.ParseFloat(strings.TrimSpace(confValue("auto_update_min_mbps")), 64)
	if err != nil || min <= 0 {
		min = defaultMinMbps
	}
	return uplinkMbps >= min
}

// localAPIClient talks to manet-ctrl on this same node over HTTPS with
// verification skipped, matching manet-ctrl's own peerTLSConfig pattern for
// internal calls — every node's cert is self-signed, and manet-ctrl's plain
// HTTP port only ever serves a redirect to HTTPS (never content), so a
// verifying client here would just fail every request.
var localAPIClient = &http.Client{
	Timeout:   5 * time.Second,
	Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
}

// queryUplink asks manet-ctrl's local status endpoint for this node's
// current best throughput toward its gateway, rather than re-implementing
// batman-adv parsing here — manet-ctrl already computes this for the
// topology "Real Rate" column.
func queryUplink() (mbps float64, uplinkType string) {
	resp, err := localAPIClient.Get("https://localhost/api/local")
	if err != nil {
		return 0, "unknown"
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, "unknown"
	}
	var v struct {
		UplinkMbps float64 `json:"uplink_mbps"`
		UplinkType string  `json:"uplink_type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&v); err != nil {
		return 0, "unknown"
	}
	if v.UplinkType == "" {
		return 0, "unknown"
	}
	return v.UplinkMbps, v.UplinkType
}

func writeStatus(status updateStatus) {
	data, err := json.Marshal(status)
	if err != nil {
		log.Printf("failed to marshal update status: %v", err)
		return
	}
	tmp := statusFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("failed to write update status: %v", err)
		return
	}
	if err := os.Rename(tmp, statusFile); err != nil {
		log.Printf("failed to publish update status: %v", err)
	}
}

// detectSoftware checks the software-stack channel without downloading
// anything. Returns the local/remote version strings and whether remote is
// strictly newer.
func detectSoftware(baseURL string) (local, remote string, available bool, err error) {
	local = readLocalReleaseVersion()
	localVer, _ := parseSemver(local)

	remote, err = fetchText(baseURL + "/manet_release_version.txt")
	if err != nil {
		return local, "", false, err
	}
	remoteVer, ok := parseSemver(remote)
	if !ok {
		return local, remote, false, fmt.Errorf("unparsable remote version %q", remote)
	}
	return local, remote, semverGreater(remoteVer, localVer), nil
}

// applySoftware downloads, extracts, and records the given already-detected
// release, then schedules a reboot. Callers (automatic or manual) have
// already decided this should happen.
func applySoftware(baseURL, board, remoteVerStr string, status *updateStatus) {
	status.Phase = "downloading software"
	writeStatus(*status)

	tarballURL := fmt.Sprintf("%s/%s-tools.tar.gz", baseURL, board)
	if err := download(tarballURL, tarballPath); err != nil {
		log.Printf("download failed: %v", err)
		status.Phase = "idle"
		writeStatus(*status)
		return
	}

	status.Phase = "extracting software"
	writeStatus(*status)

	// --no-overwrite-dir: an archive must never change the mode of an
	// existing directory, least of all /. Directory modes in the tarball
	// come from the build machine's mktemp -d stage (0700) or umask; letting
	// tar apply that to / would lock out every non-root process.
	out, err := exec.Command("tar", "-zxf", tarballPath, "--no-overwrite-dir", "-C", "/").CombinedOutput()
	if err != nil {
		log.Printf("extract failed: %v: %s", err, strings.TrimSpace(string(out)))
		os.Remove(tarballPath)
		status.Phase = "idle"
		writeStatus(*status)
		return
	}
	os.Remove(tarballPath)

	// Record the already-validated remote string ourselves rather than
	// trusting whatever the tarball's own etc/manet_release_version.txt
	// happens to contain. If a release is ever built from a tag that isn't
	// a clean X.Y.Z (a stray "-rc1" suffix, a typo), the embedded copy would
	// fail parseSemver on the next check and read back as 0.0.0 — making
	// the node re-detect "update available" and reboot forever, since it
	// could never catch up. remoteVerStr already parsed successfully above.
	if err := os.WriteFile(releaseVersionFile, []byte(remoteVerStr+"\n"), 0644); err != nil {
		log.Printf("failed to record release version: %v", err)
	}
	log.Printf("updated to release v%s", remoteVerStr)

	scheduleReboot(status)
}

// detectOverlay checks the SBC overlay channel (kernel, modules, firmware)
// without downloading anything.
func detectOverlay(baseURL, board string) (local, remote string, available bool, err error) {
	local = readLocalOverlayVersion()
	localVer, _ := parseOverlayVersion(local)

	remote, err = fetchText(fmt.Sprintf("%s/%s-overlay-version.txt", baseURL, board))
	if err != nil {
		return local, "", false, err
	}
	remoteVer, ok := parseOverlayVersion(remote)
	if !ok {
		return local, remote, false, fmt.Errorf("unparsable remote version %q", remote)
	}
	return local, remote, remoteVer > localVer, nil
}

// applyOverlay downloads, extracts, and records the given already-detected
// overlay, then schedules a reboot.
func applyOverlay(baseURL, board, remoteVerStr string, status *updateStatus) {
	status.Phase = "downloading overlay"
	writeStatus(*status)

	overlayURL := fmt.Sprintf("%s/%s-sbc-overlay.tar.gz", baseURL, board)
	if err := download(overlayURL, overlayTarballPath); err != nil {
		log.Printf("overlay download failed: %v", err)
		status.Phase = "idle"
		writeStatus(*status)
		return
	}

	status.Phase = "extracting overlay"
	writeStatus(*status)

	out, err := exec.Command("tar", "-zxf", overlayTarballPath, "--no-overwrite-dir", "-C", "/").CombinedOutput()
	if err != nil {
		log.Printf("overlay extract failed: %v: %s", err, strings.TrimSpace(string(out)))
		os.Remove(overlayTarballPath)
		status.Phase = "idle"
		writeStatus(*status)
		return
	}
	os.Remove(overlayTarballPath)

	if err := os.WriteFile(overlayVersionFile, []byte(remoteVerStr+"\n"), 0644); err != nil {
		log.Printf("failed to record overlay version: %v", err)
	}
	log.Printf("updated overlay to v%s", remoteVerStr)

	scheduleReboot(status)
}

// scheduleReboot asks the kernel to reboot after a random delay rather than
// immediately. Every node on the mesh runs the same check cadence, so an
// immediate reboot here would tend to drop the whole fleet (and any gateway)
// within moments of a release going out; the jitter spreads that out.
func scheduleReboot(status *updateStatus) {
	jitter := minRebootJitter + time.Duration(rand.Int63n(int64(maxRebootJitter-minRebootJitter)))
	mins := int(jitter / time.Minute)
	if mins < 1 {
		mins = 1
	}

	status.Phase = "rebooting"
	status.RebootAt = time.Now().Add(time.Duration(mins) * time.Minute).UTC().Format(time.RFC3339)
	writeStatus(*status)

	log.Printf("scheduling reboot in %d minute(s) to apply update", mins)
	out, err := exec.Command("shutdown", "-r", fmt.Sprintf("+%d", mins), "MANET OTA update applied, rebooting").CombinedOutput()
	if err != nil {
		log.Printf("failed to schedule reboot: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func boardType() string {
	data, err := os.ReadFile("/proc/device-tree/model")
	if err != nil {
		return "unknown"
	}
	model := strings.TrimRight(string(data), "\x00")
	switch {
	case strings.Contains(model, "Raspberry Pi 5"):
		return "rpi5"
	case strings.Contains(model, "Raspberry Pi 4"),
		strings.Contains(model, "Raspberry Pi Compute Module 4"):
		return "cm4"
	}
	return "unknown"
}

func readLocalReleaseVersion() string {
	data, err := os.ReadFile(releaseVersionFile)
	if err != nil {
		return "0.0.0"
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) == 0 || lines[0] == "" {
		return "0.0.0"
	}
	return lines[0]
}

// parseSemver parses a plain "X.Y.Z" string. A node that has never received
// a release, or a remote value that fails to parse, is treated as 0.0.0 by
// callers — never as "greater" — so malformed input can't spuriously trigger
// or block an update.
func parseSemver(s string) ([3]int, bool) {
	var v [3]int
	parts := strings.SplitN(strings.TrimSpace(s), ".", 3)
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

func semverGreater(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}

func readLocalOverlayVersion() string {
	data, err := os.ReadFile(overlayVersionFile)
	if err != nil {
		return "0"
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) == 0 || lines[0] == "" {
		return "0"
	}
	return lines[0]
}

// parseOverlayVersion parses the SBC overlay's existing plain-decimal
// numbering (e.g. "0.538") — deliberately not the X.Y.Z scheme used for
// software releases, since this value is "not a version we invent" (see
// docs/VERSIONING.md): it mirrors whatever the vendor/CI stamps.
func parseOverlayVersion(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v, err == nil
}

func confValue(key string) string {
	f, err := os.Open(confFile)
	if err != nil {
		return ""
	}
	defer f.Close()

	prefix := strings.ToLower(key) + "="
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(strings.ToLower(line), prefix) {
			val := line[len(prefix):]
			val = strings.Trim(val, "\"'")
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func fetchText(url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Cache-Control", "no-cache, no-store")
	req.Header.Set("Pragma", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}

	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) == 0 {
		return "", fmt.Errorf("empty response from %s", url)
	}
	return lines[0], nil
}

func download(url, dest string) error {
	log.Printf("downloading %s", url)

	resp, err := downloadClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	n, err := io.Copy(f, resp.Body)
	if err != nil {
		os.Remove(dest)
		return err
	}

	log.Printf("downloaded %d bytes", n)
	return nil
}
