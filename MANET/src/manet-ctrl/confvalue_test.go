package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Covers mesh_ssid/mesh_key values that the supplicant writers can't
// represent, and the '$' expansion that regexp.ReplaceAllString applied to
// the replacement text (e.g. "abc$def123" was written as "abc").

func TestRewriteWPAConfKeepsDollarSigns(t *testing.T) {
	const in = "network={\n\tssid=\"old\"\n\tmode=5\n\tsae_password=\"old-key\"\n}\n"
	got := rewriteWPAConf(in, "mesh$1", "pa$$word-abc$def123")
	want := "network={\n\tssid=\"mesh$1\"\n\tmode=5\n\tsae_password=\"pa$$word-abc$def123\"\n}\n"
	if got != want {
		t.Fatalf("rewriteWPAConf:\n got %q\nwant %q", got, want)
	}
}

func TestRewriteWPAConfEmptyKeyLeavesPasswordAlone(t *testing.T) {
	const in = "ssid=\"old\"\nsae_password=\"keep-me\"\n"
	got := rewriteWPAConf(in, "new", "")
	if got != "ssid=\"new\"\nsae_password=\"keep-me\"\n" {
		t.Fatalf("empty key must not touch sae_password, got %q", got)
	}
}

func TestConfigValueError(t *testing.T) {
	cases := []struct {
		key, value string
		ok         bool
	}{
		{"mesh_key", "pa$$word\\with`odd'chars", true},
		{"mesh_ssid", strings.Repeat("a", 32), true},
		{"mesh_ssid", strings.Repeat("a", 33), false},
		{"mesh_ssid", "mesh\"x", false},
		{"mesh_key", "key\"\n\tctrl_interface=/tmp/x", false},
		{"mesh_key", "tab\there", false},
		{"mesh_key", "del\x7f", false},
		{"node_hostname", "a\nrequire_auth=n", false},
		{"node_hostname", "a\rb", false},
		{"lan_ap_ssid", "quotes\"are fine unquoted", true},
	}
	for _, c := range cases {
		err := configValueError(c.key, c.value)
		if (err == nil) != c.ok {
			t.Errorf("configValueError(%q, %q) = %v, want ok=%v", c.key, c.value, err, c.ok)
		}
	}
}

func TestSaveKVFileRefusesLineBreaks(t *testing.T) {
	const initial = "require_auth=y\n"
	withTempMeshConf(t, initial)

	if err := saveKVFile(MeshConfFile, map[string]string{"node_hostname": "x\nrequire_auth=n"}); err == nil {
		t.Fatalf("saveKVFile accepted a value containing a newline")
	}
	after, _ := os.ReadFile(MeshConfFile)
	if string(after) != initial {
		t.Fatalf("mesh.conf modified: %q", after)
	}
}

func TestApiAdminSaveRejectsUnrepresentableMeshKey(t *testing.T) {
	const initial = "mesh_ssid=mesh\nmesh_key=old-sae-key\n"
	withTempMeshConf(t, initial)

	payload, _ := json.Marshal(map[string]interface{}{
		"config": map[string]string{"mesh_key": "new\"\nctrl_interface=/tmp/x"},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/save", strings.NewReader(string(payload)))
	rr := httptest.NewRecorder()
	apiAdminSave(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	after, _ := os.ReadFile(MeshConfFile)
	if string(after) != initial {
		t.Fatalf("mesh.conf modified by a rejected save: %q", after)
	}
}

func TestFleetApplyConfigDropsUnrepresentableValues(t *testing.T) {
	withTempMeshConf(t, "mesh_ssid=mesh\nmesh_key=old-sae-key\nrequire_auth=n\n")

	fleetApplyConfig(map[string]interface{}{
		"config": map[string]interface{}{
			"mesh_key":     "bad\"key",
			"require_auth": "y",
		},
	})

	conf := loadKVFile(MeshConfFile)
	if conf["mesh_key"] != "old-sae-key" {
		t.Fatalf("invalid mesh_key was applied: %q", conf["mesh_key"])
	}
	if conf["require_auth"] != "y" {
		t.Fatalf("valid key in the same push was not applied: require_auth=%q", conf["require_auth"])
	}
}
