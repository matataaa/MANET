package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	appletsDir    = "/usr/local/share/manet/applets"
	systemdDir    = "/etc/systemd/system"
	maxUploadSize = 50 << 20 // 50 MB
)

type appletDNSRecord struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

type appletManifest struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Type        string `json:"type"`
	Backend     struct {
		Binary  string   `json:"binary"`
		Port    int      `json:"port"`
		Args    []string `json:"args"`
		Service string   `json:"service"`
	} `json:"backend"`
	Frontend struct {
		Entrypoint string `json:"entrypoint"`
	} `json:"frontend"`
	Config struct {
		Page string `json:"page"`
		File string `json:"file"`
	} `json:"config"`
	DNS []appletDNSRecord `json:"dns,omitempty"`
}

func collectAppletDNS(myIP, myHostname string) []map[string]interface{} {
	var records []map[string]interface{}
	entries, err := os.ReadDir(appletsDir)
	if err != nil {
		return records
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := loadManifest(e.Name())
		if m == nil || len(m.DNS) == 0 {
			continue
		}
		svc := m.Backend.Service
		if svc == "" {
			svc = m.Name + ".service"
		}
		running := exec.Command("systemctl", "is-active", "--quiet", svc).Run() == nil
		for _, d := range m.DNS {
			records = append(records, map[string]interface{}{
				"name":   d.Name,
				"ip":     myIP,
				"type":   d.Scope,
				"source": m.Label,
				"stale":  !running,
			})
		}
	}
	return records
}

func loadManifest(name string) *appletManifest {
	data, err := os.ReadFile(filepath.Join(appletsDir, name, "applet.json"))
	if err != nil {
		return nil
	}
	var m appletManifest
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return &m
}

func (m *appletManifest) serviceName() string {
	if m.Backend.Service != "" {
		return m.Backend.Service
	}
	return "applet-" + m.Name + ".service"
}

func systemctl(action, unit string) (bool, string) {
	out, err := exec.Command("systemctl", action, unit).CombinedOutput()
	if err != nil {
		return false, strings.TrimSpace(string(out))
	}
	return true, ""
}

func unitProps(unit string) map[string]string {
	props := map[string]string{}
	out, err := exec.Command("systemctl", "show", unit,
		"--property=ActiveState,SubState,MainPID,LoadState,ActiveEnterTimestamp,UnitFileState",
	).Output()
	if err != nil {
		return props
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = v
		}
	}
	return props
}

func appletInfo(name string) map[string]interface{} {
	m := loadManifest(name)
	if m == nil {
		return nil
	}
	svc := m.serviceName()
	props := unitProps(svc)
	active := props["ActiveState"]

	status := active
	switch active {
	case "active":
		status = "running"
	case "inactive":
		status = "stopped"
	case "failed":
		status = "failed"
	}

	pid := 0
	if v, err := strconv.Atoi(props["MainPID"]); err == nil && v > 0 {
		pid = v
	}

	return map[string]interface{}{
		"name":         m.Name,
		"label":        m.Label,
		"version":      m.Version,
		"description":  m.Description,
		"author":       m.Author,
		"type":         m.Type,
		"status":       status,
		"enabled":      props["UnitFileState"] == "enabled" || props["UnitFileState"] == "enabled-runtime",
		"installed":    props["LoadState"] != "not-found",
		"has_backend":  m.Backend.Binary != "",
		"has_frontend": m.Frontend.Entrypoint != "",
		"has_config":   m.Config.Page != "",
		"service":      svc,
		"pid":          pid,
		"started_at":   props["ActiveEnterTimestamp"],
	}
}

func scanLocalApplets() []AppletBrief {
	entries, err := os.ReadDir(appletsDir)
	if err != nil {
		return nil
	}
	var out []AppletBrief
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := loadManifest(e.Name())
		if m == nil {
			continue
		}
		svc := m.serviceName()
		props := unitProps(svc)
		status := props["ActiveState"]
		switch status {
		case "active":
			status = "running"
		case "inactive":
			status = "stopped"
		case "failed":
			status = "failed"
		case "":
			status = "unknown"
		}
		label := m.Label
		if label == "" {
			label = m.Name
		}
		out = append(out, AppletBrief{Name: m.Name, Label: label, Status: status})
	}
	return out
}

// --- Handlers ---

func apiAppletsList(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(appletsDir)
	if err != nil {
		writeJSON(w, 200, map[string]interface{}{"applets": []interface{}{}})
		return
	}
	var applets []interface{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(appletsDir, e.Name(), "applet.json")); err != nil {
			continue
		}
		if info := appletInfo(e.Name()); info != nil {
			applets = append(applets, info)
		}
	}
	if applets == nil {
		applets = []interface{}{}
	}
	writeJSON(w, 200, map[string]interface{}{"applets": applets})
}

func apiAppletDetail(w http.ResponseWriter, r *http.Request, name string) {
	info := appletInfo(name)
	if info == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "applet not found"})
		return
	}
	writeJSON(w, 200, info)
}

func apiAppletAction(w http.ResponseWriter, r *http.Request, name string) {
	m := loadManifest(name)
	if m == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "applet not found"})
		return
	}
	body := readBody(r)
	action := jsonStr(body, "action", "")
	switch action {
	case "start", "stop", "restart", "enable", "disable":
	default:
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "invalid action"})
		return
	}
	ok, errMsg := systemctl(action, m.serviceName())
	writeJSON(w, 200, map[string]interface{}{"ok": ok, "error": errMsg})
}

func apiAppletLogs(w http.ResponseWriter, r *http.Request, name string) {
	m := loadManifest(name)
	if m == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "applet not found"})
		return
	}
	lines := r.URL.Query().Get("lines")
	if lines == "" {
		lines = "100"
	}
	n, err := strconv.Atoi(lines)
	if err != nil || n > 500 {
		n = 100
	}

	out, _ := exec.Command("journalctl", "-u", m.serviceName(),
		"-n", strconv.Itoa(n), "--no-pager", "-o", "short-iso").CombinedOutput()
	writeJSON(w, 200, map[string]interface{}{"logs": string(out), "unit": m.serviceName()})
}

func apiAppletConfigGet(w http.ResponseWriter, r *http.Request, name string) {
	m := loadManifest(name)
	if m == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "applet not found"})
		return
	}
	if m.Backend.Port == 0 {
		writeJSON(w, 502, map[string]interface{}{"error": "no backend port"})
		return
	}
	proxyToBackend(w, r, m.Backend.Port, "/config")
}

func apiAppletConfigPost(w http.ResponseWriter, r *http.Request, name string) {
	m := loadManifest(name)
	if m == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "applet not found"})
		return
	}
	if m.Config.File == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "no config file defined"})
		return
	}
	body := readBody(r)
	var lines []string
	for k, v := range body {
		lines = append(lines, fmt.Sprintf("%s=%v", k, v))
	}
	if err := os.WriteFile(m.Config.File, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true})
}

func apiAppletConfigPage(w http.ResponseWriter, r *http.Request, name string) {
	m := loadManifest(name)
	if m == nil || m.Config.Page == "" {
		http.NotFound(w, r)
		return
	}
	fp := filepath.Join(appletsDir, name, m.Config.Page)
	if !strings.HasPrefix(filepath.Clean(fp), filepath.Clean(filepath.Join(appletsDir, name))) {
		http.Error(w, "forbidden", 403)
		return
	}
	http.ServeFile(w, r, fp)
}

func apiAppletFrontend(w http.ResponseWriter, r *http.Request, name, subPath string) {
	m := loadManifest(name)
	if m == nil {
		http.NotFound(w, r)
		return
	}
	if subPath == "" || subPath == "/" {
		subPath = m.Frontend.Entrypoint
		if subPath == "" {
			subPath = "index.html"
		}
	}
	subPath = strings.TrimPrefix(subPath, "/")
	fp := filepath.Join(appletsDir, name, subPath)
	if !strings.HasPrefix(filepath.Clean(fp), filepath.Clean(filepath.Join(appletsDir, name))) {
		http.Error(w, "forbidden", 403)
		return
	}
	http.ServeFile(w, r, fp)
}

func apiAppletProxy(w http.ResponseWriter, r *http.Request, name, subPath string) {
	m := loadManifest(name)
	if m == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "applet not found"})
		return
	}
	if m.Backend.Port == 0 {
		writeJSON(w, 502, map[string]interface{}{"error": "no backend port"})
		return
	}
	if subPath == "" {
		subPath = "/"
	}
	if !strings.HasPrefix(subPath, "/") {
		subPath = "/" + subPath
	}

	// WebSocket upgrade — proxy directly
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		proxyWebSocket(w, r, m.Backend.Port, subPath)
		return
	}
	proxyToBackend(w, r, m.Backend.Port, subPath)
}

func proxyToBackend(w http.ResponseWriter, r *http.Request, port int, path string) {
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	r.URL.Path = path
	r.URL.RawQuery = ""
	r.Host = target.Host
	proxy.ServeHTTP(w, r)
}

func proxyWebSocket(w http.ResponseWriter, r *http.Request, port int, path string) {
	backendURL := fmt.Sprintf("ws://127.0.0.1:%d%s", port, path)
	backHeader := http.Header{}
	for _, proto := range r.Header["Sec-Websocket-Protocol"] {
		backHeader.Add("Sec-Websocket-Protocol", proto)
	}

	backConn, _, err := (&websocket.Dialer{}).Dial(backendURL, backHeader)
	if err != nil {
		writeJSON(w, 502, map[string]interface{}{"error": "backend ws connect: " + err.Error()})
		return
	}
	defer backConn.Close()

	frontConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer frontConn.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			mt, msg, err := backConn.ReadMessage()
			if err != nil {
				return
			}
			if frontConn.WriteMessage(mt, msg) != nil {
				return
			}
		}
	}()

	for {
		mt, msg, err := frontConn.ReadMessage()
		if err != nil {
			return
		}
		if backConn.WriteMessage(mt, msg) != nil {
			return
		}
	}
}

func apiAppletUninstall(w http.ResponseWriter, r *http.Request, name string) {
	m := loadManifest(name)
	if m == nil {
		writeJSON(w, 404, map[string]interface{}{"error": "applet not found"})
		return
	}
	svc := m.serviceName()
	systemctl("stop", svc)
	systemctl("disable", svc)
	os.Remove(filepath.Join(systemdDir, svc))
	os.RemoveAll(filepath.Join(appletsDir, name))
	exec.Command("systemctl", "daemon-reload").Run()
	go refreshMeshDNS()
	writeJSON(w, 200, map[string]interface{}{"ok": true, "removed": name})
}

func apiAppletInstall(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "read error: " + err.Error()})
		return
	}

	tmpDir, err := os.MkdirTemp("", "applet-install-*")
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	defer os.RemoveAll(tmpDir)

	tarPath := filepath.Join(tmpDir, "upload.tar.gz")
	if err := os.WriteFile(tarPath, data, 0644); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}

	extractDir := filepath.Join(tmpDir, "extract")
	os.MkdirAll(extractDir, 0755)
	if err := extractTarGz(tarPath, extractDir); err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "extract failed: " + err.Error()})
		return
	}

	manifestPath := filepath.Join(extractDir, "applet.json")
	if _, err := os.Stat(manifestPath); err != nil {
		entries, _ := os.ReadDir(extractDir)
		for _, e := range entries {
			if e.IsDir() {
				sub := filepath.Join(extractDir, e.Name(), "applet.json")
				if _, err := os.Stat(sub); err == nil {
					extractDir = filepath.Join(extractDir, e.Name())
					manifestPath = sub
					break
				}
			}
		}
	}

	mData, err := os.ReadFile(manifestPath)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "no applet.json in tarball"})
		return
	}

	var manifest appletManifest
	if json.Unmarshal(mData, &manifest) != nil || manifest.Name == "" {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "invalid applet.json"})
		return
	}
	if strings.Contains(manifest.Name, "/") || strings.Contains(manifest.Name, "..") {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "invalid applet name"})
		return
	}

	dest := filepath.Join(appletsDir, manifest.Name)
	os.MkdirAll(appletsDir, 0755)
	os.RemoveAll(dest)
	if err := copyDir(extractDir, dest); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "copy failed: " + err.Error()})
		return
	}

	svc := manifest.serviceName()
	for _, candidate := range []string{
		filepath.Join(dest, svc),
		filepath.Join(dest, manifest.Name+".service"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			copyFile(candidate, filepath.Join(systemdDir, svc))
			break
		}
	}

	if manifest.Config.File != "" {
		defaultConf := filepath.Join(dest, manifest.Name+".conf.default")
		if _, err := os.Stat(defaultConf); err == nil {
			if _, err := os.Stat(manifest.Config.File); err != nil {
				copyFile(defaultConf, manifest.Config.File)
			}
		}
	}

	if manifest.Backend.Binary != "" {
		binPath := filepath.Join(dest, manifest.Backend.Binary)
		os.Chmod(binPath, 0755)
	}

	exec.Command("systemctl", "daemon-reload").Run()
	systemctl("enable", svc)
	systemctl("start", svc)
	go refreshMeshDNS()

	writeJSON(w, 200, map[string]interface{}{
		"ok":        true,
		"installed": manifest.Name,
		"version":   manifest.Version,
	})
}

// --- Route dispatcher ---

func apiAppletsRouter(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/applets")
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		if r.Method == "GET" {
			apiAppletsList(w, r)
		} else {
			http.Error(w, "method not allowed", 405)
		}
		return
	}

	if path == "install" && r.Method == "POST" {
		apiAppletInstall(w, r)
		return
	}

	parts := strings.SplitN(path, "/", 3)
	name := parts[0]
	action := ""
	sub := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if len(parts) > 2 {
		sub = parts[2]
	}

	switch r.Method {
	case "GET":
		switch action {
		case "":
			apiAppletDetail(w, r, name)
		case "logs":
			apiAppletLogs(w, r, name)
		case "config":
			apiAppletConfigGet(w, r, name)
		case "config-page":
			apiAppletConfigPage(w, r, name)
		case "frontend":
			apiAppletFrontend(w, r, name, sub)
		case "proxy":
			apiAppletProxy(w, r, name, sub)
		default:
			http.NotFound(w, r)
		}
	case "POST":
		switch action {
		case "action":
			apiAppletAction(w, r, name)
		case "config":
			apiAppletConfigPost(w, r, name)
		case "proxy":
			apiAppletProxy(w, r, name, sub)
		default:
			http.NotFound(w, r)
		}
	case "DELETE":
		switch action {
		case "":
			apiAppletUninstall(w, r, name)
		case "proxy":
			apiAppletProxy(w, r, name, sub)
		default:
			http.NotFound(w, r)
		}
	default:
		http.Error(w, "method not allowed", 405)
	}
}

// --- Helpers ---

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
			continue
		}
		target := filepath.Join(dst, clean)
		if !strings.HasPrefix(target, filepath.Clean(dst)+string(os.PathSeparator)) && target != filepath.Clean(dst) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, 0755)
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	os.MkdirAll(filepath.Dir(dst), 0755)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	info, _ := os.Stat(src)
	if info != nil {
		os.Chmod(dst, info.Mode())
	}
	return nil
}

func refreshMeshDNS() {
	exec.Command("systemctl", "restart", "mesh-manager").Run()
}

func appletHostRedirect(next http.Handler) http.Handler {
	var mu sync.Mutex
	var dnsMap map[string]string
	var lastLoad time.Time

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.Split(r.Host, ":")[0]

		if strings.HasSuffix(host, ".mesh") && r.URL.Path == "/" {
			mu.Lock()
			if dnsMap == nil || time.Since(lastLoad) > 30*time.Second {
				dnsMap = appletDNSMap()
				lastLoad = time.Now()
			}
			applet := dnsMap[host]
			mu.Unlock()

			if applet != "" {
				http.Redirect(w, r, "/applets/"+applet+"/frontend/", http.StatusFound)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func appletDNSMap() map[string]string {
	m := make(map[string]string)
	entries, err := os.ReadDir(appletsDir)
	if err != nil {
		return m
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest := loadManifest(e.Name())
		if manifest == nil || len(manifest.DNS) == 0 {
			continue
		}
		for _, d := range manifest.DNS {
			m[d.Name] = e.Name()
		}
	}
	return m
}
