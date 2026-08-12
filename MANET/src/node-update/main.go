package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	confFile        = "/etc/mesh.conf"
	versionFile     = "/etc/manet_version.txt"
	tarballPath     = "/root/tools.tar.gz"
	defaultInterval = 6 * time.Hour
	cooldown        = 1 * time.Hour
	httpTimeout     = 30 * time.Second
	startupDelay    = 30 * time.Second
)

var (
	lastCheck time.Time
	mu        sync.Mutex
	client    = &http.Client{Timeout: httpTimeout}
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	log.SetPrefix("[node-update] ")
	log.Println("starting")

	board := boardType()
	if board == "unknown" {
		log.Fatal("unknown board type")
	}
	log.Printf("board: %s", board)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	timer := time.NewTimer(startupDelay)

	for {
		select {
		case <-timer.C:
			checkAndUpdate(board)
			timer.Reset(defaultInterval)
		case s := <-sig:
			if s == syscall.SIGHUP {
				mu.Lock()
				since := time.Since(lastCheck)
				mu.Unlock()
				if since < cooldown {
					log.Printf("SIGHUP: cooldown (%s since last check)", since.Round(time.Second))
					continue
				}
				log.Println("SIGHUP: checking for updates")
				checkAndUpdate(board)
				timer.Reset(defaultInterval)
			} else {
				log.Println("shutting down")
				return
			}
		}
	}
}

func checkAndUpdate(board string) {
	mu.Lock()
	lastCheck = time.Now()
	mu.Unlock()

	baseURL := confValue("update_url")
	if baseURL == "" {
		log.Println("OTA disabled (update_url not set in mesh.conf)")
		return
	}
	baseURL = strings.TrimRight(baseURL, "/")

	localVer := readLocalVersion()

	remoteVer, err := fetchText(baseURL + "/manet_version.txt")
	if err != nil {
		log.Printf("version check failed: %v", err)
		return
	}

	if localVer == remoteVer {
		log.Printf("up to date (v%s)", localVer)
		os.Chtimes(versionFile, time.Now(), time.Now())
		return
	}

	log.Printf("update: v%s -> v%s", localVer, remoteVer)

	tarballURL := fmt.Sprintf("%s/%s-tools.tar.gz", baseURL, board)
	if err := download(tarballURL, tarballPath); err != nil {
		log.Printf("download failed: %v", err)
		return
	}

	out, err := exec.Command("tar", "-zxf", tarballPath, "-C", "/").CombinedOutput()
	if err != nil {
		log.Printf("extract failed: %v: %s", err, strings.TrimSpace(string(out)))
		os.Remove(tarballPath)
		return
	}

	os.Remove(tarballPath)
	log.Printf("updated to v%s", remoteVer)
}

func boardType() string {
	data, err := os.ReadFile("/proc/device-tree/model")
	if err != nil {
		return "unknown"
	}
	model := strings.TrimRight(string(data), "\x00")
	switch {
	case strings.Contains(model, "ROCK3"):
		return "r3a"
	case strings.Contains(model, "Raspberry Pi 5"):
		return "rpi5"
	case strings.Contains(model, "Raspberry Pi 4"),
		strings.Contains(model, "Raspberry Pi Compute Module 4"):
		return "cm4"
	}
	return "unknown"
}

func readLocalVersion() string {
	data, err := os.ReadFile(versionFile)
	if err != nil {
		return "0"
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	if len(lines) == 0 || lines[0] == "" {
		return "0"
	}
	return lines[0]
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

	resp, err := client.Get(url)
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
