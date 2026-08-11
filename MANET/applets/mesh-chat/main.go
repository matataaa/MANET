package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/syslog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultMulticastAddr = "239.255.50.50:9800"
	maxHistory           = 200
	maxMsgBytes          = 4096
)

type Message struct {
	From string `json:"from"`
	Text string `json:"text"`
	Time int64  `json:"time"`
}

type Config struct {
	MulticastAddr string
	Iface         string
	Port          int
}

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	hostname string
	cfg      Config

	mu      sync.Mutex
	clients = map[*websocket.Conn]bool{}
	history []Message
)

func broadcast(msg Message) {
	mu.Lock()
	history = append(history, msg)
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
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
	mu.Unlock()
}

func multicastListener() {
	addr, err := net.ResolveUDPAddr("udp4", cfg.MulticastAddr)
	if err != nil {
		log.Fatalf("resolve multicast: %v", err)
	}

	var ifi *net.Interface
	if cfg.Iface != "" {
		ifi, err = net.InterfaceByName(cfg.Iface)
		if err != nil {
			log.Printf("interface %s not found, listening on all", cfg.Iface)
		}
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
			log.Printf("multicast read: %v", err)
			continue
		}
		var msg Message
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			continue
		}
		if msg.From == hostname {
			continue
		}
		log.Printf("rx from %s: %s", msg.From, msg.Text)
		broadcast(msg)
	}
}

func multicastSend(msg Message) error {
	addr, err := net.ResolveUDPAddr("udp4", cfg.MulticastAddr)
	if err != nil {
		return err
	}
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("ws upgrade: %v", err)
		return
	}
	log.Printf("client connected from %s", r.RemoteAddr)

	mu.Lock()
	clients[conn] = true
	for _, msg := range history {
		conn.WriteJSON(msg)
	}
	mu.Unlock()

	defer func() {
		mu.Lock()
		delete(clients, conn)
		mu.Unlock()
		conn.Close()
		log.Printf("client disconnected: %s", r.RemoteAddr)
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			break
		}
		text := strings.TrimSpace(string(raw))
		if text == "" || len(text) > 1000 {
			continue
		}
		msg := Message{
			From: hostname,
			Text: text,
			Time: time.Now().Unix(),
		}
		log.Printf("tx: %s", text)
		broadcast(msg)
		multicastSend(msg)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	nc := len(clients)
	nh := len(history)
	mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"hostname": hostname,
		"clients":  nc,
		"history":  nh,
	})
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"multicast_addr": cfg.MulticastAddr,
		"interface":      cfg.Iface,
		"port":           cfg.Port,
		"max_history":    maxHistory,
	})
}

func loadConfig(path string) Config {
	c := Config{
		MulticastAddr: defaultMulticastAddr,
		Iface:         "br0",
		Port:          9800,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
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
	iface := flag.String("iface", "br0", "network interface for multicast")
	configFile := flag.String("config", "/etc/mesh-chat.conf", "config file path")
	flag.Parse()

	cfg = loadConfig(*configFile)
	if *port != 9800 {
		cfg.Port = *port
	}
	if *iface != "br0" {
		cfg.Iface = *iface
	}

	syslogWriter, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "mesh-chat")
	if err == nil {
		log.SetOutput(syslogWriter)
	}

	hostname, err = os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	go multicastListener()

	http.HandleFunc("/ws", handleWS)
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/config", handleConfig)

	listen := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	log.Printf("starting: host=%s listen=%s multicast=%s iface=%s", hostname, listen, cfg.MulticastAddr, cfg.Iface)
	log.Fatal(http.ListenAndServe(listen, nil))
}
