package main

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var Version = "dev"

var upgrader = websocket.Upgrader{
	ReadBufferSize:    16384,
	WriteBufferSize:   16384,
	CheckOrigin:       func(r *http.Request) bool { return true },
	EnableCompression: true,
}

var peerTLSConfig *tls.Config

func handleTerminal(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	defer conn.Close()

	q := r.URL.Query()
	target := q.Get("target")
	protocol := q.Get("protocol")

	if target != "" && protocol != "ssh" {
		handleTerminalProxy(conn, target)
		return
	}

	if target != "" && protocol == "ssh" {
		handleTerminalSSH(conn, q)
		return
	}

	handleTerminalLocal(conn)
}

func handleTerminalProxy(client *websocket.Conn, target string) {
	remoteURL := fmt.Sprintf("wss://%s/ws/terminal", target)
	dialer := websocket.Dialer{
		ReadBufferSize:    16384,
		WriteBufferSize:   16384,
		TLSClientConfig:   peerTLSConfig,
		EnableCompression: true,
	}
	remote, _, err := dialer.Dial(remoteURL, nil)
	if err != nil {
		log.Printf("terminal proxy dial %s: %v", target, err)
		client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\x1b[31mFailed to connect to %s: %v\x1b[0m\r\n", target, err)))
		return
	}
	defer remote.Close()
	log.Printf("terminal proxy connected to %s", target)

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			msgType, msg, err := remote.ReadMessage()
			if err != nil {
				return
			}
			if err := client.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	for {
		msgType, msg, err := client.ReadMessage()
		if err != nil {
			break
		}
		if err := remote.WriteMessage(msgType, msg); err != nil {
			break
		}
	}
	<-done
}

func handleTerminalSSH(conn *websocket.Conn, q url.Values) {
	target := q.Get("target")
	user := q.Get("user")
	if user == "" {
		user = "root"
	}
	password := q.Get("password")
	if password == "" {
		conf := loadKVFile(MeshConfFile)
		password = conf["admin_password"]
	}

	sshArgs := []string{"-tt",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=5",
		fmt.Sprintf("%s@%s", user, target),
	}
	var cmd *exec.Cmd
	if password != "" {
		cmd = exec.Command("sshpass", append([]string{"-p", password, "ssh"}, sshArgs...)...)
	} else {
		cmd = exec.Command("ssh", sshArgs...)
	}

	handleTerminalPTY(conn, cmd, target)
}

func handleTerminalLocal(conn *websocket.Conn) {
	cmd := exec.Command("bash", "-l")
	handleTerminalPTY(conn, cmd, "")
}

func handleTerminalPTY(conn *websocket.Conn, cmd *exec.Cmd, target string) {
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=en_US.UTF-8")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		log.Printf("pty: %v", err)
		return
	}

	pid := cmd.Process.Pid
	log.Printf("terminal pid=%d target=%q", pid, target)

	var wmu sync.Mutex
	dataCh := make(chan []byte, 32)

	go func() {
		buf := make([]byte, 16384)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				close(dataCh)
				return
			}
			cp := make([]byte, n)
			copy(cp, buf[:n])
			dataCh <- cp
		}
	}()

	go func() {
		var batch []byte
		timer := time.NewTimer(10 * time.Millisecond)
		if !timer.Stop() {
			<-timer.C
		}

		flush := func() {
			if len(batch) == 0 {
				return
			}
			wmu.Lock()
			conn.WriteMessage(websocket.BinaryMessage, batch)
			wmu.Unlock()
			batch = nil
		}

		for {
			select {
			case data, ok := <-dataCh:
				if !ok {
					flush()
					conn.Close()
					return
				}
				batch = append(batch, data...)
				if len(batch) >= 4096 {
					timer.Stop()
					flush()
				} else {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(10 * time.Millisecond)
				}
			case <-timer.C:
				flush()
			}
		}
	}()

	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		switch msgType {
		case websocket.TextMessage:
			ptmx.Write(msg)
		case websocket.BinaryMessage:
			if len(msg) >= 5 && msg[0] == 1 {
				cols := binary.BigEndian.Uint16(msg[1:3])
				rows := binary.BigEndian.Uint16(msg[3:5])
				pty.Setsize(ptmx, &pty.Winsize{Cols: cols, Rows: rows})
			} else if len(msg) == 9 && msg[0] == 2 {
				pong := make([]byte, 9)
				copy(pong, msg)
				pong[0] = 3
				wmu.Lock()
				conn.WriteMessage(websocket.BinaryMessage, pong)
				wmu.Unlock()
			}
		}
	}

	ptmx.Close()
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	log.Printf("terminal ended pid=%d", pid)
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	defer conn.Close()

	q := r.URL.Query()
	target := q.Get("target")
	unit := q.Get("unit")
	file := q.Get("file")
	lines := q.Get("lines")
	if lines == "" {
		lines = "200"
	}

	if target != "" {
		handleLogsProxy(conn, target, q)
		return
	}

	var cmd *exec.Cmd
	if file != "" {
		cmd = exec.Command("tail", "-f", "-n", lines, file)
	} else if unit != "" {
		cmd = exec.Command("journalctl", "-u", unit, "-f", "-n", lines, "--no-pager", "-o", "short-iso")
	} else {
		cmd = exec.Command("journalctl", "-f", "-n", lines, "--no-pager", "-o", "short-iso")
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("pipe: %v", err)
		return
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		log.Printf("start: %v", err)
		return
	}
	log.Printf("logs pid=%d unit=%q file=%q", cmd.Process.Pid, unit, file)

	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 64*1024)
		for scanner.Scan() {
			line := scanner.Text() + "\r\n"
			if err := conn.WriteMessage(websocket.TextMessage, []byte(line)); err != nil {
				return
			}
		}
		conn.Close()
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}

	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	log.Printf("logs ended pid=%d", cmd.Process.Pid)
}

func handleLogsProxy(client *websocket.Conn, target string, q url.Values) {
	params := url.Values{}
	if u := q.Get("unit"); u != "" {
		params.Set("unit", u)
	}
	if f := q.Get("file"); f != "" {
		params.Set("file", f)
	}
	if l := q.Get("lines"); l != "" {
		params.Set("lines", l)
	}
	remoteURL := fmt.Sprintf("wss://%s/ws/logs?%s", target, params.Encode())

	dialer := websocket.Dialer{
		ReadBufferSize:    16384,
		WriteBufferSize:   16384,
		TLSClientConfig:   peerTLSConfig,
		HandshakeTimeout:  5 * time.Second,
		EnableCompression: true,
	}
	remote, _, err := dialer.Dial(remoteURL, nil)
	if err != nil {
		log.Printf("logs proxy dial %s: %v", target, err)
		client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\x1b[31mFailed to connect to %s: %v\x1b[0m\r\n", target, err)))
		return
	}
	defer remote.Close()
	log.Printf("logs proxy connected to %s", target)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			msgType, msg, err := remote.ReadMessage()
			if err != nil {
				client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("\r\n\x1b[31mRemote %s disconnected\x1b[0m\r\n", target)))
				return
			}
			if err := client.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	for {
		_, _, err := client.ReadMessage()
		if err != nil {
			break
		}
	}
	<-done
}

var mimeTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "application/javascript; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".ico":  "image/x-icon",
	".woff": "font/woff",
	".woff2": "font/woff2",
	".ttf":  "font/ttf",
}

func serveStatic(webRoot string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		clean := filepath.Clean(path)
		if strings.Contains(clean, "..") {
			http.Error(w, "forbidden", 403)
			return
		}

		filePath := filepath.Join(webRoot, clean)
		info, err := os.Stat(filePath)
		if err != nil || info.IsDir() {
			// SPA fallback
			filePath = filepath.Join(webRoot, "index.html")
			info, err = os.Stat(filePath)
			if err != nil {
				http.NotFound(w, r)
				return
			}
		}

		ext := filepath.Ext(filePath)
		if ct, ok := mimeTypes[ext]; ok {
			w.Header().Set("Content-Type", ct)
		}

		switch ext {
		case ".js", ".css":
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		case ".woff", ".woff2", ".ttf", ".png", ".jpg", ".ico", ".svg":
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}

		http.ServeFile(w, r, filePath)
	}
}

func main() {
	port := flag.String("port", "80", "listen port")
	tlsPort := flag.String("tls-port", "443", "HTTPS listen port")
	tlsCert := flag.String("tls-cert", "/etc/manet/tls/cert.pem", "TLS certificate file")
	tlsKey := flag.String("tls-key", "/etc/manet/tls/key.pem", "TLS key file")
	webRoot := flag.String("webroot", "/usr/local/share/manet/www", "static files directory")
	tlsSkipVerify := flag.Bool("tls-skip-verify", true, "skip TLS verification for peer connections")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(Version)
		return
	}

	log.Printf("starting (version %s)", Version)

	peerTLSConfig = &tls.Config{InsecureSkipVerify: *tlsSkipVerify}

	mux := http.NewServeMux()

	// WebSocket
	mux.HandleFunc("/ws/terminal", requireAuth(handleTerminal))
	mux.HandleFunc("/ws/logs", handleLogs)
	mux.HandleFunc("/ws/voice", handleVoiceWS)

	// Status APIs (read-only — no auth)
	mux.HandleFunc("/api/data", apiData)
	mux.HandleFunc("/api/local", apiLocal)
	mux.HandleFunc("/api/peer/", apiPeer)
	mux.HandleFunc("/api/voice", apiVoice)
	mux.HandleFunc("/api/voice/channels", apiVoiceChannels)
	mux.HandleFunc("/api/admin/status", apiAdminStatus)
	mux.HandleFunc("/api/admin/update-status", apiUpdateStatus)
	mux.HandleFunc("/api/admin/update-summary", apiUpdateSummary)
	mux.HandleFunc("/api/daemons", apiDaemons)
	mux.HandleFunc("/api/atak-package", apiATAKPackage)
	mux.HandleFunc("/api/mesh-ctrl.apk", apiDownloadAPK)
	mux.HandleFunc("/api/services", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			if !checkAuth(w, r) {
				return
			}
			apiServiceAction(w, r)
		} else {
			apiServices(w, r)
		}
	})
	mux.HandleFunc("/api/services/", requireAuth(apiServiceAction))
	mux.HandleFunc("/api/mesh", apiMesh)
	mux.HandleFunc("/api/qos", apiQoS)
	mux.HandleFunc("/api/registry", apiRegistry)

	// Control APIs (all destructive — require auth)
	mux.HandleFunc("/api/control/interface", requireAuth(apiControlInterface))
	mux.HandleFunc("/api/control/txpower", requireAuth(apiControlTxPower))
	mux.HandleFunc("/api/control/halow_channel", requireAuth(apiControlHalowChannel))
	mux.HandleFunc("/api/control/wifi_channel", requireAuth(apiControlWifiChannel))
	mux.HandleFunc("/api/control/hostname", requireAuth(apiControlHostname))

	// Admin APIs (all destructive — require auth)
	mux.HandleFunc("/api/admin/save", requireAuth(apiAdminSave))
	mux.HandleFunc("/api/admin/stage", requireAuth(apiAdminStage))
	mux.HandleFunc("/api/admin/activate", requireAuth(apiAdminActivate))
	mux.HandleFunc("/api/admin/cancel", requireAuth(apiAdminCancel))
	mux.HandleFunc("/api/admin/delete-node", requireAuth(apiAdminDeleteNode))
	mux.HandleFunc("/api/admin/preferences", requireAuth(apiFleetPreferences))
	mux.HandleFunc("/api/admin/update-now", requireAuth(apiUpdateNow))
	mux.HandleFunc("/api/admin/force-update", requireAuth(apiForceUpdate))

	// Perf APIs (run tests on mesh — require auth)
	mux.HandleFunc("/api/iperf/server/start", requireAuth(apiIperfServerStart))
	mux.HandleFunc("/api/iperf/server/stop", requireAuth(apiIperfServerStop))
	mux.HandleFunc("/api/iperf/client/run", requireAuth(apiIperfClientRun))
	mux.HandleFunc("/api/iperf/client/stream", requireAuth(apiIperfClientStream))
	mux.HandleFunc("/api/iperf/stop", requireAuth(apiIperfStop))
	mux.HandleFunc("/api/ping/run", requireAuth(apiPingRun))
	mux.HandleFunc("/api/ping/stream", requireAuth(apiPingStream))
	mux.HandleFunc("/api/ping/stop", requireAuth(apiPingStop))
	mux.HandleFunc("/api/traceroute/stream", requireAuth(apiTracerouteStream))
	mux.HandleFunc("/api/traceroute/stop", requireAuth(apiTracerouteStop))

	// Terminal HTTP fallback (all destructive — require auth)
	mux.HandleFunc("/api/terminal/exec", requireAuth(apiTerminalExec))
	mux.HandleFunc("/api/terminal/complete", requireAuth(apiTerminalComplete))
	mux.HandleFunc("/api/terminal/reboot", requireAuth(apiTerminalReboot))

	// Version
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]interface{}{"version": Version})
	})

	// Auth
	mux.HandleFunc("/api/auth/status", apiAuthStatus)
	mux.HandleFunc("/api/perf-auth", apiPerfAuth)

	// Applets
	mux.HandleFunc("/api/applets", apiAppletsRouter)
	mux.HandleFunc("/api/applets/", apiAppletsRouter)

	// Static files (SPA fallback)
	mux.HandleFunc("/", serveStatic(*webRoot))

	voiceInitChannels()
	go fleetConfigWatcher()
	go fleetMcastListener()
	go airtimeLoop()

	handler := appletHostRedirect(mux, *webRoot)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down")
		os.Exit(0)
	}()

	if err := ensureTLSCert(*tlsCert, *tlsKey); err != nil {
		log.Printf("TLS cert generation failed: %v — HTTPS disabled", err)
		log.Printf("manet-ctrl listening on :%s webroot=%s", *port, *webRoot)
		log.Fatal(http.ListenAndServe(":"+*port, handler))
	} else {
		go func() {
			tlsSrv := &http.Server{
				Addr:    ":" + *tlsPort,
				Handler: handler,
				TLSConfig: &tls.Config{
					MinVersion: tls.VersionTLS12,
				},
			}
			log.Printf("manet-ctrl HTTPS on :%s", *tlsPort)
			if err := tlsSrv.ListenAndServeTLS(*tlsCert, *tlsKey); err != nil {
				log.Printf("HTTPS listener failed: %v", err)
			}
		}()

		redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			target := "https://" + r.Host + r.URL.RequestURI()
			if *tlsPort != "443" {
				host := r.Host
				if h, _, found := strings.Cut(host, ":"); found {
					host = h
				}
				target = "https://" + host + ":" + *tlsPort + r.URL.RequestURI()
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		})
		log.Printf("manet-ctrl HTTP :%s → HTTPS :%s", *port, *tlsPort)
		log.Fatal(http.ListenAndServe(":"+*port, redirect))
	}
}
