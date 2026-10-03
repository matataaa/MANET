package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// signedTarball makes a key pair in dir, a fake tarball named name, and
// signs it for version. Returns the tarball path and the key dir.
func signedTarball(t *testing.T, name, version string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	keyDir := filepath.Join(dir, "keys")
	os.Mkdir(keyDir, 0755)
	priv := filepath.Join(dir, "ota.key")
	if err := otaCommand([]string{"ota-keygen", priv, filepath.Join(keyDir, "release.pub")}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(priv); info.Mode().Perm() != 0600 {
		t.Fatalf("private key mode %v, want 0600", info.Mode().Perm())
	}
	tarball := filepath.Join(dir, name)
	os.WriteFile(tarball, []byte("pretend tarball"), 0644)
	if err := otaCommand([]string{"ota-sign", priv, version, tarball}); err != nil {
		t.Fatal(err)
	}
	return tarball, keyDir
}

func TestVerifyOTA(t *testing.T) {
	tarball, keyDir := signedTarball(t, "cm4-tools.tar.gz", "0.4.7")
	keys, err := loadOTAKeys(keyDir)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := os.ReadFile(tarball + ".sig")

	if err := verifyOTA(tarball, "cm4-tools.tar.gz", "0.4.7", string(sig), keys); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if verifyOTA(tarball, "cm4-tools.tar.gz", "0.4.9", string(sig), keys) == nil {
		t.Error("accepted under another version (rollback relabel)")
	}
	if verifyOTA(tarball, "cm4-sbc-overlay.tar.gz", "0.4.7", string(sig), keys) == nil {
		t.Error("accepted under another file name")
	}
	if verifyOTA(tarball, "cm4-tools.tar.gz", "0.4.7", "bm90IGEgc2ln", keys) == nil {
		t.Error("accepted a malformed signature")
	}
	_, otherKeys := signedTarball(t, "x", "1")
	other, _ := loadOTAKeys(otherKeys)
	if verifyOTA(tarball, "cm4-tools.tar.gz", "0.4.7", string(sig), other) == nil {
		t.Error("accepted with an unrelated key")
	}
	os.WriteFile(tarball, []byte("tampered tarball"), 0644)
	if verifyOTA(tarball, "cm4-tools.tar.gz", "0.4.7", string(sig), keys) == nil {
		t.Error("accepted a modified tarball")
	}
}

func TestLoadOTAKeysEmpty(t *testing.T) {
	if _, err := loadOTAKeys(t.TempDir()); err == nil {
		t.Fatal("no keys must be an error, never an open door")
	}
}

func TestOTAKeygenNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "ota.key")
	os.WriteFile(priv, []byte("existing"), 0600)
	if otaCommand([]string{"ota-keygen", priv, filepath.Join(dir, "x.pub")}) == nil {
		t.Fatal("keygen overwrote an existing private key")
	}
}

// End to end over plain http, as from an off-grid gateway.
func TestCheckOTASignatureOverHTTP(t *testing.T) {
	tarball, keyDir := signedTarball(t, "cm4-tools.tar.gz", "0.4.7")
	origKeys := otaKeysDir
	otaKeysDir = keyDir
	t.Cleanup(func() { otaKeysDir = origKeys })

	srv := httptest.NewServer(http.FileServer(http.Dir(filepath.Dir(tarball))))
	defer srv.Close()

	if err := checkOTASignature(srv.URL+"/cm4-tools.tar.gz", tarball, "0.4.7"); err != nil {
		t.Fatalf("signed package refused: %v", err)
	}
	os.Remove(tarball + ".sig")
	if checkOTASignature(srv.URL+"/cm4-tools.tar.gz", tarball, "0.4.7") == nil {
		t.Fatal("unsigned package accepted without update_allow_unsigned")
	}
}
