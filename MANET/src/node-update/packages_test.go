package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTrigger(t *testing.T) {
	cases := []struct {
		in   string
		want trigger
	}{
		{"software", trigger{software: true}},
		{"both\n", trigger{software: true, overlay: true}},
		{"check", trigger{checkOnly: true}},
		{"overlay\nsource=/var/lib/manet-packages", trigger{overlay: true, source: "/var/lib/manet-packages"}},
		// Only a clean absolute directory is a source.
		{"software\nsource=https://10.30.2.1/x", trigger{software: true}},
		{"software\nsource=/var/lib/../../etc", trigger{software: true}},
		{"software\nsource=/", trigger{software: true}},
		{"bogus\nsource=/var/lib/manet-packages", trigger{}},
		{"", trigger{}},
	}
	for _, c := range cases {
		if got := parseTrigger(c.in); got != c.want {
			t.Errorf("parseTrigger(%q) = %+v; want %+v", c.in, got, c.want)
		}
	}
}

// An offline package is read from a local directory laid out like the
// update server, and its signature is checked exactly as over http.
func TestOfflineSourceDetectAndVerify(t *testing.T) {
	tarball, keyDir := signedTarball(t, "cm4-tools.tar.gz", "0.4.7")
	origKeys := otaKeysDir
	otaKeysDir = keyDir
	t.Cleanup(func() { otaKeysDir = origKeys })
	src := filepath.Dir(tarball)
	os.WriteFile(filepath.Join(src, "manet_release_version.txt"), []byte("0.4.7\n"), 0644)

	_, remote, _, err := detectSoftware(src)
	if err != nil || remote != "0.4.7" {
		t.Fatalf("detectSoftware(local dir) = %q, %v", remote, err)
	}
	dest := filepath.Join(t.TempDir(), "tools.tar.gz")
	if err := download(filepath.Join(src, "cm4-tools.tar.gz"), dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tarball); err != nil {
		t.Fatal("download from a local source must copy, not move, the package")
	}
	if err := checkOTASignature(filepath.Join(src, "cm4-tools.tar.gz"), dest, "0.4.7"); err != nil {
		t.Fatalf("signed offline package refused: %v", err)
	}
	if err := otaCommand([]string{"ota-verify", "0.4.7", tarball}); err != nil {
		t.Fatalf("ota-verify refused a valid package: %v", err)
	}
	if otaCommand([]string{"ota-verify", "0.4.8", tarball}) == nil {
		t.Fatal("ota-verify accepted the package under another version")
	}
}

func TestKeepPackage(t *testing.T) {
	tarball, _ := signedTarball(t, "cm4-tools.tar.gz", "0.4.7")
	orig := packageDir
	packageDir = filepath.Join(t.TempDir(), "packages")
	t.Cleanup(func() { packageDir = orig })

	// The recovery copy has another name; the store uses the published one.
	kept := filepath.Join(filepath.Dir(tarball), "software.tar.gz")
	data, _ := os.ReadFile(tarball)
	os.WriteFile(kept, data, 0600)

	keepPackage(tarball, kept, "manet_release_version.txt", "0.4.7")

	got, err := os.ReadFile(filepath.Join(packageDir, "cm4-tools.tar.gz"))
	if err != nil || string(got) != string(data) {
		t.Fatalf("package not kept: %v", err)
	}
	wantSig, _ := os.ReadFile(tarball + ".sig")
	if sig, _ := os.ReadFile(filepath.Join(packageDir, "cm4-tools.tar.gz.sig")); string(sig) != string(wantSig) {
		t.Errorf("signature not kept: %q", sig)
	}
	if v, _ := os.ReadFile(filepath.Join(packageDir, "manet_release_version.txt")); strings.TrimSpace(string(v)) != "0.4.7" {
		t.Errorf("version file = %q", v)
	}

	// Re-keeping from the store itself (an install from this node's own
	// offline package) must leave it intact.
	src := filepath.Join(packageDir, "cm4-tools.tar.gz")
	keepPackage(src, src, "manet_release_version.txt", "0.4.7")
	if got, _ := os.ReadFile(src); string(got) != string(data) {
		t.Fatal("package damaged by keeping it from the store")
	}
	if sig, _ := os.ReadFile(src + ".sig"); string(sig) != string(wantSig) {
		t.Errorf("signature lost re-keeping from the store: %q", sig)
	}
}
