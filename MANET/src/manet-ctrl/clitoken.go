package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

// The mesh CLI runs on the node itself and calls this API over loopback.
// It authenticates with a random token manet-ctrl writes at startup to a
// root-only file, instead of logging in with the admin password: being
// able to read the file already means root on the node, and it creates no
// session, so scripted CLI use can't evict browser logins from the
// maxSessions table. A restart issues a new token.
const CLITokenHeader = "X-Manet-CLI-Token"

var (
	CLITokenFile = "/run/manet-ctrl/cli-token"
	cliToken     string
)

// initCLIToken creates a fresh token and publishes it to CLITokenFile
// (0600, root). On failure the CLI just can't authenticate; the web UI is
// unaffected.
func initCLIToken() error {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return err
	}
	token := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(filepath.Dir(CLITokenFile), 0700); err != nil {
		return err
	}
	tmp := CLITokenFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(token+"\n"), 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, CLITokenFile); err != nil {
		return err
	}
	cliToken = token
	return nil
}

// cliTokenAuthenticated accepts the CLI token, and only on a loopback
// connection: it is never meant to leave the node.
func cliTokenAuthenticated(r *http.Request) bool {
	token := r.Header.Get(CLITokenHeader)
	if token == "" || cliToken == "" {
		return false
	}
	ip := net.ParseIP(clientAddr(r))
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(cliToken)) == 1
}
