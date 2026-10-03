package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIToken(t *testing.T) {
	orig := CLITokenFile
	CLITokenFile = filepath.Join(t.TempDir(), "run", "cli-token")
	t.Cleanup(func() { CLITokenFile = orig; cliToken = "" })
	withTempMeshConf(t, "require_auth=y\nadmin_password=pw\n")

	if err := initCLIToken(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(CLITokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("token file mode %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(CLITokenFile)
	token := strings.TrimSpace(string(data))

	req := func(remote, tok string) bool {
		r := httptest.NewRequest("POST", "/api/admin/save", nil)
		r.RemoteAddr = remote
		if tok != "" {
			r.Header.Set(CLITokenHeader, tok)
		}
		_, authed := authState(r)
		return authed
	}
	if !req("127.0.0.1:50000", token) {
		t.Error("loopback request with the token was refused")
	}
	if !req("[::1]:50000", token) {
		t.Error("IPv6 loopback request with the token was refused")
	}
	if req("127.0.0.1:50000", token+"x") {
		t.Error("wrong token accepted")
	}
	if req("10.30.2.7:50000", token) {
		t.Error("token accepted from a non-loopback address")
	}
	if req("127.0.0.1:50000", "") {
		t.Error("loopback request without a token accepted")
	}
}
