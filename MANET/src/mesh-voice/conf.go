package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Channel layout, shared with manet-ctrl (voice.go): channel N is multicast
// group 239.69.0.N on port 4370+N-1.
const (
	voiceChannelCount  = 21
	voiceMcastBase     = "239.69.0."
	voiceMcastPortBase = 4370
)

var validPTT = map[string]bool{"always": true, "gpio": true, "openvlm": true, "vox": true}

// readMeshConf returns the key=value pairs of mesh.conf, or nil if it can't
// be read.
func readMeshConf(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	conf := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			conf[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return conf
}

// applyConfDefaults fills every setting not given on the command line from
// mesh.conf: voice_iface, voice_ptt_mode and voice_channel (-> -addr/-port).
// manet-ctrl changes the channel by editing mesh.conf and restarting the
// service, so the shipped unit passes no flags and the channel survives an
// OTA that reinstalls the unit.
func applyConfDefaults(cfg *Config, conf map[string]string, set map[string]bool) {
	if v := conf["voice_iface"]; v != "" && !set["iface"] {
		cfg.Iface = v
	}
	if v := conf["voice_ptt_mode"]; validPTT[v] && !set["ptt"] {
		cfg.PTTSource = v
	}
	ch, err := strconv.Atoi(conf["voice_channel"])
	if err != nil || ch < 1 || ch > voiceChannelCount {
		return
	}
	if !set["addr"] {
		cfg.McastAddr = fmt.Sprintf("%s%d", voiceMcastBase, ch)
	}
	if !set["port"] {
		cfg.McastPort = voiceMcastPortBase + ch - 1
	}
}
