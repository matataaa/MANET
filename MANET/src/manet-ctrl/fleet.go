package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	fleetMcastAddr = "239.30.2.70:17070"
	fleetMcastIface = "br0"
)

func fleetConfigWatcher() {
	for {
		time.Sleep(10 * time.Second)
		fleetPollAlfred()
		fleetCheckActivation()
	}
}

func fleetCheckActivation() {
	raw := getPendingConfig()
	if raw == nil {
		return
	}
	var pkg map[string]interface{}
	if json.Unmarshal(raw, &pkg) != nil {
		return
	}
	at, ok := pkg["activate_at"].(float64)
	if !ok || at == 0 {
		return
	}
	if time.Now().Unix() < int64(at) {
		return
	}
	log.Printf("fleet: activation time reached, applying config")
	fleetApplyConfig(pkg)
	clearPendingConfig()
	log.Printf("fleet: config applied and pending cleared")
}

func fleetApplyConfig(pkg map[string]interface{}) {
	configRaw, ok := pkg["config"].(map[string]interface{})
	if !ok {
		return
	}
	updates := make(map[string]string)
	for k, v := range configRaw {
		if saveableKeys[k] {
			updates[k] = fmt.Sprintf("%v", v)
		}
	}
	if len(updates) == 0 {
		return
	}
	if err := saveKVFile(MeshConfFile, updates); err != nil {
		log.Printf("fleet: apply save error: %v", err)
		return
	}

	conf := loadKVFile(MeshConfFile)

	if updates["node_hostname"] != "" || updates["mesh_ssid"] != "" {
		prefix := confGet(conf, "node_hostname", "node")
		meshSSID := conf["mesh_ssid"]
		macSuffix := getMACsuffix()
		full := prefix
		if meshSSID != "" {
			full += "-" + meshSSID
		}
		if macSuffix != "" {
			full += "-" + macSuffix
		}
		setHostname(full)
	}
	if updates["gateway"] != "" || updates["gateway_nat"] != "" || updates["gateway_mss_clamp"] != "" || updates["gateway_bandwidth"] != "" {
		runCmd(5*time.Second, "systemctl", "reload", "gateway-manager")
	}
	if updates["lan_ap_ssid"] != "" || updates["lan_ap_key"] != "" {
		applyHostapdConfig(conf)
		runCmd(10*time.Second, "systemctl", "restart", "hostapd")
	}
	if updates["mesh_ssid"] != "" || updates["mesh_key"] != "" {
		applyWPAConfig(conf)
	}
	if updates["multicast_mode"] != "" {
		applyMulticastMode(updates["multicast_mode"])
	}
	if updates["voice_mic_volume"] != "" || updates["voice_speaker_volume"] != "" {
		applyVoiceVolume(conf)
	}
	if updates["voice_ptt_mode"] != "" || updates["voice_channel"] != "" {
		txCh := int(voiceTxCh.Load())
		if txCh <= 0 {
			txCh = 1
		}
		voiceRestartDaemon(txCh)
	}
	if updates["dns_servers"] != "" {
		applyDNSServers(updates["dns_servers"])
	}
	if updates["lan_ap_channel"] != "" || updates["lan_ap_bw"] != "" {
		runCmd(10*time.Second, "systemctl", "restart", "hostapd")
	}
	if updates["qos_enabled"] != "" || updates["qos_voice_band"] != "" || updates["qos_cot_band"] != "" || updates["qos_chat_band"] != "" {
		applyQoSFromConf(conf)
	}
}

func fleetPollAlfred() {
	out, err := exec.Command("/usr/sbin/alfred", "-r", "70").Output()
	if err != nil || len(out) == 0 {
		return
	}

	// Alfred output: { "mac", "json_payload" },
	// Find the JSON payload between the second quote pair
	s := strings.TrimSpace(string(out))
	idx := strings.Index(s, "\", \"")
	if idx < 0 {
		return
	}
	rest := s[idx+4:]
	end := strings.LastIndex(rest, "\"")
	if end < 0 {
		return
	}
	payload := rest[:end]

	// Unescape JSON string escaping
	var raw string
	if json.Unmarshal([]byte("\""+payload+"\""), &raw) != nil {
		raw = payload
	}

	fleetProcessPackage([]byte(raw))
}

func fleetProcessPackage(data []byte) {
	var pkg map[string]interface{}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	version, _ := pkg["version"].(string)
	if version == "" {
		return
	}

	// Check if we already have this version ACKed
	existing, _ := os.ReadFile(AckVersionFile)
	if strings.TrimSpace(string(existing)) == version {
		return
	}

	// Save as pending config
	savePendingConfig(pkg)

	// Write ACK
	os.WriteFile(AckVersionFile, []byte(version), 0644)
	log.Printf("fleet: ACKed config version %s", version)

	// Broadcast ACK via multicast for fast propagation
	fleetMcastSendAck(version)
}

func fleetMcastSendAck(version string) {
	addr, err := net.ResolveUDPAddr("udp4", fleetMcastAddr)
	if err != nil {
		return
	}
	iface, _ := net.InterfaceByName(fleetMcastIface)
	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = iface // used for receive side

	hostname, _ := os.Hostname()
	mac := getMyMAC()
	msg := map[string]string{
		"type":     "fleet_ack",
		"version":  version,
		"hostname": hostname,
		"mac":      mac,
	}
	data, _ := json.Marshal(msg)
	conn.Write(data)
}

func fleetMcastListener() {
	addr, err := net.ResolveUDPAddr("udp4", fleetMcastAddr)
	if err != nil {
		log.Printf("fleet mcast: resolve error: %v", err)
		return
	}
	iface, err := net.InterfaceByName(fleetMcastIface)
	if err != nil {
		log.Printf("fleet mcast: interface %s not found, retrying in 30s", fleetMcastIface)
		time.Sleep(30 * time.Second)
		iface, err = net.InterfaceByName(fleetMcastIface)
		if err != nil {
			log.Printf("fleet mcast: giving up on %s", fleetMcastIface)
			return
		}
	}
	conn, err := net.ListenMulticastUDP("udp4", iface, addr)
	if err != nil {
		log.Printf("fleet mcast: listen error: %v", err)
		return
	}
	defer conn.Close()
	conn.SetReadBuffer(4096)

	buf := make([]byte, 4096)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		var msg map[string]string
		if json.Unmarshal(buf[:n], &msg) != nil {
			continue
		}
		if msg["type"] == "fleet_stage" {
			fleetProcessPackage(buf[:n])
		}
	}
}
