package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file covers the fix for /ws/logs: unlike every other sensitive route
// in this file, it had ZERO authentication, and streamed an arbitrary
// caller-controlled file= path via "tail -f" run as root -- trivially
// leaking /etc/mesh.conf's admin_password/mesh_key to anyone who could reach
// the node's HTTPS port. The fix reuses #40's fleet-peer-token mechanism
// (generalized to take a domain string) for the proxy hop, and adds a
// file-path allowlist plus lines= validation to handleLogs itself.
//
// TestFleetPeerTokenDomainsAreNotInterchangeable is the single most
// important test in this file: a token minted for one route's domain must
// not verify for a different route's domain, even against the identical
// target and within the same freshness window -- otherwise a captured
// /ws/terminal proxy token could be replayed against /ws/logs (or vice
// versa).

// TestFleetPeerTokenDomainsAreNotInterchangeable: a token minted with the
// logs domain must not verify against the terminal domain, and a token
// minted with the terminal domain must not verify against the logs domain.
func TestFleetPeerTokenDomainsAreNotInterchangeable(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	logsToken := mintFleetPeerToken("eud2.local:8443", FleetPeerAuthDomainLogs)
	if logsToken == "" {
		t.Fatalf("expected a non-empty logs-domain token when eligible")
	}
	if verifyFleetPeerToken(logsToken, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("a token minted for the logs domain must not verify against the terminal domain")
	}
	if !verifyFleetPeerToken(logsToken, "eud2.local:8443", FleetPeerAuthDomainLogs) {
		t.Fatalf("sanity check: a logs-domain token must still verify against its own domain")
	}

	terminalToken := mintFleetPeerToken("eud2.local:8443", FleetPeerAuthDomainTerminal)
	if terminalToken == "" {
		t.Fatalf("expected a non-empty terminal-domain token when eligible")
	}
	if verifyFleetPeerToken(terminalToken, "eud2.local:8443", FleetPeerAuthDomainLogs) {
		t.Fatalf("a token minted for the terminal domain must not verify against the logs domain")
	}
	if !verifyFleetPeerToken(terminalToken, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("sanity check: a terminal-domain token must still verify against its own domain")
	}
}

// TestFleetPeerLogsTokenGatedOnRequireAuth mirrors the terminal open-relay
// regression test for the logs domain: it's the same underlying
// fleetPeerTokenKey() gating (require_auth must be genuinely on), so a node
// with require_auth=n must never mint a usable logs peer-token either.
func TestFleetPeerLogsTokenGatedOnRequireAuth(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=n\n")
	if got := mintFleetPeerToken("some-target.local", FleetPeerAuthDomainLogs); got != "" {
		t.Fatalf("expected empty logs-domain token when require_auth=n, got %q -- this is the open-relay bug", got)
	}
}

// TestRequireAuthOrPeerTokenLogsDomainRejectsTerminalToken: the /ws/logs
// route wrapper (requireAuthOrPeerToken(FleetPeerAuthDomainLogs)) must
// reject a token minted for the terminal domain, even though it's otherwise
// a validly-signed, fresh, correctly-target-bound token.
func TestRequireAuthOrPeerTokenLogsDomainRejectsTerminalToken(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	called := false
	handler := requireAuthOrPeerToken(FleetPeerAuthDomainLogs)(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws/logs?target=eud2", nil)
	req.Host = "127.0.0.1:8443"
	req.Header.Set(FleetPeerAuthHeader, mintFleetPeerToken("127.0.0.1:8443", FleetPeerAuthDomainTerminal))
	rr := httptest.NewRecorder()

	handler(rr, req)

	if called {
		t.Fatalf("a terminal-domain token must not authenticate the logs route")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestRequireAuthOrPeerTokenLogsDomainAcceptsOwnToken: sanity check that the
// logs route wrapper still accepts a correctly-minted logs-domain token,
// same shape as #40's terminal equivalent.
func TestRequireAuthOrPeerTokenLogsDomainAcceptsOwnToken(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	called := false
	handler := requireAuthOrPeerToken(FleetPeerAuthDomainLogs)(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws/logs?target=eud2", nil)
	req.Host = "127.0.0.1:8443"
	req.Header.Set(FleetPeerAuthHeader, mintFleetPeerToken("127.0.0.1:8443", FleetPeerAuthDomainLogs))
	rr := httptest.NewRecorder()

	handler(rr, req)

	if !called {
		t.Fatalf("expected the wrapped handler to run for a valid logs-domain peer token, got status %d", rr.Code)
	}
}

// TestValidateLogFileAllowlist covers the file= path allowlist:
// /var/log/... paths pass (after filepath.Clean), anything outside
// /var/log/ is rejected, and -- critically -- a "/var/log/../etc/..."
// traversal attempt is rejected because filepath.Clean collapses the ".."
// lexically before the prefix check runs, so the check operates on the
// collapsed "/etc/..." path, not the raw string.
func TestValidateLogFileAllowlist(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"allowed_plain_syslog", "/var/log/syslog", false},
		{"allowed_nested_path", "/var/log/radio-setup.log", false},
		{"allowed_root_itself", "/var/log", false},
		{"allowed_root_with_slash", "/var/log/", false},
		{"rejected_etc_mesh_conf", "/etc/mesh.conf", true},
		{"rejected_traversal_out_of_var_log", "/var/log/../etc/mesh.conf", true},
		{"rejected_traversal_deeper", "/var/log/sub/../../etc/mesh.conf", true},
		{"rejected_lookalike_prefix", "/var/log-fake/evil", true},
		{"rejected_relative_path", "etc/mesh.conf", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateLogFile(tc.in)
			if tc.wantErr && err == nil {
				t.Fatalf("validateLogFile(%q) = %q, <nil>; expected an error", tc.in, got)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateLogFile(%q) unexpected error: %v", tc.in, err)
			}
		})
	}
}

// TestValidateLogLines covers lines= validation: non-numeric, negative,
// zero, and an absurdly large value must all be rejected (this
// implementation rejects rather than silently clamping, so a caller gets a
// clear error instead of a surprising truncated value); a normal value and
// the empty-string default must both be accepted.
func TestValidateLogLines(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{"empty_uses_default", "", logsDefaultLines, false},
		{"normal_value", "50", 50, false},
		{"non_numeric", "abc", 0, true},
		{"negative", "-5", 0, true},
		{"zero", "0", 0, true},
		{"absurdly_large", "999999999", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateLogLines(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateLogLines(%q) = %d, <nil>; expected an error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateLogLines(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("validateLogLines(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
