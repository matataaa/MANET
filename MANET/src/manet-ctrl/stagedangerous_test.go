package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApiAdminStageFlagsAdminPasswordAsDangerous is the regression test for
// the stage-time/activate-time dangerous-key inconsistency: apiAdminStage's
// inline "dangerous" boolean used to check only mesh_ssid/mesh_key/
// ipv4_network (missing admin_password), unlike fleetDangerousKeys (the list
// the real Activate-time gate and ack_status use) -- so staging an
// admin_password rotation could show dangerous:false in the immediate stage
// response. This confirms the stage response now agrees with
// fleetDangerousKeys for admin_password specifically.
func TestApiAdminStageFlagsAdminPasswordAsDangerous(t *testing.T) {
	withTempFleetProcessFiles(t)
	origMesh := MeshConfFile
	dir := t.TempDir()
	MeshConfFile = filepath.Join(dir, "mesh.conf")
	t.Cleanup(func() { MeshConfFile = origMesh })
	if err := os.WriteFile(MeshConfFile, []byte("admin_password=old-pw\nmesh_ssid=test-mesh\n"), 0644); err != nil {
		t.Fatalf("seed mesh.conf: %v", err)
	}
	installFakeAlfred(t)

	body := `{"config":{"admin_password":"brand-new-pw"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/stage", strings.NewReader(body))
	rr := httptest.NewRecorder()

	apiAdminStage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	dangerous, _ := resp["dangerous"].(bool)
	if !dangerous {
		t.Fatalf("expected dangerous:true for an admin_password change, got response %s", rr.Body.String())
	}
}

// TestApiAdminStageDoesNotFlagUnchangedAdminPasswordAsDangerous is the
// negative counterpart: re-submitting the SAME admin_password (as the fleet
// UI does on every save, whether or not that field was actually edited)
// must not be flagged as dangerous.
func TestApiAdminStageDoesNotFlagUnchangedAdminPasswordAsDangerous(t *testing.T) {
	withTempFleetProcessFiles(t)
	origMesh := MeshConfFile
	dir := t.TempDir()
	MeshConfFile = filepath.Join(dir, "mesh.conf")
	t.Cleanup(func() { MeshConfFile = origMesh })
	if err := os.WriteFile(MeshConfFile, []byte("admin_password=same-pw\nmesh_ssid=test-mesh\n"), 0644); err != nil {
		t.Fatalf("seed mesh.conf: %v", err)
	}
	installFakeAlfred(t)

	body := `{"config":{"admin_password":"same-pw","node_hostname":"unrelated-edit"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/stage", strings.NewReader(body))
	rr := httptest.NewRecorder()

	apiAdminStage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	dangerous, _ := resp["dangerous"].(bool)
	if dangerous {
		t.Fatalf("expected dangerous:false when admin_password is resubmitted unchanged, got response %s", rr.Body.String())
	}
}
