package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const qosConfPath = "/etc/mesh-qos.json"

type qosRule struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	DSCP string `json:"dscp,omitempty"`
	Band int    `json:"band"`
}

type qosConfig struct {
	Enabled bool      `json:"enabled"`
	Rules   []qosRule `json:"rules"`
}

type qosBandStats struct {
	Bytes   int64 `json:"bytes"`
	Packets int64 `json:"packets"`
	Dropped int64 `json:"dropped"`
}

type qosResponse struct {
	Enabled    bool                    `json:"enabled"`
	Rules      []qosRule               `json:"rules"`
	Interfaces []string                `json:"interfaces"`
	Stats      map[string][]qosBandStats `json:"stats"`
	BandNames  []string                `json:"band_names"`
}

var defaultQoSRules = []qosRule{
	{Name: "Voice", Port: 4370, DSCP: "0xb8", Band: 0},
	{Name: "CoT", Port: 6969, DSCP: "0x20", Band: 1},
	{Name: "Mesh Chat", Port: 9800, Band: 2},
}

func loadQoSConfig() qosConfig {
	data, err := os.ReadFile(qosConfPath)
	if err != nil {
		return qosConfig{Enabled: true, Rules: defaultQoSRules}
	}
	var cfg qosConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return qosConfig{Enabled: true, Rules: defaultQoSRules}
	}
	return cfg
}

func saveQoSConfig(cfg qosConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(qosConfPath, data, 0644)
}

var tcClassRe = regexp.MustCompile(`class prio 1:(\d+)`)
var tcStatsRe = regexp.MustCompile(`Sent (\d+) bytes (\d+) pkt \(dropped (\d+),`)

func readTcStats(iface string) []qosBandStats {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/tc", "-s", "class", "show", "dev", iface).Output()
	if err != nil {
		return nil
	}

	bands := make([]qosBandStats, 3)
	lines := strings.Split(string(out), "\n")
	currentBand := -1
	for _, line := range lines {
		if m := tcClassRe.FindStringSubmatch(line); m != nil {
			idx, _ := strconv.Atoi(m[1])
			currentBand = idx - 1
		}
		if currentBand >= 0 && currentBand < 3 {
			if m := tcStatsRe.FindStringSubmatch(line); m != nil {
				bands[currentBand].Bytes, _ = strconv.ParseInt(m[1], 10, 64)
				bands[currentBand].Packets, _ = strconv.ParseInt(m[2], 10, 64)
				bands[currentBand].Dropped, _ = strconv.ParseInt(m[3], 10, 64)
				currentBand = -1
			}
		}
	}
	return bands
}

func qosInterfaces() []string {
	ifaces := []string{"br0"}
	if data, err := os.ReadFile("/var/lib/mesh_if"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if f := strings.TrimSpace(line); f != "" {
				ifaces = append(ifaces, f)
			}
		}
	}
	return ifaces
}

func applyQoSRules(cfg qosConfig) {
	ifaces := qosInterfaces()
	for _, iface := range ifaces {
		exec.Command("/usr/sbin/tc", "qdisc", "del", "dev", iface, "root").Run()

		if !cfg.Enabled {
			continue
		}

		exec.Command("/usr/sbin/tc", "qdisc", "add", "dev", iface, "root", "handle", "1:", "prio",
			"bands", "3", "priomap",
			"1", "2", "2", "2", "1", "2", "0", "0",
			"1", "1", "1", "1", "1", "1", "1", "1").Run()

		// DSCP EF → band 0
		exec.Command("/usr/sbin/tc", "filter", "add", "dev", iface, "parent", "1:0",
			"protocol", "ip", "prio", "1", "u32",
			"match", "ip", "tos", "0xb8", "0xfc",
			"flowid", "1:1").Run()

		for i, rule := range cfg.Rules {
			flowid := "1:" + strconv.Itoa(rule.Band+1)
			prio := strconv.Itoa(i + 2)
			exec.Command("/usr/sbin/tc", "filter", "add", "dev", iface, "parent", "1:0",
				"protocol", "ip", "prio", prio, "u32",
				"match", "ip", "dport", strconv.Itoa(rule.Port), "0xffff",
				"flowid", flowid).Run()
		}
	}
}

func apiQoS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		cfg := loadQoSConfig()
		ifaces := qosInterfaces()
		stats := make(map[string][]qosBandStats)
		for _, iface := range ifaces {
			if s := readTcStats(iface); s != nil {
				stats[iface] = s
			}
		}
		writeJSON(w, 200, qosResponse{
			Enabled:    cfg.Enabled,
			Rules:      cfg.Rules,
			Interfaces: ifaces,
			Stats:      stats,
			BandNames:  []string{"High (Voice)", "Normal", "Low (Bulk)"},
		})

	case "POST":
		body := readBody(r)

		cfg := loadQoSConfig()

		if v, ok := body["enabled"]; ok {
			if b, ok := v.(bool); ok {
				cfg.Enabled = b
			}
		}

		if svc, ok := body["service"].(string); ok {
			if bandF, ok := body["band"].(float64); ok {
				band := int(bandF)
				if band >= 0 && band <= 2 {
					for i, rule := range cfg.Rules {
						if rule.Name == svc {
							cfg.Rules[i].Band = band
							break
						}
					}
				}
			}
		}

		if err := saveQoSConfig(cfg); err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		applyQoSRules(cfg)
		writeJSON(w, 200, map[string]bool{"ok": true})

	default:
		http.Error(w, "method not allowed", 405)
	}
}
