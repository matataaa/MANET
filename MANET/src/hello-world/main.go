package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

var startTime = time.Now()

func handleStatus(w http.ResponseWriter, r *http.Request) {
	hostname, _ := os.Hostname()
	writeJSON(w, 200, map[string]interface{}{
		"status":   "running",
		"hostname": hostname,
		"uptime":   time.Since(startTime).String(),
		"message":  "Hello from the mesh!",
	})
}

func handleHello(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]interface{}{
		"hello": "world",
		"mesh":  true,
	})
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func main() {
	port := flag.Int("port", 9820, "listen port")
	flag.Parse()

	http.HandleFunc("/status", handleStatus)
	http.HandleFunc("/hello", handleHello)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("hello-world listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
