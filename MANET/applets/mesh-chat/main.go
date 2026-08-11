package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"log/syslog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultMulticastAddr = "239.255.50.50:9800"
	maxHistory           = 500
	maxMsgBytes          = 65536
	maxFileSize          = 5 << 20
	presenceInterval     = 15 * time.Second
	peerTimeout          = 60 * time.Second
	dataDir              = "/var/lib/mesh-chat"
)

type Message struct {
	ID   string    `json:"id"`
	From string    `json:"from"`
	To   string    `json:"to,omitempty"`
	Text string    `json:"text,omitempty"`
	Time int64     `json:"time"`
	Type string    `json:"type"`
	File *FileInfo `json:"file,omitempty"`
}

type FileInfo struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	ID       string `json:"id"`
	SenderIP string `json:"sender_ip"`
}

type Peer struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
	LastSeen int64  `json:"last_seen"`
}

type WSCommand struct {
	Action string `json:"action"`
	Text   string `json:"text,omitempty"`
	To     string `json:"to,omitempty"`
}

type Config struct {
	MulticastAddr string
	Iface         string
	Port          int
}

var (
	upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	hostname string
	meshIP   string
	cfg      Config

	mu      sync.Mutex
	clients = map[*websocket.Conn]bool{}
	history []Message
	peers   = map[string]*Peer{}
	seenIDs = map[string]bool{}
)

func genID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return fmt.Sprintf("%s-%d-%s", hostname, time.Now().UnixMilli(), hex.EncodeToString(b))
}

func getMeshIP(ifaceName string) string {
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}

func isDuplicate(id string) bool {
	if id == "" {
		return false
	}
	if seenIDs[id] {
		return true
	}
	seenIDs[id] = true
	if len(seenIDs) > maxHistory*3 {
		seenIDs = map[string]bool{}
		for _, m := range history {
			seenIDs[m.ID] = true
		}
	}
	return false
}

func historyPath() string { return filepath.Join(dataDir, "history.jsonl") }
func filesPath() string   { return filepath.Join(dataDir, "files") }

func persistMsg(msg Message) {
	os.MkdirAll(dataDir, 0755)
	f, err := os.OpenFile(historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	data, _ := json.Marshal(msg)
	f.Write(append(data, '\n'))
}

func loadHistory() {
	data, err := os.ReadFile(historyPath())
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	start := 0
	if len(lines) > maxHistory {
		start = len(lines) - maxHistory
	}
	for _, line := range lines[start:] {
		if line == "" {
			continue
		}
		var msg Message
		if json.Unmarshal([]byte(line), &msg) == nil {
			if msg.Type == "" {
				msg.Type = "text"
			}
			if msg.ID == "" {
				msg.ID = genID()
			}
			history = append(history, msg)
			seenIDs[msg.ID] = true
		}
	}
	log.Printf("loaded %d messages from disk", len(history))
}

func broadcast(msg Message) {
	mu.Lock()
	defer mu.Unlock()

	if msg.Type == "text" || msg.Type == "file" {
		history = append(history, msg)
		if len(history) > maxHistory {
			history = history[len(history)-maxHistory:]
		}
		seenIDs[msg.ID] = true
		persistMsg(msg)
	}

	dead := []*websocket.Conn{}
	for c := range clients {
		if err := c.WriteJSON(msg); err != nil {
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		delete(clients, c)
		c.Close()
	}
}

func sendPeers() {
	mu.Lock()
	defer mu.Unlock()
	list := []Peer{{Hostname: hostname, IP: meshIP, LastSeen: time.Now().Unix()}}
	for _, p := range peers {
		list = append(list, *p)
	}
	payload := map[string]interface{}{"type": "peers", "peers": list}
	dead := []*websocket.Conn{}
	for c := range clients {
		if err := c.WriteJSON(payload); err != nil {
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		delete(clients, c)
		c.Close()
	}
}

func multicastSend(msg Message) {
	addr, _ := net.ResolveUDPAddr("udp4", cfg.MulticastAddr)
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	data, _ := json.Marshal(msg)
	conn.Write(data)
}

func multicastListener() {
	addr, err := net.ResolveUDPAddr("udp4", cfg.MulticastAddr)
	if err != nil {
		log.Fatalf("resolve multicast: %v", err)
	}
	var ifi *net.Interface
	if cfg.Iface != "" {
		ifi, _ = net.InterfaceByName(cfg.Iface)
	}
	conn, err := net.ListenMulticastUDP("udp4", ifi, addr)
	if err != nil {
		log.Fatalf("listen multicast: %v", err)
	}
	conn.SetReadBuffer(65536)

	buf := make([]byte, maxMsgBytes)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		var msg Message
		if json.Unmarshal(buf[:n], &msg) != nil {
			continue
		}

		if msg.Type == "presence" {
			if msg.From != hostname {
				mu.Lock()
				peers[msg.From] = &Peer{Hostname: msg.From, IP: msg.Text, LastSeen: time.Now().Unix()}
				mu.Unlock()
				sendPeers()
			}
			continue
		}

		if msg.Type == "sync_req" {
			if msg.From != hostname {
				go handleSyncReq(msg)
			}
			continue
		}

		if msg.From == hostname {
			continue
		}

		mu.Lock()
		dup := isDuplicate(msg.ID)
		mu.Unlock()
		if dup {
			continue
		}

		log.Printf("rx %s from %s", msg.Type, msg.From)
		broadcast(msg)
	}
}

func handleSyncReq(req Message) {
	mu.Lock()
	var msgs []Message
	for _, m := range history {
		if m.Time > req.Time {
			msgs = append(msgs, m)
		}
	}
	mu.Unlock()
	for _, m := range msgs {
		multicastSend(m)
		time.Sleep(5 * time.Millisecond)
	}
}

func presenceLoop() {
	time.Sleep(2 * time.Second)

	multicastSend(Message{Type: "presence", From: hostname, Text: meshIP, Time: time.Now().Unix()})

	mu.Lock()
	var lastTime int64
	if len(history) > 0 {
		lastTime = history[len(history)-1].Time
	}
	mu.Unlock()
	multicastSend(Message{Type: "sync_req", From: hostname, Time: lastTime})

	tick := time.NewTicker(presenceInterval)
	for range tick.C {
		multicastSend(Message{Type: "presence", From: hostname, Text: meshIP, Time: time.Now().Unix()})

		mu.Lock()
		now := time.Now().Unix()
		changed := false
		for h, p := range peers {
			if now-p.LastSeen > int64(peerTimeout.Seconds()) {
				delete(peers, h)
				changed = true
			}
		}
		mu.Unlock()
		if changed {
			sendPeers()
		}
	}
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	mu.Lock()
	clients[conn] = true
	for _, msg := range history {
		conn.WriteJSON(msg)
	}
	list := []Peer{{Hostname: hostname, IP: meshIP, LastSeen: time.Now().Unix()}}
	for _, p := range peers {
		list = append(list, *p)
	}
	conn.WriteJSON(map[string]interface{}{"type": "peers", "peers": list})
	mu.Unlock()

	defer func() {
		mu.Lock()
		delete(clients, conn)
		mu.Unlock()
		conn.Close()
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}

		var cmd WSCommand
		if json.Unmarshal(raw, &cmd) == nil && cmd.Action != "" {
			switch cmd.Action {
			case "send":
				text := strings.TrimSpace(cmd.Text)
				if text == "" || len(text) > 2000 {
					continue
				}
				msg := Message{ID: genID(), From: hostname, To: cmd.To, Text: text, Time: time.Now().Unix(), Type: "text"}
				broadcast(msg)
				multicastSend(msg)
			case "sync":
				mu.Lock()
				var t int64
				if len(history) > 0 {
					t = history[len(history)-1].Time - 3600
				}
				mu.Unlock()
				multicastSend(Message{Type: "sync_req", From: hostname, Time: t})
			}
			continue
		}

		// backward compat: plain text
		text := strings.TrimSpace(string(raw))
		if text == "" || len(text) > 2000 {
			continue
		}
		msg := Message{ID: genID(), From: hostname, Text: text, Time: time.Now().Unix(), Type: "text"}
		broadcast(msg)
		multicastSend(msg)
	}
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.ParseMultipartForm(maxFileSize)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "no file"})
		return
	}
	defer file.Close()
	if hdr.Size > maxFileSize {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "max 5MB"})
		return
	}

	id := genID()
	ext := filepath.Ext(hdr.Filename)
	stored := id + ext

	os.MkdirAll(filesPath(), 0755)
	dst, err := os.Create(filepath.Join(filesPath(), stored))
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "storage error"})
		return
	}
	io.Copy(dst, file)
	dst.Close()

	to := r.FormValue("to")
	msg := Message{
		ID: id, From: hostname, To: to, Time: time.Now().Unix(), Type: "file",
		File: &FileInfo{Name: hdr.Filename, Size: hdr.Size, ID: stored, SenderIP: meshIP},
	}
	broadcast(msg)
	multicastSend(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "id": stored})
}

func handleFiles(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/files/")
	if name == "" || strings.Contains(name, "..") {
		http.Error(w, "not found", 404)
		return
	}
	http.ServeFile(w, r, filepath.Join(filesPath(), name))
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	nc, nh, np := len(clients), len(history), len(peers)+1
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok", "hostname": hostname, "clients": nc, "history": nh, "peers": np,
	})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"multicast_addr": cfg.MulticastAddr, "interface": cfg.Iface, "port": cfg.Port, "max_history": maxHistory,
	})
}

func loadConfig(path string) Config {
	c := Config{MulticastAddr: defaultMulticastAddr, Iface: "br0", Port: 9800}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		switch k {
		case "MULTICAST_ADDR":
			c.MulticastAddr = v
		case "INTERFACE":
			c.Iface = v
		case "PORT":
			fmt.Sscanf(v, "%d", &c.Port)
		}
	}
	return c
}

func main() {
	port := flag.Int("port", 9800, "HTTP/WebSocket port")
	iface := flag.String("iface", "br0", "network interface")
	configFile := flag.String("config", "/etc/mesh-chat.conf", "config file")
	flag.Parse()

	cfg = loadConfig(*configFile)
	if *port != 9800 {
		cfg.Port = *port
	}
	if *iface != "br0" {
		cfg.Iface = *iface
	}

	if w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "mesh-chat"); err == nil {
		log.SetOutput(w)
	}

	hostname, _ = os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	meshIP = getMeshIP(cfg.Iface)

	loadHistory()
	go multicastListener()
	go presenceLoop()

	http.HandleFunc("/ws", handleWS)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/config", handleConfig)
	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/files/", handleFiles)

	listen := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	log.Printf("mesh-chat v2: host=%s ip=%s listen=%s mcast=%s", hostname, meshIP, listen, cfg.MulticastAddr)
	log.Fatal(http.ListenAndServe(listen, nil))
}
