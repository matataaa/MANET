package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withOfflineStore points the store, board, installed versions and the
// node-update signature check at a temp dir. verifyOK picks what the fake
// `node-update ota-verify` answers.
func withOfflineStore(t *testing.T, verifyOK bool) string {
	t.Helper()
	withTempMeshConf(t, "mesh_ssid=test\n")
	dir := t.TempDir()
	origDir, origBin, origModel := PackageDir, nodeUpdateBin, deviceModelFile
	origSW, origOV := installedVersionFile["software"], installedVersionFile["overlay"]
	PackageDir = filepath.Join(dir, "lib", "manet-packages")
	os.MkdirAll(filepath.Dir(PackageDir), 0755)
	deviceModelFile = filepath.Join(dir, "model")
	os.WriteFile(deviceModelFile, []byte("Raspberry Pi Compute Module 4 Rev 1.1\x00"), 0644)
	installedVersionFile["software"] = filepath.Join(dir, "release_version.txt")
	installedVersionFile["overlay"] = filepath.Join(dir, "overlay_version.txt")
	os.WriteFile(installedVersionFile["software"], []byte("0.5.8\n"), 0644)
	os.WriteFile(installedVersionFile["overlay"], []byte("0.559\n"), 0644)
	nodeUpdateBin = filepath.Join(dir, "node-update")
	script := "#!/bin/sh\nexit 0\n"
	if !verifyOK {
		script = "#!/bin/sh\necho 'signature does not match any installed OTA key'\nexit 1\n"
	}
	os.WriteFile(nodeUpdateBin, []byte(script), 0755)
	t.Cleanup(func() {
		PackageDir, nodeUpdateBin, deviceModelFile = origDir, origBin, origModel
		installedVersionFile["software"], installedVersionFile["overlay"] = origSW, origOV
	})
	return dir
}

func releaseZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		f, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(content))
	}
	zw.Close()
	return buf.Bytes()
}

func uploadOffline(t *testing.T, files map[string][]byte) (int, map[string]interface{}) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		fw, _ := mw.CreateFormFile("files", name)
		fw.Write(data)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/offline-update", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	apiOfflineUpdate(rec, req)
	var resp map[string]interface{}
	json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func TestOfflineUploadZip(t *testing.T) {
	withOfflineStore(t, true)
	z := releaseZip(t, map[string]string{
		"v0.5.9/manet_release_version.txt":    "0.5.9",
		"v0.5.9/cm4-tools.tar.gz":             "tools",
		"v0.5.9/cm4-tools.tar.gz.sig":         "sig",
		"v0.5.9/cm4-overlay-version.txt":      "0.567",
		"v0.5.9/cm4-sbc-overlay.tar.gz":       "overlay",
		"v0.5.9/cm4-sbc-overlay.tar.gz.sig":   "sig",
		"v0.5.9/manet-cm4-release-v0.5.9.img": "image",
	})
	code, resp := uploadOffline(t, map[string][]byte{"manet-v0.5.9.zip": z})
	if code != 200 {
		t.Fatalf("upload: %d %v", code, resp)
	}
	s := readOfflineStore()
	if s.Software == nil || s.Software.Version != "0.5.9" || !s.Software.Signed {
		t.Errorf("software = %+v", s.Software)
	}
	if s.Overlay == nil || s.Overlay.Version != "0.567" {
		t.Errorf("overlay = %+v", s.Overlay)
	}
	if _, err := os.Stat(filepath.Join(PackageDir, "manet-cm4-release-v0.5.9.img")); err == nil {
		t.Error("stored a file that is not part of a package")
	}
	if ign, _ := resp["ignored"].([]interface{}); len(ign) != 1 {
		t.Errorf("ignored = %v, want the .img", resp["ignored"])
	}
	// Leftover upload dirs next to the store would fill the disk.
	entries, _ := os.ReadDir(filepath.Dir(PackageDir))
	if len(entries) != 1 {
		t.Errorf("left %d entries next to the store", len(entries))
	}
}

func TestOfflineUploadLooseFilesAndChecks(t *testing.T) {
	withOfflineStore(t, true)
	code, _ := uploadOffline(t, map[string][]byte{
		"cm4-tools.tar.gz":          []byte("tools"),
		"cm4-tools.tar.gz.sig":      []byte("sig"),
		"manet_release_version.txt": []byte("0.5.9\n"),
	})
	if code != 200 || readOfflineStore().Software == nil {
		t.Fatalf("loose files: %d", code)
	}

	// An older package must not replace a newer one the node holds.
	code, resp := uploadOffline(t, map[string][]byte{
		"cm4-tools.tar.gz":          []byte("old"),
		"manet_release_version.txt": []byte("0.5.7"),
	})
	if code != 400 || !strings.Contains(resp["error"].(string), "newer") {
		t.Errorf("older package: %d %v", code, resp)
	}
	// A package without its version file can't be checked or installed.
	code, _ = uploadOffline(t, map[string][]byte{"cm4-sbc-overlay.tar.gz": []byte("overlay")})
	if code != 400 {
		t.Errorf("overlay without version file: %d", code)
	}
	// Another board's package is not for this node.
	code, _ = uploadOffline(t, map[string][]byte{
		"rpi5-tools.tar.gz":         []byte("tools"),
		"manet_release_version.txt": []byte("0.6.0"),
	})
	if code != 400 {
		t.Errorf("rpi5 package on a cm4: %d", code)
	}
	if v := readOfflineStore().Software.Version; v != "0.5.9" {
		t.Errorf("failed uploads changed the store: %s", v)
	}
}

func TestOfflineUploadRefusesBadSignature(t *testing.T) {
	withOfflineStore(t, false)
	code, resp := uploadOffline(t, map[string][]byte{
		"cm4-tools.tar.gz":          []byte("tools"),
		"manet_release_version.txt": []byte("0.5.9"),
	})
	if code != 400 || !strings.Contains(resp["error"].(string), "signature") {
		t.Fatalf("bad signature: %d %v", code, resp)
	}
	if readOfflineStore().Software != nil {
		t.Fatal("stored a package that failed its signature check")
	}

	os.WriteFile(MeshConfFile, []byte("update_allow_unsigned=y\n"), 0644)
	if code, _ := uploadOffline(t, map[string][]byte{
		"cm4-tools.tar.gz":          []byte("tools"),
		"manet_release_version.txt": []byte("0.5.9"),
	}); code != 200 {
		t.Fatalf("update_allow_unsigned=y: %d", code)
	}
}

func TestOfflineUploadNeedsLogin(t *testing.T) {
	withOfflineStore(t, true)
	os.WriteFile(MeshConfFile, []byte("require_auth=y\nadmin_password=secret\n"), 0644)
	if code, _ := uploadOffline(t, map[string][]byte{"cm4-tools.tar.gz": []byte("x")}); code != 401 {
		t.Fatalf("upload without login: %d", code)
	}
}

func TestOfflineFilesServesOnlyPackageFiles(t *testing.T) {
	withOfflineStore(t, true)
	os.MkdirAll(PackageDir, 0755)
	os.WriteFile(filepath.Join(PackageDir, "cm4-tools.tar.gz"), []byte("tools"), 0644)
	os.WriteFile(filepath.Join(filepath.Dir(PackageDir), "secret"), []byte("no"), 0644)

	for path, want := range map[string]int{
		"/api/offline-update/files/cm4-tools.tar.gz": 200,
		"/api/offline-update/files/../secret":        404,
		"/api/offline-update/files/secret":           404,
		"/api/offline-update/files/":                 404,
	} {
		rec := httptest.NewRecorder()
		apiOfflineFiles(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

// A peer copies a package from the source node's store into its own.
func TestFetchOfflinePackageFromPeer(t *testing.T) {
	withOfflineStore(t, true)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "cm4-tools.tar.gz"), []byte("tools"), 0644)
	os.WriteFile(filepath.Join(src, "cm4-tools.tar.gz.sig"), []byte("sig\n"), 0644)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/offline-update" {
			writeJSON(w, 200, offlineStore{Board: "cm4", Software: &offlinePackage{File: "cm4-tools.tar.gz", Version: "0.5.9", Size: 5, Signed: true}})
			return
		}
		http.ServeFile(w, r, filepath.Join(src, strings.TrimPrefix(r.URL.Path, "/api/offline-update/files/")))
	}))
	defer srv.Close()
	origTLS := peerTLSConfig
	peerTLSConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	t.Cleanup(func() { peerTLSConfig = origTLS })
	host := strings.TrimPrefix(srv.URL, "https://")

	if err := fetchOfflinePackage("both", host); err != nil {
		t.Fatal(err)
	}
	s := readOfflineStore()
	if s.Software == nil || s.Software.Version != "0.5.9" || !s.Software.Signed {
		t.Fatalf("fetched store = %+v", s.Software)
	}
	if s.Overlay != nil {
		t.Error("fetched an overlay the source does not hold")
	}

	// Not newer than what is installed: nothing to fetch.
	os.WriteFile(installedVersionFile["software"], []byte("0.6.0\n"), 0644)
	os.RemoveAll(PackageDir)
	if err := fetchOfflinePackage("software", host); err != nil {
		t.Fatal(err)
	}
	if readOfflineStore().Software != nil {
		t.Error("fetched a package older than the installed release")
	}
}

func TestVersionNewer(t *testing.T) {
	cases := []struct {
		ch, a, b string
		want     bool
	}{
		{"software", "0.5.10", "0.5.9", true},
		{"software", "0.5.9", "0.5.9", false},
		{"software", "0.5.9", "", true},
		{"software", "junk", "0.0.1", false},
		{"overlay", "0.567", "0.559", true},
		{"overlay", "0.559", "0.567", false},
	}
	for _, c := range cases {
		if got := versionNewer(c.ch, c.a, c.b); got != c.want {
			t.Errorf("versionNewer(%s, %q, %q) = %v", c.ch, c.a, c.b, got)
		}
	}
}
