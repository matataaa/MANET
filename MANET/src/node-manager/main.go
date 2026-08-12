package main

import (
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	confFile         = "/etc/mesh.conf"
	meshIfFile       = "/var/lib/mesh_if"
	radioStateFile   = "/var/lib/mesh_radio_state.json"
	gwCheckInterval  = 60
	loopInterval     = 15
	staticFreq24     = "2412"
	staticFreq5      = "5180"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("[node-manager] ")
	log.Println("starting")

	ensureStaticChannels()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)

	tick := time.NewTicker(loopInterval * time.Second)
	defer tick.Stop()

	var lastGWCheck time.Time

	loop := func() {
		radioStateSync()
		ensureStaticChannels()
		if time.Since(lastGWCheck) >= gwCheckInterval*time.Second {
			gatewayReconcile()
			lastGWCheck = time.Now()
		}
		runElections()
	}

	loop()
	for {
		select {
		case <-tick.C:
			loop()
		case <-sig:
			log.Println("shutting down")
			return
		}
	}
}

func radioStateSync() {
	out, err := exec.Command("/usr/local/bin/mesh-radio-state", "sync").CombinedOutput()
	if err != nil {
		log.Printf("radio-state sync: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func gatewayReconcile() {
	path := "/usr/local/bin/manet-uplink-dispatch.sh"
	if !fileExists(path) {
		return
	}
	out, err := exec.Command(path, "reconcile").CombinedOutput()
	if err != nil {
		log.Printf("uplink-dispatch reconcile: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func loadConf(key string) string {
	data, err := os.ReadFile(confFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && k == key {
			return strings.Trim(v, "'\"")
		}
	}
	return ""
}

func ifaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}

func meshIfaces() (iface24, iface5 string) {
	data, err := os.ReadFile(meshIfFile)
	if err != nil {
		if ifaceExists("wlan0") {
			return "wlan0", ""
		}
		return "", ""
	}
	lines := strings.Fields(strings.TrimSpace(string(data)))
	if len(lines) > 0 && ifaceExists(lines[0]) {
		iface24 = lines[0]
	}
	if len(lines) > 1 && ifaceExists(lines[1]) {
		iface5 = lines[1]
	}
	return
}

func radioIfaceEnabled(iface string) bool {
	data, err := os.ReadFile(radioStateFile)
	if err != nil {
		return true
	}
	var state map[string]interface{}
	if json.Unmarshal(data, &state) != nil {
		return true
	}
	desired, _ := state["desired"].(map[string]interface{})
	if desired == nil {
		return true
	}
	v, _ := desired[iface].(string)
	return v != "down"
}

func getConfFreq(confPath string) string {
	data, err := os.ReadFile(confPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "frequency" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func ensureStaticIfaceChannel(iface, confPath, staticFreq, band string) {
	if iface == "" || confPath == "" {
		return
	}
	if !fileExists(confPath) {
		log.Printf("static mesh WPA config not ready for %s: %s", band, confPath)
		return
	}
	freq := getConfFreq(confPath)
	if freq == staticFreq {
		return
	}

	log.Printf("correcting %s to static channel %s (was %s)", band, staticFreq, freq)
	data, err := os.ReadFile(confPath)
	if err != nil {
		return
	}
	var out strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == "frequency" {
			out.WriteString("frequency=" + staticFreq + "\n")
		} else {
			out.WriteString(line + "\n")
		}
	}
	result := strings.TrimRight(out.String(), "\n") + "\n"
	os.WriteFile(confPath, []byte(result), 0644)

	if radioIfaceEnabled(iface) {
		svc := "wpa_supplicant@" + iface + ".service"
		log.Printf("restarting %s", svc)
		exec.Command("systemctl", "restart", svc).Run()
		time.Sleep(5 * time.Second)
	}
}

func ensureStaticChannels() {
	iface24, iface5 := meshIfaces()
	if iface24 != "" {
		conf24 := "/etc/wpa_supplicant/wpa_supplicant-" + iface24 + ".conf"
		ensureStaticIfaceChannel(iface24, conf24, staticFreq24, "2.4 GHz")
	}
	if iface5 != "" {
		conf5 := "/etc/wpa_supplicant/wpa_supplicant-" + iface5 + ".conf"
		ensureStaticIfaceChannel(iface5, conf5, staticFreq5, "5 GHz")
	}
}

func runElections() {
	matches, _ := filepath.Glob("/usr/local/bin/*-election.sh")
	for _, script := range matches {
		base := filepath.Base(script)
		if strings.Contains(base, "channel-election") {
			continue
		}
		info, err := os.Stat(script)
		if err != nil || info.Mode()&0111 == 0 {
			continue
		}
		cmd := exec.Command(script)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			log.Printf("election %s: %v", base, err)
			continue
		}
		go func(name string, c *exec.Cmd) {
			if err := c.Wait(); err != nil {
				log.Printf("election %s: %v", name, err)
			}
		}(base, cmd)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

