package main

import (
	"encoding/json"
	"testing"
)

// TestRedactSecretKeys covers the C1 fix: admin_password/mesh_key/lan_ap_key
// must never survive redaction, in any of the 3 places they can appear.

func TestRedactSecretKeysStringMap(t *testing.T) {
	m := map[string]string{
		"admin_password": "hunter2",
		"mesh_key":       "sae-key",
		"lan_ap_key":     "ap-key",
		"node_hostname":  "eud1",
	}
	redactSecretKeys(m)
	for _, k := range fleetSecretKeys {
		if _, ok := m[k]; ok {
			t.Fatalf("secret key %q survived redactSecretKeys", k)
		}
	}
	if m["node_hostname"] != "eud1" {
		t.Fatalf("redactSecretKeys must not touch non-secret keys")
	}
}

func TestRedactPendingSecretsTopLevelConfig(t *testing.T) {
	pkg := map[string]interface{}{
		"version": "v1",
		"config": map[string]interface{}{
			"admin_password": "hunter2",
			"mesh_key":       "sae-key",
			"node_hostname":  "eud1",
		},
	}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	redacted := redactPendingSecrets(raw)
	if redacted == nil {
		t.Fatalf("redactPendingSecrets returned nil for valid input")
	}

	var got map[string]interface{}
	if err := json.Unmarshal(redacted, &got); err != nil {
		t.Fatalf("unmarshal redacted: %v", err)
	}
	cfg, _ := got["config"].(map[string]interface{})
	if cfg == nil {
		t.Fatalf("config missing from redacted pending")
	}
	if _, ok := cfg["admin_password"]; ok {
		t.Fatalf("admin_password survived redactPendingSecrets in top-level config")
	}
	if _, ok := cfg["mesh_key"]; ok {
		t.Fatalf("mesh_key survived redactPendingSecrets in top-level config")
	}
	if cfg["node_hostname"] != "eud1" {
		t.Fatalf("non-secret key node_hostname was incorrectly stripped")
	}
}

func TestRedactPendingSecretsNestedProfiles(t *testing.T) {
	// This is the exact shape fleetSyncProfiles/apiAdminStage produce:
	// pkg["profiles"][<id>]["config"][<key>].
	pkg := map[string]interface{}{
		"version": "v1",
		"config":  map[string]interface{}{},
		"profiles": map[string]interface{}{
			"profile-a": map[string]interface{}{
				"name": "Profile A",
				"config": map[string]interface{}{
					"admin_password": "hunter2",
					"lan_ap_key":     "ap-key",
					"node_hostname":  "eud2",
				},
			},
		},
	}
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	redacted := redactPendingSecrets(raw)
	var got map[string]interface{}
	if err := json.Unmarshal(redacted, &got); err != nil {
		t.Fatalf("unmarshal redacted: %v", err)
	}
	profiles, _ := got["profiles"].(map[string]interface{})
	profA, _ := profiles["profile-a"].(map[string]interface{})
	cfg, _ := profA["config"].(map[string]interface{})
	if cfg == nil {
		t.Fatalf("nested profile config missing after redaction")
	}
	if _, ok := cfg["admin_password"]; ok {
		t.Fatalf("admin_password survived redactPendingSecrets inside a nested profile config")
	}
	if _, ok := cfg["lan_ap_key"]; ok {
		t.Fatalf("lan_ap_key survived redactPendingSecrets inside a nested profile config")
	}
	if cfg["node_hostname"] != "eud2" {
		t.Fatalf("non-secret key node_hostname was incorrectly stripped from nested profile config")
	}
}

func TestRedactPendingSecretsFailsClosedOnBadInput(t *testing.T) {
	if got := redactPendingSecrets(nil); got != nil {
		t.Fatalf("redactPendingSecrets(nil) must return nil, got %v", got)
	}
	if got := redactPendingSecrets(json.RawMessage(`not json at all {{{`)); got != nil {
		t.Fatalf("redactPendingSecrets must fail closed (return nil) on unparseable input, got %v", got)
	}
}

func TestRedactFleetPreferences(t *testing.T) {
	prefs := FleetPreferences{
		MeshConfig: map[string]string{
			"admin_password": "hunter2",
			"node_hostname":  "eud3",
		},
		Profiles: map[string]FleetProfile{
			"p1": {
				Name: "Profile 1",
				Config: map[string]string{
					"mesh_key":      "sae-key",
					"node_hostname": "eud4",
				},
			},
		},
		NodeProfiles: map[string]string{},
	}

	redactFleetPreferences(&prefs)

	if _, ok := prefs.MeshConfig["admin_password"]; ok {
		t.Fatalf("admin_password survived redactFleetPreferences in MeshConfig")
	}
	if prefs.MeshConfig["node_hostname"] != "eud3" {
		t.Fatalf("non-secret MeshConfig key was incorrectly stripped")
	}
	if _, ok := prefs.Profiles["p1"].Config["mesh_key"]; ok {
		t.Fatalf("mesh_key survived redactFleetPreferences in a profile's Config")
	}
	if prefs.Profiles["p1"].Config["node_hostname"] != "eud4" {
		t.Fatalf("non-secret profile Config key was incorrectly stripped")
	}
}

// TestDangerousKeyChanges covers W3's core logic: presence in a pushed
// config is NOT enough to count as a "change" (fleet pushes resend every
// field every time) -- only an actual value difference from the current
// on-disk config counts.
func TestDangerousKeyChanges(t *testing.T) {
	current := map[string]string{
		"admin_password": "old-pw",
		"mesh_ssid":      "mesh1",
		"mesh_key":       "old-key",
		"ipv4_network":   "10.30.2.0/24",
		"node_hostname":  "eud1",
	}

	t.Run("no_changes", func(t *testing.T) {
		configRaw := map[string]interface{}{
			"admin_password": "old-pw",
			"mesh_ssid":      "mesh1",
			"node_hostname":  "eud1-renamed",
		}
		changed := dangerousKeyChanges(configRaw, current)
		if len(changed) != 0 {
			t.Fatalf("expected no dangerous keys changed, got %v", changed)
		}
	})

	t.Run("admin_password_changed", func(t *testing.T) {
		configRaw := map[string]interface{}{
			"admin_password": "new-pw",
		}
		changed := dangerousKeyChanges(configRaw, current)
		if len(changed) != 1 || changed[0] != "admin_password" {
			t.Fatalf("expected only admin_password flagged, got %v", changed)
		}
	})

	t.Run("multiple_changed", func(t *testing.T) {
		configRaw := map[string]interface{}{
			"admin_password": "new-pw",
			"mesh_ssid":      "mesh2",
			"ipv4_network":   "10.30.2.0/24", // unchanged
		}
		changed := dangerousKeyChanges(configRaw, current)
		if len(changed) != 2 {
			t.Fatalf("expected exactly 2 dangerous keys changed, got %v", changed)
		}
	})

	t.Run("non_dangerous_key_ignored", func(t *testing.T) {
		configRaw := map[string]interface{}{
			"node_hostname": "totally-different",
		}
		changed := dangerousKeyChanges(configRaw, current)
		if len(changed) != 0 {
			t.Fatalf("node_hostname is not a dangerous key, expected none flagged, got %v", changed)
		}
	})
}
