package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	configDir  = "/etc/wireguard"
	configFile = "/etc/mesh-wireguard.conf"
)

type WGPeer struct {
	PublicKey  string `json:"public_key"`
	Endpoint  string `json:"endpoint"`
	AllowedIPs string `json:"allowed_ips"`
	Keepalive  int    `json:"keepalive"`
}

type Config struct {
	Interface  string   `json:"interface"`
	Address    string   `json:"address"`
	ListenPort int      `json:"listen_port"`
	PrivateKey string   `json:"private_key"`
	DNS        string   `json:"dns"`
	Peers      []WGPeer `json:"peers"`
}

var (
	mu  sync.Mutex
	cfg Config
)

func loadConfig() {
	cfg.Interface = "wg0"
	cfg.ListenPort = 51820
	data, err := os.ReadFile(configFile)
	if err != nil {
		return
	}
	json.Unmarshal(data, &cfg)
}

func saveConfig() error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configFile, data, 0600)
}

// ifaceRE is wg-quick's own rule for interface names. The name also
// becomes the file name under configDir, so it must not hold a path.
var ifaceRE = regexp.MustCompile(`^[a-zA-Z0-9_=+.-]{1,15}$`)

// validateConfig rejects what generateWGConf can't write safely. Every
// value lands unescaped on its own "Key = value" line of a wg-quick
// config, so a line break would start a new directive -- and wg-quick
// runs PostUp/PreUp lines as root shell commands.
func validateConfig(c Config) error {
	if c.Interface != "" && !ifaceRE.MatchString(c.Interface) {
		return fmt.Errorf("invalid interface name")
	}
	fields := []string{c.Address, c.PrivateKey, c.DNS}
	for _, p := range c.Peers {
		fields = append(fields, p.PublicKey, p.Endpoint, p.AllowedIPs)
	}
	for _, f := range fields {
		for _, ch := range f {
			if ch < 0x20 || ch == 0x7f {
				return fmt.Errorf("values must not contain control characters")
			}
		}
	}
	return nil
}

func generateWGConf() error {
	os.MkdirAll(configDir, 0700)
	var sb strings.Builder
	sb.WriteString("[Interface]\n")
	if cfg.PrivateKey != "" {
		sb.WriteString(fmt.Sprintf("PrivateKey = %s\n", cfg.PrivateKey))
	}
	if cfg.Address != "" {
		sb.WriteString(fmt.Sprintf("Address = %s\n", cfg.Address))
	}
	if cfg.ListenPort > 0 {
		sb.WriteString(fmt.Sprintf("ListenPort = %d\n", cfg.ListenPort))
	}
	if cfg.DNS != "" {
		sb.WriteString(fmt.Sprintf("DNS = %s\n", cfg.DNS))
	}
	for _, p := range cfg.Peers {
		sb.WriteString("\n[Peer]\n")
		sb.WriteString(fmt.Sprintf("PublicKey = %s\n", p.PublicKey))
		if p.Endpoint != "" {
			sb.WriteString(fmt.Sprintf("Endpoint = %s\n", p.Endpoint))
		}
		if p.AllowedIPs != "" {
			sb.WriteString(fmt.Sprintf("AllowedIPs = %s\n", p.AllowedIPs))
		}
		if p.Keepalive > 0 {
			sb.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", p.Keepalive))
		}
	}
	iface := cfg.Interface
	if iface == "" {
		iface = "wg0"
	}
	return os.WriteFile(filepath.Join(configDir, iface+".conf"), []byte(sb.String()), 0600)
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	iface := cfg.Interface
	if iface == "" {
		iface = "wg0"
	}
	out, err := exec.Command("wg", "show", iface, "dump").CombinedOutput()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{
			"running":   false,
			"interface": iface,
			"error":     strings.TrimSpace(string(out)),
		})
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	result := map[string]interface{}{
		"running":   true,
		"interface": iface,
	}
	if len(lines) > 0 {
		parts := strings.Split(lines[0], "\t")
		if len(parts) >= 4 {
			result["public_key"] = parts[1]
			result["listen_port"] = parts[2]
		}
	}

	var peers []map[string]string
	for _, line := range lines[1:] {
		parts := strings.Split(line, "\t")
		if len(parts) < 8 {
			continue
		}
		peers = append(peers, map[string]string{
			"public_key":           parts[0],
			"endpoint":             parts[2],
			"allowed_ips":          parts[3],
			"latest_handshake":     parts[4],
			"transfer_rx":         parts[5],
			"transfer_tx":         parts[6],
			"persistent_keepalive": parts[7],
		})
	}
	if peers == nil {
		peers = []map[string]string{}
	}
	result["peers"] = peers
	writeJSON(w, 200, result)
}

func handlePeers(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	peers := cfg.Peers
	mu.Unlock()
	if peers == nil {
		peers = []WGPeer{}
	}
	writeJSON(w, 200, map[string]interface{}{"peers": peers})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		mu.Lock()
		safe := cfg
		safe.PrivateKey = maskKey(safe.PrivateKey)
		mu.Unlock()
		writeJSON(w, 200, safe)
		return
	}

	var body Config
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "bad json"})
		return
	}
	if err := validateConfig(body); err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	mu.Lock()
	if body.Interface != "" {
		cfg.Interface = body.Interface
	}
	if body.Address != "" {
		cfg.Address = body.Address
	}
	if body.ListenPort > 0 {
		cfg.ListenPort = body.ListenPort
	}
	if body.PrivateKey != "" {
		cfg.PrivateKey = body.PrivateKey
	}
	if body.DNS != "" {
		cfg.DNS = body.DNS
	}
	if body.Peers != nil {
		cfg.Peers = body.Peers
	}
	err := saveConfig()
	if err == nil {
		err = generateWGConf()
	}
	mu.Unlock()

	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

// requirePost keeps actions off GET, so a link or <img> can't trigger them.
func requirePost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]interface{}{"ok": false, "error": "POST required"})
		return false
	}
	return true
}

func handleUp(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	mu.Lock()
	generateWGConf()
	iface := cfg.Interface
	mu.Unlock()
	if iface == "" {
		iface = "wg0"
	}
	out, err := exec.Command("wg-quick", "up", iface).CombinedOutput()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "error": strings.TrimSpace(string(out))})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "output": strings.TrimSpace(string(out))})
}

func handleDown(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	iface := cfg.Interface
	if iface == "" {
		iface = "wg0"
	}
	out, err := exec.Command("wg-quick", "down", iface).CombinedOutput()
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"ok": false, "error": strings.TrimSpace(string(out))})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func handleGenKey(w http.ResponseWriter, r *http.Request) {
	if !requirePost(w, r) {
		return
	}
	privOut, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "genkey failed"})
		return
	}
	priv := strings.TrimSpace(string(privOut))
	cmd := exec.Command("wg", "pubkey")
	cmd.Stdin = strings.NewReader(priv)
	pubOut, err := cmd.Output()
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "pubkey failed"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"private_key": priv,
		"public_key":  strings.TrimSpace(string(pubOut)),
	})
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
	port := flag.Int("port", 9811, "listen port")
	flag.Parse()

	loadConfig()

	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/peers", handlePeers)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/up", handleUp)
	http.HandleFunc("/down", handleDown)
	http.HandleFunc("/genkey", handleGenKey)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("mesh-wireguard listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
