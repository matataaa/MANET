package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// legacy40 is a 5GHz conf as the noscan build's mesh_5ghz_bw=40 writes it.
const legacy40 = `ctrl_interface=/var/run/wpa_supplicant
country=US
update_config=1
sae_pwe=0
ap_scan=2
network={
    ssid="MANET"
    mode=5
    frequency=5765
    key_mgmt=SAE
    sae_password="secret"
    ieee80211w=2
    mesh_fwding=0
    group_rekey=0
    noscan=1
    disable_vht=1
}
`

func writeConf(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wpa_supplicant-wlan1.conf")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readConf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// networkKeys returns the managed width keys inside network={}, in order.
func networkKeys(conf string) []string {
	var keys []string
	in := false
	for _, line := range strings.Split(conf, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "network={":
			in = true
		case trimmed == "}":
			in = false
		case in && isMeshWidthKey(trimmed):
			keys = append(keys, trimmed)
		}
	}
	return keys
}

func TestMeshWidthKeysManetKeepsHE(t *testing.T) {
	// No manet key set may contain disable_vht: on 5GHz, 2.11 enables HE
	// only together with VHT.
	for _, width := range []string{"20", "40", "80", ""} {
		if meshWidthKeys(width, supplicantManet)["disable_vht=1"] {
			t.Errorf("manet width %q writes disable_vht=1", width)
		}
		if meshWidthKeys(width, supplicantManet)["noscan=1"] {
			t.Errorf("manet width %q writes noscan=1", width)
		}
	}
}

func TestSetMeshWidthKeysLegacyToManet(t *testing.T) {
	cases := []struct {
		width string
		want  []string
	}{
		{"20", []string{"disable_ht40=1", "max_oper_chwidth=0"}},
		{"40", []string{"max_oper_chwidth=0"}},
		{"80", []string{"max_oper_chwidth=1"}},
		{"", []string{"disable_ht40=1", "max_oper_chwidth=0"}}, // 2.4GHz
	}
	for _, c := range cases {
		path := writeConf(t, legacy40)
		if !setMeshWidthKeys(path, c.width, supplicantManet) {
			t.Fatalf("width %q: expected a change", c.width)
		}
		got := readConf(t, path)
		if keys := networkKeys(got); strings.Join(keys, ",") != strings.Join(c.want, ",") {
			t.Errorf("width %q: keys %v, want %v", c.width, keys, c.want)
		}
		if !strings.HasPrefix(got, manetInactivityLine+"\n") {
			t.Errorf("width %q: global %s missing or not first:\n%s", c.width, manetInactivityLine, got)
		}
		if setMeshWidthKeys(path, c.width, supplicantManet) {
			t.Errorf("width %q: second reconcile changed the file again", c.width)
		}
	}
}

func TestSetMeshWidthKeysManetToNoscan(t *testing.T) {
	path := writeConf(t, legacy40)
	setMeshWidthKeys(path, "20", supplicantManet)
	if !setMeshWidthKeys(path, "40", supplicantNoscan) {
		t.Fatal("expected a change back to noscan keys")
	}
	got := readConf(t, path)
	// Keys are re-added in meshWidthKeyNames order; only the set matters.
	if keys := networkKeys(got); strings.Join(keys, ",") != "disable_vht=1,noscan=1" {
		t.Errorf("round trip keys %v, want [disable_vht=1 noscan=1]", keys)
	}
	if strings.Contains(got, "mesh_max_inactivity") {
		t.Errorf("leaving manet kept %s:\n%s", manetInactivityLine, got)
	}
}

func TestSetMeshWidthKeysKeepsExplicitInactivity(t *testing.T) {
	tuned := "mesh_max_inactivity=30\n" + legacy40
	path := writeConf(t, tuned)
	setMeshWidthKeys(path, "40", supplicantManet)
	got := readConf(t, path)
	if strings.Count(got, "mesh_max_inactivity=") != 1 || !strings.HasPrefix(got, "mesh_max_inactivity=30\n") {
		t.Errorf("manet overrode explicit inactivity:\n%s", got)
	}
	setMeshWidthKeys(path, "40", supplicantNoscan)
	if got := readConf(t, path); !strings.HasPrefix(got, "mesh_max_inactivity=30\n") {
		t.Errorf("leaving manet removed explicit inactivity:\n%s", got)
	}
}

func TestSetMeshWidthKeysStock24Untouched(t *testing.T) {
	conf := strings.Replace(strings.Replace(legacy40, "    noscan=1\n    disable_vht=1\n", "", 1),
		"frequency=5765", "frequency=2462", 1)
	path := writeConf(t, conf)
	for _, mode := range []string{supplicantStock, supplicantNoscan} {
		if setMeshWidthKeys(path, "", mode) {
			t.Errorf("%s mode changed a 2.4GHz conf:\n%s", mode, readConf(t, path))
		}
	}
}
