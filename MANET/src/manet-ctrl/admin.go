package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

func makeConfigVersion(config map[string]string) string {
	keys := make([]string, 0, len(config))
	for k := range config {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf(`"%s":"%s"`, k, config[k])
	}
	data := "{" + strings.Join(parts, ",") + "}"
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)[:8]
}

func getPendingConfig() json.RawMessage {
	data, err := os.ReadFile(PendingConfFile)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

func savePendingConfig(pkg map[string]interface{}) error {
	data, err := json.Marshal(pkg)
	if err != nil {
		return err
	}
	return os.WriteFile(PendingConfFile, data, 0644)
}

func clearPendingConfig() {
	os.Remove(PendingConfFile)
}

func broadcastConfigPackage(pkg map[string]interface{}) bool {
	data, err := json.Marshal(pkg)
	if err != nil {
		return false
	}
	cmd := exec.Command("alfred", "-s", "70")
	cmd.Stdin = strings.NewReader(string(data))
	err = cmd.Run()
	return err == nil
}

func assembleAdminStatus() AdminStatus {
	conf := loadKVFile(MeshConfFile)
	registry := parseRegistry()
	pending := getPendingConfig()

	currentConfig := map[string]string{
		"node_hostname":     conf["node_hostname"],
		"eud":               confGet(conf, "eud", "wired"),
		"lan_ap_ssid":       conf["lan_ap_ssid"],
		"lan_ap_key":        conf["lan_ap_key"],
		"max_euds_per_node": confGet(conf, "max_euds_per_node", "0"),
		"mesh_ssid":         conf["mesh_ssid"],
		"mesh_key":          conf["mesh_key"],
		"ipv4_network":      confGet(conf, "ipv4_network", "10.30.2.0/24"),
		"regulatory_domain": confGet(conf, "regulatory_domain", "US"),
		"acs":               confGet(conf, "acs", "n"),
		"mtx":               confGet(conf, "mtx", "n"),
		"mumble":            confGet(conf, "mumble", "n"),
		"auto_update":       confGet(conf, "auto_update", "n"),
		"admin_password":    conf["admin_password"],
		"gateway":           confGet(conf, "gateway", "y"),
		"gateway_nat":       confGet(conf, "gateway_nat", "y"),
		"gateway_mss_clamp": confGet(conf, "gateway_mss_clamp", "y"),
		"gateway_bandwidth": confGet(conf, "gateway_bandwidth", ""),
		"halow_bw":          confGet(conf, "halow_bw", ""),
		"multicast_mode":    confGet(conf, "multicast_mode", "flood"),
		"lan_ap_channel":    conf["lan_ap_channel"],
		"lan_ap_bw":         confGet(conf, "lan_ap_bw", "20"),
	}

	if currentConfig["halow_bw"] == "" {
		info := getHalowDriverInfo("wlan2")
		if bw, ok := info["halow_bw"]; ok {
			currentConfig["halow_bw"] = bw
		} else {
			currentConfig["halow_bw"] = "8MHz"
		}
	}

	var adminNodes []AdminNode
	activeCount := 0
	for _, rn := range registry {
		state := rn["NODE_STATE"]
		if state == "ACTIVE" {
			activeCount++
		}
		adminNodes = append(adminNodes, AdminNode{
			Hostname:  rn["HOSTNAME"],
			IP:        rn["IPV4_ADDRESS"],
			Ack:       rn["CONFIG_ACK_VERSION"],
			LastSeen:  rn["LAST_SEEN_TIMESTAMP"],
			NodeState: state,
		})
	}

	return AdminStatus{
		CurrentConfig: currentConfig,
		Pending:       pending,
		Nodes:         adminNodes,
		TotalNodes:    len(registry),
		ActiveNodes:   activeCount,
		MyHostname:    getMyHostname(),
	}
}

func getMACsuffix() string {
	out, err := runCmdStdout(3*time.Second, "ip", "-br", "link", "show")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && (strings.HasPrefix(fields[0], "eth") || strings.HasPrefix(fields[0], "end")) {
			mac := fields[2]
			parts := strings.Split(mac, ":")
			if len(parts) >= 4 {
				return strings.Join(parts[len(parts)-4:], "")
			}
		}
	}
	return ""
}
