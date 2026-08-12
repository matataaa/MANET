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
	maxMessages          = 1000
	maxMsgBytes          = 65536
	maxFileSize          = 100 << 20
	presenceInterval     = 15 * time.Second
	peerTimeout          = 90 * time.Second
	deliveryTimeout      = 5 * time.Second
	dataDir              = "/var/lib/mesh-chat"
)

type Message struct {
	ID     string   `json:"id"`
	From   string   `json:"from"`
	FromIP string   `json:"from_ip"`
	To     []string `json:"to"`
	Type   string   `json:"type"`
	Body   string   `json:"body,omitempty"`
	File   *FileMeta `json:"file,omitempty"`
	TS     int64    `json:"ts"`
	Read   bool     `json:"read"`
}

type FileMeta struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	ID    string `json:"id"`
	Local bool   `json:"local"`
}

type Peer struct {
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
	LastSeen int64  `json:"last_seen"`
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

	mu         sync.Mutex
	messages   []Message
	readSet    = map[string]bool{}
	deletedSet = map[string]bool{}
	seenIDs    = map[string]bool{}
	peers      = map[string]*Peer{}
	clients    = map[*websocket.Conn]bool{}

	fetchMu       sync.Mutex
	fetchProgress = map[string]*FileProgress{}
)

type FileProgress struct {
	FileID   string `json:"file_id"`
	Name     string `json:"name"`
	Total    int64  `json:"total"`
	Received int64  `json:"received"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
}

type FileSyncEntry struct {
	TS      int64  `json:"ts"`
	Source  string `json:"source,omitempty"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

var (
	syncLogMu    sync.Mutex
	fileSyncLogs = map[string][]FileSyncEntry{}
)

func addSyncLog(fileID string, entry FileSyncEntry) {
	syncLogMu.Lock()
	fileSyncLogs[fileID] = append(fileSyncLogs[fileID], entry)
	if len(fileSyncLogs[fileID]) > 50 {
		fileSyncLogs[fileID] = fileSyncLogs[fileID][len(fileSyncLogs[fileID])-50:]
	}
	syncLogMu.Unlock()
	wsEvent("file_log", map[string]interface{}{
		"file_id": fileID,
		"entry":   entry,
	})
}

type progressWriter struct {
	fileID string
	name   string
	total  int64
	n      int64
	last   int64
	dst    *os.File
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.dst.Write(p)
	pw.n += int64(n)
	if pw.n-pw.last > pw.total/20+1024 || err != nil {
		pw.last = pw.n
		fetchMu.Lock()
		if fp, ok := fetchProgress[pw.fileID]; ok {
			fp.Received = pw.n
		}
		fetchMu.Unlock()
		wsEvent("file_progress", map[string]interface{}{
			"file_id": pw.fileID, "name": pw.name,
			"total": pw.total, "received": pw.n,
		})
	}
	return n, err
}

func genID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%d-%s", time.Now().UnixMilli(), hex.EncodeToString(b))
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

// --- Persistence ---

func messagesPath() string { return filepath.Join(dataDir, "messages.jsonl") }
func statePath() string    { return filepath.Join(dataDir, "state.json") }
func filesDir() string     { return filepath.Join(dataDir, "files") }

func loadMessages() {
	data, err := os.ReadFile(messagesPath())
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		msg := migrateMessage([]byte(line))
		if msg.ID == "" {
			continue
		}
		messages = append(messages, msg)
		seenIDs[msg.ID] = true
	}
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	log.Printf("loaded %d messages", len(messages))
}

func migrateMessage(data []byte) Message {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return Message{}
	}
	var msg Message
	j := func(key string, dst *string) {
		if v, ok := raw[key]; ok {
			json.Unmarshal(v, dst)
		}
	}
	var i64 int64
	j("id", &msg.ID)
	j("from", &msg.From)
	j("from_ip", &msg.FromIP)
	j("type", &msg.Type)

	if v, ok := raw["body"]; ok {
		json.Unmarshal(v, &msg.Body)
	} else if v, ok := raw["text"]; ok {
		json.Unmarshal(v, &msg.Body)
	}

	if v, ok := raw["ts"]; ok {
		json.Unmarshal(v, &i64)
		msg.TS = i64
	} else if v, ok := raw["time"]; ok {
		json.Unmarshal(v, &i64)
		msg.TS = i64
	}

	if v, ok := raw["to"]; ok {
		var s string
		var arr []string
		if json.Unmarshal(v, &arr) == nil {
			msg.To = arr
		} else if json.Unmarshal(v, &s) == nil && s != "" {
			msg.To = []string{s}
		}
	}

	if v, ok := raw["file"]; ok {
		var fm FileMeta
		json.Unmarshal(v, &fm)
		if fm.ID != "" {
			msg.File = &fm
		}
	}

	if v, ok := raw["read"]; ok {
		json.Unmarshal(v, &msg.Read)
	}

	if msg.Type == "" {
		msg.Type = "text"
	}
	if msg.ID == "" {
		msg.ID = genID()
	}
	return msg
}

func saveMessage(msg Message) {
	os.MkdirAll(dataDir, 0755)
	f, err := os.OpenFile(messagesPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	data, _ := json.Marshal(msg)
	f.Write(append(data, '\n'))
}

type persistedState struct {
	Read    []string `json:"read"`
	Deleted []string `json:"deleted"`
}

func loadState() {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return
	}
	var s persistedState
	if json.Unmarshal(data, &s) != nil {
		return
	}
	for _, id := range s.Read {
		readSet[id] = true
	}
	for _, id := range s.Deleted {
		deletedSet[id] = true
	}
	for i := range messages {
		if readSet[messages[i].ID] {
			messages[i].Read = true
		}
	}
}

func saveState() {
	s := persistedState{}
	for id := range readSet {
		s.Read = append(s.Read, id)
	}
	for id := range deletedSet {
		s.Deleted = append(s.Deleted, id)
	}
	data, _ := json.Marshal(s)
	os.WriteFile(statePath(), data, 0644)
}

// --- WebSocket broadcast ---

func wsEvent(event string, payload interface{}) {
	data, _ := json.Marshal(map[string]interface{}{"event": event, "data": payload})
	dead := []*websocket.Conn{}
	for c := range clients {
		if err := c.WriteMessage(websocket.TextMessage, data); err != nil {
			dead = append(dead, c)
		}
	}
	for _, c := range dead {
		delete(clients, c)
		c.Close()
	}
}

func unreadCount() int {
	n := 0
	for _, m := range messages {
		if deletedSet[m.ID] || m.Type == "system" {
			continue
		}
		if m.From == hostname {
			continue
		}
		if !m.Read && !readSet[m.ID] {
			if len(m.To) == 0 || contains(m.To, hostname) {
				n++
			}
		}
	}
	return n
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// --- Multicast ---

func multicastSend(data []byte) {
	addr, _ := net.ResolveUDPAddr("udp4", cfg.MulticastAddr)
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.Write(data)
}

func multicastMsg(msg Message) {
	data, _ := json.Marshal(msg)
	multicastSend(data)
}

type syncBeacon struct {
	Type     string `json:"type"`
	From     string `json:"from"`
	FromIP   string `json:"from_ip"`
	Since    int64  `json:"since"`
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
		var raw map[string]json.RawMessage
		if json.Unmarshal(buf[:n], &raw) != nil {
			continue
		}
		var msgType string
		if v, ok := raw["type"]; ok {
			json.Unmarshal(v, &msgType)
		}

		switch msgType {
		case "presence":
			handlePresence(buf[:n])
		case "sync":
			handleSyncBeacon(buf[:n])
		case "text", "file":
			handleIncomingMessage(buf[:n])
		}
	}
}

func handlePresence(data []byte) {
	var p struct {
		From   string `json:"from"`
		FromIP string `json:"from_ip"`
	}
	if json.Unmarshal(data, &p) != nil || p.From == hostname {
		return
	}
	mu.Lock()
	_, existed := peers[p.From]
	peers[p.From] = &Peer{Hostname: p.From, IP: p.FromIP, LastSeen: time.Now().Unix()}
	mu.Unlock()
	if !existed {
		mu.Lock()
		wsEvent("peers", peerList())
		mu.Unlock()
		go resyncPendingFiles()
	}
}

func handleSyncBeacon(data []byte) {
	var sb syncBeacon
	if json.Unmarshal(data, &sb) != nil || sb.From == hostname || sb.FromIP == "" {
		return
	}
	mu.Lock()
	peers[sb.From] = &Peer{Hostname: sb.From, IP: sb.FromIP, LastSeen: time.Now().Unix()}
	var toSend []Message
	for _, m := range messages {
		if m.TS <= sb.Since || m.From == sb.From {
			continue
		}
		if len(m.To) == 0 || contains(m.To, sb.From) {
			toSend = append(toSend, m)
		}
	}
	mu.Unlock()
	if len(toSend) > 0 {
		go deliverMessages(sb.FromIP, toSend)
	}
}

func handleIncomingMessage(data []byte) {
	msg := migrateMessage(data)
	if msg.ID == "" || msg.From == hostname {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if seenIDs[msg.ID] {
		return
	}
	if len(msg.To) > 0 && !contains(msg.To, hostname) {
		return
	}
	msg.Read = false
	seenIDs[msg.ID] = true
	messages = append(messages, msg)
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	saveMessage(msg)
	wsEvent("message", msg)
	wsEvent("unread", map[string]int{"count": unreadCount()})
	log.Printf("rx %s from %s", msg.Type, msg.From)
	if msg.File != nil {
		go autoFetchFile(msg)
	}
}

// --- File replication ---

func autoFetchFile(msg Message) {
	if msg.File == nil || msg.File.ID == "" {
		return
	}
	localPath := filepath.Join(filesDir(), msg.File.ID)
	if _, err := os.Stat(localPath); err == nil {
		return
	}
	fetchMu.Lock()
	if _, already := fetchProgress[msg.File.ID]; already {
		fetchMu.Unlock()
		return
	}
	fp := &FileProgress{FileID: msg.File.ID, Name: msg.File.Name, Total: msg.File.Size}
	fetchProgress[msg.File.ID] = fp
	fetchMu.Unlock()
	wsEvent("file_progress", map[string]interface{}{
		"file_id": msg.File.ID, "name": msg.File.Name,
		"total": msg.File.Size, "received": 0,
	})

	ok := false
	sources := fileSources(msg)
	if len(sources) == 0 {
		addSyncLog(msg.File.ID, FileSyncEntry{
			TS: time.Now().Unix(), Success: false,
			Error: "no peers available on mesh",
		})
	}
	for _, ip := range sources {
		if fetchFileFrom(ip, msg.File.ID, msg.File.Name, localPath) {
			log.Printf("fetched file %s from %s", msg.File.ID, ip)
			addSyncLog(msg.File.ID, FileSyncEntry{
				TS: time.Now().Unix(), Source: ip, Success: true,
			})
			ok = true
			break
		}
		addSyncLog(msg.File.ID, FileSyncEntry{
			TS: time.Now().Unix(), Source: ip, Success: false,
			Error: "fetch failed",
		})
	}

	fetchMu.Lock()
	delete(fetchProgress, msg.File.ID)
	fetchMu.Unlock()

	if ok {
		wsEvent("file_ready", map[string]string{"file_id": msg.File.ID, "name": msg.File.Name})
	} else {
		reason := "all sources failed"
		if len(sources) == 0 {
			reason = "no peers available on mesh"
		}
		log.Printf("failed to fetch file %s: %s", msg.File.ID, reason)
		wsEvent("file_error", map[string]interface{}{
			"id": msg.ID, "file_id": msg.File.ID, "name": msg.File.Name,
			"reason": reason, "sources_tried": len(sources),
		})
	}
}

func fileSources(msg Message) []string {
	mu.Lock()
	defer mu.Unlock()
	var direct []string
	var other []string
	for _, p := range peers {
		if p.IP == msg.FromIP {
			continue
		}
		if hasFileCandidate(p, msg) {
			direct = append(direct, p.IP)
		}
	}
	if msg.FromIP != "" {
		other = append(other, msg.FromIP)
	}
	return append(direct, other...)
}

func hasFileCandidate(p *Peer, msg Message) bool {
	if len(msg.To) == 0 {
		return true
	}
	return contains(msg.To, p.Hostname)
}

func fetchFileFrom(peerIP, fileID, fileName, localPath string) bool {
	transport := &http.Transport{ResponseHeaderTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/files/%s", peerIP, cfg.Port, fileID))
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return false
	}
	defer resp.Body.Close()
	total := resp.ContentLength
	if total <= 0 {
		total = maxFileSize
	}
	os.MkdirAll(filesDir(), 0755)
	f, err := os.Create(localPath)
	if err != nil {
		return false
	}
	pw := &progressWriter{fileID: fileID, name: fileName, total: total, dst: f}
	_, err = io.Copy(pw, io.LimitReader(resp.Body, maxFileSize))
	f.Close()
	return err == nil
}

// --- Peer delivery ---

func deliverMessages(peerIP string, msgs []Message) {
	client := &http.Client{Timeout: deliveryTimeout}
	for _, m := range msgs {
		data, _ := json.Marshal(m)
		resp, err := client.Post(
			fmt.Sprintf("http://%s:%d/deliver", peerIP, cfg.Port),
			"application/json",
			strings.NewReader(string(data)),
		)
		if err != nil {
			log.Printf("deliver to %s failed: %v", peerIP, err)
			continue
		}
		resp.Body.Close()
		time.Sleep(10 * time.Millisecond)
	}
}

func peerList() []Peer {
	list := []Peer{{Hostname: hostname, IP: meshIP, LastSeen: time.Now().Unix()}}
	for _, p := range peers {
		list = append(list, *p)
	}
	return list
}

// --- File resync ---

func pendingFiles() []Message {
	mu.Lock()
	defer mu.Unlock()
	var pending []Message
	for _, m := range messages {
		if m.File == nil || m.File.ID == "" || deletedSet[m.ID] {
			continue
		}
		localPath := filepath.Join(filesDir(), m.File.ID)
		if _, err := os.Stat(localPath); err == nil {
			continue
		}
		fetchMu.Lock()
		_, fetching := fetchProgress[m.File.ID]
		fetchMu.Unlock()
		if fetching {
			continue
		}
		pending = append(pending, m)
	}
	return pending
}

func resyncPendingFiles() {
	pending := pendingFiles()
	if len(pending) == 0 {
		return
	}
	log.Printf("resync: %d unsynced files, retrying", len(pending))
	for _, m := range pending {
		go autoFetchFile(m)
		time.Sleep(500 * time.Millisecond)
	}
}

// --- Presence loop ---

func presenceLoop() {
	time.Sleep(2 * time.Second)
	sendPresence()
	sendSync()

	presenceTick := time.NewTicker(presenceInterval)
	resyncTick := time.NewTicker(60 * time.Second)
	for {
		select {
		case <-presenceTick.C:
			sendPresence()
			mu.Lock()
			now := time.Now().Unix()
			changed := false
			for h, p := range peers {
				if now-p.LastSeen > int64(peerTimeout.Seconds()) {
					delete(peers, h)
					changed = true
				}
			}
			if changed {
				wsEvent("peers", peerList())
			}
			mu.Unlock()
		case <-resyncTick.C:
			resyncPendingFiles()
		}
	}
}

func sendPresence() {
	data, _ := json.Marshal(map[string]string{
		"type": "presence", "from": hostname, "from_ip": meshIP,
	})
	multicastSend(data)
}

func sendSync() {
	mu.Lock()
	var since int64
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].TS > 0 {
			since = messages[i].TS - 3600
			break
		}
	}
	mu.Unlock()
	data, _ := json.Marshal(syncBeacon{
		Type: "sync", From: hostname, FromIP: meshIP, Since: since,
	})
	multicastSend(data)
}

// --- HTTP Handlers ---

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	mu.Lock()
	clients[conn] = true
	init := map[string]interface{}{
		"hostname": hostname,
		"peers":    peerList(),
		"unread":   unreadCount(),
	}
	conn.WriteJSON(map[string]interface{}{"event": "init", "data": init})
	mu.Unlock()

	defer func() {
		mu.Lock()
		delete(clients, conn)
		mu.Unlock()
		conn.Close()
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func handleGetMessages(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	var out []Message
	for _, m := range messages {
		if deletedSet[m.ID] {
			continue
		}
		if m.Type != "text" && m.Type != "file" {
			continue
		}
		if readSet[m.ID] {
			m.Read = true
		}
		if m.File != nil && m.File.ID != "" {
			_, err := os.Stat(filepath.Join(filesDir(), m.File.ID))
			m.File.Local = err == nil
		}
		if len(m.To) == 0 || contains(m.To, hostname) || m.From == hostname {
			out = append(out, m)
		}
	}
	mu.Unlock()
	if out == nil {
		out = []Message{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func handleMessageAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/messages/"), "/")
	if len(parts) < 1 {
		http.Error(w, "not found", 404)
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	mu.Lock()
	defer mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	found := false
	for _, m := range messages {
		if m.ID == id && !deletedSet[id] {
			found = true
			break
		}
	}
	if !found {
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "message not found"})
		return
	}

	if r.Method == "DELETE" || (r.Method == "POST" && action == "delete") {
		deletedSet[id] = true
		saveState()
		wsEvent("deleted", map[string]string{"id": id})
		wsEvent("unread", map[string]int{"count": unreadCount()})
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
		return
	}

	if r.Method == "POST" && action == "read" {
		readSet[id] = true
		for i := range messages {
			if messages[i].ID == id {
				messages[i].Read = true
				break
			}
		}
		saveState()
		wsEvent("read", map[string]string{"id": id})
		wsEvent("unread", map[string]int{"count": unreadCount()})
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
		return
	}

	http.Error(w, "not found", 404)
}

func handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var req struct {
		To   []string `json:"to"`
		Body string   `json:"body"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || strings.TrimSpace(req.Body) == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "empty message"})
		return
	}
	if len(req.Body) > 4000 {
		req.Body = req.Body[:4000]
	}

	msg := Message{
		ID: genID(), From: hostname, FromIP: meshIP,
		To: req.To, Type: "text", Body: req.Body,
		TS: time.Now().Unix(), Read: true,
	}

	mu.Lock()
	seenIDs[msg.ID] = true
	messages = append(messages, msg)
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	saveMessage(msg)
	wsEvent("message", msg)
	mu.Unlock()

	multicastMsg(msg)
	go tryDeliver(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "id": msg.ID})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.ParseMultipartForm(maxFileSize)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "no file"})
		return
	}
	defer file.Close()
	if hdr.Size > maxFileSize {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "max 100MB"})
		return
	}

	fileID := genID() + filepath.Ext(hdr.Filename)
	os.MkdirAll(filesDir(), 0755)
	dst, err := os.Create(filepath.Join(filesDir(), fileID))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "storage error"})
		return
	}
	io.Copy(dst, file)
	dst.Close()

	var to []string
	if vals, ok := r.MultipartForm.Value["to"]; ok {
		for _, v := range vals {
			if v != "" {
				to = append(to, v)
			}
		}
	}

	msg := Message{
		ID: genID(), From: hostname, FromIP: meshIP,
		To: to, Type: "file",
		File: &FileMeta{Name: hdr.Filename, Size: hdr.Size, ID: fileID},
		TS: time.Now().Unix(), Read: true,
	}

	mu.Lock()
	seenIDs[msg.ID] = true
	messages = append(messages, msg)
	saveMessage(msg)
	wsEvent("message", msg)
	mu.Unlock()

	multicastMsg(msg)
	go tryDeliver(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "id": msg.ID})
}

func handleFiles(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/files/")
	if path == "" || strings.Contains(path, "..") {
		http.Error(w, "not found", 404)
		return
	}
	parts := strings.SplitN(path, "/", 2)
	name := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if action == "fetch" && r.Method == "POST" {
		handleFileFetch(w, name)
		return
	}
	if action == "log" {
		handleFileLog(w, name)
		return
	}
	if action != "" {
		http.Error(w, "not found", 404)
		return
	}
	fetchMu.Lock()
	_, fetching := fetchProgress[name]
	fetchMu.Unlock()
	if fetching {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		json.NewEncoder(w).Encode(map[string]string{"error": "file is still downloading to this node"})
		return
	}
	localPath := filepath.Join(filesDir(), name)
	if _, err := os.Stat(localPath); err == nil {
		http.ServeFile(w, r, localPath)
		return
	}
	http.Error(w, "not found", 404)
}

func handleFileFetch(w http.ResponseWriter, fileID string) {
	fetchMu.Lock()
	_, fetching := fetchProgress[fileID]
	fetchMu.Unlock()
	if fetching {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "status": "already syncing"})
		return
	}
	localPath := filepath.Join(filesDir(), fileID)
	if _, err := os.Stat(localPath); err == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "status": "already local"})
		return
	}
	mu.Lock()
	var msg *Message
	for i := range messages {
		if messages[i].File != nil && messages[i].File.ID == fileID {
			m := messages[i]
			msg = &m
			break
		}
	}
	mu.Unlock()
	if msg == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "file not found in messages"})
		return
	}
	go autoFetchFile(*msg)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "status": "sync started"})
}

func handleFileLog(w http.ResponseWriter, fileID string) {
	syncLogMu.Lock()
	entries := fileSyncLogs[fileID]
	syncLogMu.Unlock()
	if entries == nil {
		entries = []FileSyncEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func handleUnread(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	n := unreadCount()
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"count": n})
}

func handleSyncReq(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	go sendSync()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func handleResync(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	pending := pendingFiles()
	go resyncPendingFiles()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "pending": len(pending)})
}

func handlePeers(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	list := peerList()
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	nc, nm, np := len(clients), len(messages), len(peers)+1
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok", "hostname": hostname, "clients": nc, "messages": nm, "peers": np,
	})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"multicast_addr": cfg.MulticastAddr, "interface": cfg.Iface, "port": cfg.Port,
	})
}

func handleDeliver(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMsgBytes))
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	msg := migrateMessage(body)
	if msg.ID == "" || msg.From == hostname {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
		return
	}

	mu.Lock()
	defer mu.Unlock()
	if seenIDs[msg.ID] {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "dup": true})
		return
	}
	msg.Read = false
	seenIDs[msg.ID] = true
	messages = append(messages, msg)
	if len(messages) > maxMessages {
		messages = messages[len(messages)-maxMessages:]
	}
	saveMessage(msg)
	wsEvent("message", msg)
	wsEvent("unread", map[string]int{"count": unreadCount()})
	log.Printf("delivered %s from %s", msg.Type, msg.From)
	go autoFetchFile(msg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func handleDeleteAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	mu.Lock()
	for _, m := range messages {
		deletedSet[m.ID] = true
	}
	messages = nil
	saveState()
	wsEvent("clear", nil)
	wsEvent("unread", map[string]int{"count": 0})
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func handleReadAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	mu.Lock()
	count := 0
	for i := range messages {
		if !messages[i].Read && messages[i].From != hostname && !deletedSet[messages[i].ID] {
			messages[i].Read = true
			readSet[messages[i].ID] = true
			count++
		}
	}
	if count > 0 {
		saveState()
	}
	wsEvent("unread", map[string]int{"count": 0})
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "marked": count})
}

// --- Delivery helper ---

func tryDeliver(msg Message) {
	mu.Lock()
	targets := make(map[string]string)
	if len(msg.To) == 0 {
		for _, p := range peers {
			targets[p.Hostname] = p.IP
		}
	} else {
		for _, h := range msg.To {
			if p, ok := peers[h]; ok {
				targets[h] = p.IP
			}
		}
	}
	mu.Unlock()

	for _, ip := range targets {
		deliverMessages(ip, []Message{msg})
	}
}

// --- Config ---

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

	loadMessages()
	loadState()

	go multicastListener()
	go presenceLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", handleWS)
	mux.HandleFunc("/messages", handleGetMessages)
	mux.HandleFunc("/messages/", handleMessageAction)
	mux.HandleFunc("/send", handleSend)
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/files/", handleFiles)
	mux.HandleFunc("/unread", handleUnread)
	mux.HandleFunc("/sync", handleSyncReq)
	mux.HandleFunc("/resync", handleResync)
	mux.HandleFunc("/peers", handlePeers)
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/config", handleConfig)
	mux.HandleFunc("/deliver", handleDeliver)
	mux.HandleFunc("/clear", handleDeleteAll)
	mux.HandleFunc("/read-all", handleReadAll)

	listen := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	log.Printf("mesh-chat v3: host=%s ip=%s listen=%s mcast=%s", hostname, meshIP, listen, cfg.MulticastAddr)
	log.Fatal(http.ListenAndServe(listen, mux))
}
