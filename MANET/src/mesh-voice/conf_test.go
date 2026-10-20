package main

import "testing"

func defaultConfig() Config {
	return Config{Iface: "br0", McastAddr: "239.69.0.1", McastPort: 4370, PTTSource: "openvlm"}
}

func TestApplyConfDefaultsChannel(t *testing.T) {
	cfg := defaultConfig()
	applyConfDefaults(&cfg, map[string]string{
		"voice_channel": "4", "voice_iface": "bat0", "voice_ptt_mode": "vox",
	}, nil)
	want := Config{Iface: "bat0", McastAddr: "239.69.0.4", McastPort: 4373, PTTSource: "vox"}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestApplyConfDefaultsFlagsWin(t *testing.T) {
	cfg := Config{Iface: "eth0", McastAddr: "239.69.0.9", McastPort: 5000, PTTSource: "gpio"}
	applyConfDefaults(&cfg, map[string]string{
		"voice_channel": "4", "voice_iface": "bat0", "voice_ptt_mode": "vox",
	}, map[string]bool{"iface": true, "addr": true, "port": true, "ptt": true})
	if cfg.Iface != "eth0" || cfg.McastAddr != "239.69.0.9" || cfg.McastPort != 5000 || cfg.PTTSource != "gpio" {
		t.Fatalf("explicit flags overridden: %+v", cfg)
	}
}

func TestApplyConfDefaultsIgnoresInvalid(t *testing.T) {
	for _, conf := range []map[string]string{
		nil,
		{"voice_channel": "0"},
		{"voice_channel": "22"},
		{"voice_channel": "x"},
		{"voice_ptt_mode": "bogus"},
	} {
		cfg := defaultConfig()
		applyConfDefaults(&cfg, conf, nil)
		if cfg != defaultConfig() {
			t.Errorf("conf %v changed defaults: %+v", conf, cfg)
		}
	}
}
