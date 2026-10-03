package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file covers the incident where the C1 redaction fix (assembleAdminStatus
// stripping admin_password/mesh_key/lan_ap_key for unauthenticated callers)
// created a NEW bug: an operator whose edit form was populated pre-login
// would submit "" for those fields, and without a guard, apiAdminSave /
// apiAdminStage / fleetApplyConfig would all happily persist/broadcast an
// empty secret fleet-wide -- bricking auth (admin_password="" makes
// isAuthed() return true for everyone) and the fleet crypto pipeline
// (nothing can derive a key from an empty password) in one move.

func TestDropEmptySecretsStringMap(t *testing.T) {
	m := map[string]string{
		"admin_password": "",
		"mesh_key":       "real-key",
		"lan_ap_key":     "",
		"node_hostname":  "",
	}
	dropEmptySecrets(m)
	if _, ok := m["admin_password"]; ok {
		t.Fatalf("empty admin_password must be dropped")
	}
	if _, ok := m["lan_ap_key"]; ok {
		t.Fatalf("empty lan_ap_key must be dropped")
	}
	if m["mesh_key"] != "real-key" {
		t.Fatalf("non-empty mesh_key must be preserved, got %q", m["mesh_key"])
	}
	if _, ok := m["node_hostname"]; !ok {
		t.Fatalf("dropEmptySecrets must not touch non-secret keys, even if empty")
	}
}

func TestDropEmptySecretsAnyInterfaceMap(t *testing.T) {
	m := map[string]interface{}{
		"admin_password": "",
		"mesh_key":       "real-key",
		"lan_ap_key":     nil, // JSON null decodes to a nil interface
		"node_hostname":  "",
	}
	dropEmptySecretsAny(m)
	if _, ok := m["admin_password"]; ok {
		t.Fatalf("empty string admin_password must be dropped")
	}
	if _, ok := m["lan_ap_key"]; ok {
		t.Fatalf("nil (JSON null) lan_ap_key must be dropped")
	}
	if m["mesh_key"] != "real-key" {
		t.Fatalf("non-empty mesh_key must be preserved, got %v", m["mesh_key"])
	}
	if _, ok := m["node_hostname"]; !ok {
		t.Fatalf("dropEmptySecretsAny must not touch non-secret keys, even if empty")
	}
}

// withTempMeshConf points MeshConfFile (and PendingConfFile, so
// apiAdminSave's "fleet config staged" guard doesn't fire) at throwaway temp
// files, restoring the originals afterward.
func withTempMeshConf(t *testing.T, initialContent string) {
	t.Helper()
	dir := t.TempDir()
	origMesh := MeshConfFile
	origPending := PendingConfFile
	MeshConfFile = filepath.Join(dir, "mesh.conf")
	PendingConfFile = filepath.Join(dir, "pending.json")
	t.Cleanup(func() {
		MeshConfFile = origMesh
		PendingConfFile = origPending
	})
	if initialContent != "" {
		if err := os.WriteFile(MeshConfFile, []byte(initialContent), 0644); err != nil {
			t.Fatalf("seed MeshConfFile: %v", err)
		}
	}
}

// TestApiAdminSaveEmptySecretsOnlyLeavesMeshConfByteUnchanged is the exact
// scenario from the review: submit ONLY empty secret values (as would
// happen if a pre-login edit form got retried after authenticating).
// apiAdminSave must reject this as "no valid keys" -- and critically, must
// never have opened MeshConfFile for writing at all.
func TestApiAdminSaveEmptySecretsOnlyLeavesMeshConfByteUnchanged(t *testing.T) {
	const initial = "admin_password=old-secret-pw\nmesh_key=old-sae-key\nrequire_auth=n\n"
	withTempMeshConf(t, initial)

	body := `{"config":{"admin_password":"","mesh_key":"","lan_ap_key":""}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/save", strings.NewReader(body))
	rr := httptest.NewRecorder()

	apiAdminSave(rr, req)

	after, err := os.ReadFile(MeshConfFile)
	if err != nil {
		t.Fatalf("read mesh.conf after save attempt: %v", err)
	}
	if string(after) != initial {
		t.Fatalf("mesh.conf was modified by an all-empty-secrets save:\nbefore: %q\nafter:  %q", initial, string(after))
	}
	if rr.Code == http.StatusOK {
		t.Fatalf("expected apiAdminSave to reject an all-empty-secrets payload, got 200: %s", rr.Body.String())
	}
}

// TestApiAdminSaveEmptySecretsSurgicallyIgnored covers the mixed case: a
// real, non-secret change submitted ALONGSIDE empty secret values. The real
// change must still apply; the secrets must be preserved exactly.
func TestApiAdminSaveEmptySecretsSurgicallyIgnored(t *testing.T) {
	const initial = "admin_password=old-secret-pw\nmesh_key=old-sae-key\nrequire_auth=n\n"
	withTempMeshConf(t, initial)

	body := `{"config":{"admin_password":"","mesh_key":"","require_auth":"y"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/save", strings.NewReader(body))
	rr := httptest.NewRecorder()

	apiAdminSave(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a save with one real change, got %d: %s", rr.Code, rr.Body.String())
	}

	afterConf := loadKVFile(MeshConfFile)
	if afterConf["admin_password"] != "old-secret-pw" {
		t.Fatalf("admin_password was overwritten by an empty submitted value: got %q", afterConf["admin_password"])
	}
	if afterConf["mesh_key"] != "old-sae-key" {
		t.Fatalf("mesh_key was overwritten by an empty submitted value: got %q", afterConf["mesh_key"])
	}
	if afterConf["require_auth"] != "y" {
		t.Fatalf("expected the real, non-secret change (require_auth=y) to still apply, got %q", afterConf["require_auth"])
	}
}

// TestFleetApplyConfigBackstopIgnoresEmptySecrets covers the backstop in
// fleetApplyConfig -- the function that actually writes mesh.conf on every
// OTHER node in the fleet when an activated package is applied. Even if
// apiAdminStage's own dropEmptySecretsAny were somehow bypassed, this must
// independently refuse to persist an empty admin_password/mesh_key/
// lan_ap_key, while still applying any other real change in the same
// package.
func TestFleetApplyConfigBackstopIgnoresEmptySecrets(t *testing.T) {
	const initial = "admin_password=old-secret-pw\nmesh_key=old-sae-key\nrequire_auth=n\n"
	withTempMeshConf(t, initial)

	pkg := map[string]interface{}{
		"config": map[string]interface{}{
			"admin_password": "",
			"mesh_key":       "",
			"require_auth":   "y",
		},
	}

	fleetApplyConfig(pkg)

	afterConf := loadKVFile(MeshConfFile)
	if afterConf["admin_password"] != "old-secret-pw" {
		t.Fatalf("fleetApplyConfig backstop failed: admin_password was overwritten, got %q", afterConf["admin_password"])
	}
	if afterConf["mesh_key"] != "old-sae-key" {
		t.Fatalf("fleetApplyConfig backstop failed: mesh_key was overwritten, got %q", afterConf["mesh_key"])
	}
	if afterConf["require_auth"] != "y" {
		t.Fatalf("expected the real, non-secret change (require_auth=y) to still apply, got %q", afterConf["require_auth"])
	}
}

// TestFleetApplyConfigBackstopAllEmptySecretsLeavesFileByteUnchanged is the
// pure "stage/save {admin_password:”,mesh_key:”}" scenario at the
// fleetApplyConfig layer: no other real change in the package at all.
func TestFleetApplyConfigBackstopAllEmptySecretsLeavesFileByteUnchanged(t *testing.T) {
	const initial = "admin_password=old-secret-pw\nmesh_key=old-sae-key\nrequire_auth=n\n"
	withTempMeshConf(t, initial)

	pkg := map[string]interface{}{
		"config": map[string]interface{}{
			"admin_password": "",
			"mesh_key":       "",
		},
	}

	fleetApplyConfig(pkg)

	after, err := os.ReadFile(MeshConfFile)
	if err != nil {
		t.Fatalf("read mesh.conf after fleetApplyConfig: %v", err)
	}
	if string(after) != initial {
		t.Fatalf("mesh.conf was modified by an all-empty-secrets fleet apply:\nbefore: %q\nafter:  %q", initial, string(after))
	}
}
