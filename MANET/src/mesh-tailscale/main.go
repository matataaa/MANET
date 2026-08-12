package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
)

const configFile = "/etc/mesh-tailscale.conf"

type Config struct {
	AuthKey    string `json:"auth_key"`
	ExitNode   string `json:"exit_node"`
	Routes     string `json:"advertise_routes"`
	AcceptDNS  bool   `json:"accept_dns"`
	Hostname   string `json:"hostname"`
	ExtraFlags string `json:"extra_flags"`
}

var (
	mu  sync.Mutex
	cfg Config
)

func loadConfig() {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "auth_key":
			cfg.AuthKey = v
		case "exit_node":
			cfg.ExitNode = v
		case "advertise_routes":
			cfg.Routes = v
		case "accept_dns":
			cfg.AcceptDNS = v == "true" || v == "1"
		case "hostname":
			cfg.Hostname = v
		case "extra_flags":
			cfg.ExtraFlags = v
		}
	}
}

func saveConfig() error {
	var lines []string
	lines = append(lines, fmt.Sprintf("auth_key=%s", cfg.AuthKey))
	lines = append(lines, fmt.Sprintf("exit_node=%s", cfg.ExitNode))
	lines = append(lines, fmt.Sprintf("advertise_routes=%s", cfg.Routes))
	if cfg.AcceptDNS {
		lines = append(lines, "accept_dns=true")
	} else {
		lines = append(lines, "accept_dns=false")
	}
	lines = append(lines, fmt.Sprintf("hostname=%s", cfg.Hostname))
	lines = append(lines, fmt.Sprintf("extra_flags=%s", cfg.ExtraFlags))
	return os.WriteFile(configFile, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func runTS(args ...string) ([]byte, error) {
	return exec.Command("tailscale", args...).CombinedOutput()
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	out, err := runTS("status", "--json")
	if err != nil {
		st := map[string]interface{}{"running": false, "error": strings.TrimSpace(string(out))}
		writeJSON(w, 200, st)
		return
	}
	var status interface{}
	if json.Unmarshal(out, &status) != nil {
		writeJSON(w, 200, map[string]interface{}{"running": false, "raw": string(out)})
		return
	}
	writeJSON(w, 200, status)
}

func handlePeers(w http.ResponseWriter, r *http.Request) {
	out, err := runTS("status", "--json")
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"peers": []interface{}{}})
		return
	}
	var status map[string]interface{}
	if json.Unmarshal(out, &status) != nil {
		writeJSON(w, 200, map[string]interface{}{"peers": []interface{}{}})
		return
	}
	peers, ok := status["Peer"].(map[string]interface{})
	if !ok {
		writeJSON(w, 200, map[string]interface{}{"peers": []interface{}{}})
		return
	}
	var list []interface{}
	for _, v := range peers {
		list = append(list, v)
	}
	writeJSON(w, 200, map[string]interface{}{"peers": list})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		mu.Lock()
		safe := cfg
		safe.AuthKey = maskKey(safe.AuthKey)
		mu.Unlock()
		writeJSON(w, 200, safe)
		return
	}

	var body map[string]interface{}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "bad json"})
		return
	}

	mu.Lock()
	if v, ok := body["auth_key"].(string); ok {
		cfg.AuthKey = v
	}
	if v, ok := body["exit_node"].(string); ok {
		cfg.ExitNode = v
	}
	if v, ok := body["advertise_routes"].(string); ok {
		cfg.Routes = v
	}
	if v, ok := body["accept_dns"].(bool); ok {
		cfg.AcceptDNS = v
	}
	if v, ok := body["hostname"].(string); ok {
		cfg.Hostname = v
	}
	if v, ok := body["extra_flags"].(string); ok {
		cfg.ExtraFlags = v
	}
	err := saveConfig()
	mu.Unlock()

	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func handleUp(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	c := cfg
	mu.Unlock()

	args := []string{"up"}
	if c.AuthKey != "" {
		args = append(args, "--authkey="+c.AuthKey)
	}
	if c.ExitNode != "" {
		args = append(args, "--exit-node="+c.ExitNode)
	}
	if c.Routes != "" {
		args = append(args, "--advertise-routes="+c.Routes)
	}
	if c.Hostname != "" {
		args = append(args, "--hostname="+c.Hostname)
	}
	if c.AcceptDNS {
		args = append(args, "--accept-dns=true")
	} else {
		args = append(args, "--accept-dns=false")
	}
	if c.ExtraFlags != "" {
		for _, f := range strings.Fields(c.ExtraFlags) {
			args = append(args, f)
		}
	}

	out, err := runTS(args...)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "error": strings.TrimSpace(string(out))})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "output": strings.TrimSpace(string(out))})
}

func handleDown(w http.ResponseWriter, r *http.Request) {
	out, err := runTS("down")
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "error": strings.TrimSpace(string(out))})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func maskKey(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	port := flag.Int("port", 9810, "listen port")
	flag.Parse()

	loadConfig()

	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/peers", handlePeers)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/up", handleUp)
	http.HandleFunc("/down", handleDown)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("mesh-tailscale listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
