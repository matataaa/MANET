package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// This file covers the fix for the terminal proxy hop breaking once
// require_auth=y went live fleet-wide (mattronix/MANET#36's rollout):
// handleTerminalProxy dials a target node's own /ws/terminal
// server-to-server, with no browser session to present. mintFleetPeerToken +
// verifyFleetPeerToken + hostMatchesLocalAddr + requireAuthOrPeerToken let
// that one hop authenticate using a short-lived, target-bound HMAC that
// every node in the fleet can independently derive from its own mesh.conf
// (admin_password + mesh_ssid, via the already-PBKDF2-hardened
// deriveFleetKey), without weakening requireAuth/isAuthed for any other
// route.
//
// TestFleetPeerTokenGatedOnRequireAuth is the single most important test in
// this file: it covers the "open relay" bug where a node provisioned with a
// real admin_password but require_auth=n (firstrun.sh.template's default,
// and EUD1's likely live state) would otherwise mint a fully valid token
// for an unauthenticated caller, turning that one open node into a relay
// into every require_auth=y node in the fleet.

// TestFleetPeerTokenGatedOnRequireAuth: admin_password and mesh_ssid both
// set, but require_auth=n -- mintFleetPeerToken must return "". This
// exercises the real minting function directly, not a mock.
func TestFleetPeerTokenGatedOnRequireAuth(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=n\n")
	if got := mintFleetPeerToken("some-target.local", FleetPeerAuthDomainTerminal); got != "" {
		t.Fatalf("expected empty token when require_auth=n, got %q -- this is the open-relay bug", got)
	}
}

// TestFleetPeerTokenGatedOnRequireAuthUnset: require_auth entirely absent
// from mesh.conf must be treated the same as require_auth=n (matches
// isAuthed's own default-open behavior).
func TestFleetPeerTokenGatedOnRequireAuthUnset(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\n")
	if got := mintFleetPeerToken("some-target.local", FleetPeerAuthDomainTerminal); got != "" {
		t.Fatalf("expected empty token when require_auth is unset, got %q", got)
	}
}

// TestFleetPeerTokenEmptyPasswordYieldsEmptyToken matches getPerfAuthToken's
// existing empty-password convention: an unprovisioned fleet (no
// admin_password set) must not accidentally authenticate everything via an
// empty-string comparison, even with require_auth=y.
func TestFleetPeerTokenEmptyPasswordYieldsEmptyToken(t *testing.T) {
	withTempMeshConf(t, "mesh_ssid=some-ssid\nrequire_auth=y\n")
	if got := mintFleetPeerToken("some-target.local", FleetPeerAuthDomainTerminal); got != "" {
		t.Fatalf("expected empty token when admin_password is unset, got %q", got)
	}
}

// TestFleetPeerTokenKeyDerivesFromHardenedFleetKey: the peer-token key must
// come from deriveFleetKey's already-PBKDF2-hardened (200000 iteration)
// fleet-crypto key, via one more HMAC with its own domain string -- NOT
// directly from admin_password|mesh_ssid. This fleet's peer TLS defaults to
// InsecureSkipVerify, so a captured token's key must not be a single
// unsalted HMAC key away from a GPU-speed offline attack on admin_password.
// This test would fail if someone "simplified" fleetPeerTokenKey back to
// hashing the password directly.
func TestFleetPeerTokenKeyDerivesFromHardenedFleetKey(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	key := fleetPeerTokenKey()
	if len(key) == 0 {
		t.Fatalf("expected a non-empty key when eligible")
	}

	naiveConcat := []byte("fleet-secret|fleet-mesh")
	if hmac.Equal(key, naiveConcat) {
		t.Fatalf("peer token key must not be the raw admin_password|mesh_ssid concatenation")
	}
	naiveHash := sha256.Sum256(naiveConcat)
	if hmac.Equal(key, naiveHash[:]) {
		t.Fatalf("peer token key must not be a direct hash of admin_password|mesh_ssid")
	}

	fk, err := deriveFleetKey("fleet-secret", "fleet-mesh")
	if err != nil {
		t.Fatalf("deriveFleetKey: %v", err)
	}
	m := hmac.New(sha256.New, fk)
	m.Write([]byte("manet-fleet-peer-terminal-key|v1"))
	want := m.Sum(nil)
	if !hmac.Equal(key, want) {
		t.Fatalf("peer token key must be HMAC(deriveFleetKey(admin_password, mesh_ssid), domain-string)")
	}
}

// TestMintAndVerifyFleetPeerTokenRoundTrip: a freshly minted token verifies
// against the exact target it was minted for.
func TestMintAndVerifyFleetPeerTokenRoundTrip(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	token := mintFleetPeerToken("eud2.local:8443", FleetPeerAuthDomainTerminal)
	if token == "" {
		t.Fatalf("expected a non-empty token when eligible")
	}
	if !verifyFleetPeerToken(token, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("expected a freshly minted token to verify against its own target")
	}
}

// TestVerifyFleetPeerTokenRejectsDifferentTarget: a token minted for one
// target must not verify against a different target/host -- this is what
// stops the token being replayed cross-target (e.g. against an
// attacker-controlled target= param).
func TestVerifyFleetPeerTokenRejectsDifferentTarget(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	token := mintFleetPeerToken("eud2.local:8443", FleetPeerAuthDomainTerminal)
	if token == "" {
		t.Fatalf("expected a non-empty token when eligible")
	}
	if verifyFleetPeerToken(token, "eud3.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("token minted for eud2 must not verify against eud3")
	}
}

// TestVerifyFleetPeerTokenRejectsWrongKey: a token minted under one
// admin_password/mesh_ssid must not verify against a different one -- this
// is what makes the token depend on the shared password rather than being
// a constant any node can forge.
func TestVerifyFleetPeerTokenRejectsWrongKey(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")
	token := mintFleetPeerToken("eud2.local:8443", FleetPeerAuthDomainTerminal)
	if token == "" {
		t.Fatalf("expected a non-empty token when eligible")
	}

	withTempMeshConf(t, "admin_password=different-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")
	if verifyFleetPeerToken(token, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("token minted under one admin_password must not verify under a different one")
	}
}

// TestVerifyFleetPeerTokenRejectsExpiredTimestamp: a token that is
// correctly signed but stale (older than fleetPeerTokenMaxSkew) must be
// rejected. Uses mintFleetPeerTokenAt directly so the token is genuinely,
// validly signed -- this proves the expiry check itself is doing the
// rejecting, not just signature validation.
func TestVerifyFleetPeerTokenRejectsExpiredTimestamp(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	staleTS := time.Now().Add(-2 * fleetPeerTokenMaxSkew).Unix()
	token := mintFleetPeerTokenAt(staleTS, "eud2.local:8443", FleetPeerAuthDomainTerminal)
	if token == "" {
		t.Fatalf("expected a non-empty token when eligible")
	}
	if verifyFleetPeerToken(token, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("expected a token stale by 2x the max skew to be rejected")
	}

	freshTS := time.Now().Unix()
	freshToken := mintFleetPeerTokenAt(freshTS, "eud2.local:8443", FleetPeerAuthDomainTerminal)
	if !verifyFleetPeerToken(freshToken, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("expected a freshly-timestamped token to verify (sanity check on the skew comparison itself)")
	}
}

// TestVerifyFleetPeerTokenRejectsOverflowEdgeTimestamp: the original
// negate-and-compare skew check ("skew := now - ts; if skew < 0 { skew =
// -skew }") had an integer overflow edge at ts = math.MinInt64 (now - ts
// overflows int64). The replacement direct-range comparison
// (ts < now-maxSkew || ts > now+maxSkew) must reject this value on its own
// -- not merely rely on "the MAC would fail anyway" -- so this mints a
// genuinely, validly-signed token for that exact timestamp and confirms
// verification still fails.
func TestVerifyFleetPeerTokenRejectsOverflowEdgeTimestamp(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	token := mintFleetPeerTokenAt(math.MinInt64, "eud2.local:8443", FleetPeerAuthDomainTerminal)
	if token == "" {
		t.Fatalf("expected a non-empty token when eligible")
	}
	if verifyFleetPeerToken(token, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
		t.Fatalf("expected the math.MinInt64 timestamp edge case to be rejected, not accepted via overflow")
	}
}

// TestVerifyFleetPeerTokenRejectsMalformedInput covers empty tokens,
// tokens with no separator, and non-numeric timestamps -- none of these
// should ever reach the HMAC comparison.
func TestVerifyFleetPeerTokenRejectsMalformedInput(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	for _, bad := range []string{"", "no-separator-at-all", "not-a-number|deadbeef", "123|not-hex-zz"} {
		if verifyFleetPeerToken(bad, "eud2.local:8443", FleetPeerAuthDomainTerminal) {
			t.Fatalf("expected malformed token %q to be rejected", bad)
		}
	}
}

// TestHostMatchesLocalAddr covers the actual enforcement behind the fleet
// peer token's target binding: r.Host is sender-controlled and otherwise
// unverified, so hostMatchesLocalAddr must independently confirm it names
// one of this node's own addresses (loopback counts, since the test
// process's own "local addresses" are all it can assert about itself).
// Bogus/external addresses and bare hostnames must not match.
func TestHostMatchesLocalAddr(t *testing.T) {
	cases := []struct {
		name     string
		hostport string
		want     bool
	}{
		{"loopback_v4_no_port", "127.0.0.1", true},
		{"loopback_v4_with_port", "127.0.0.1:8443", true},
		{"loopback_v6_no_port", "::1", true},
		{"loopback_v6_bracketed_no_port", "[::1]", true},
		{"loopback_v6_bracketed_with_port", "[::1]:8443", true},
		{"bogus_external_ip", "203.0.113.1", false},
		{"bogus_external_ip_with_port", "203.0.113.1:8443", false},
		{"hostname_not_ip", "attacker.example.com", false},
		{"hostname_not_ip_with_port", "attacker.example.com:8443", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hostMatchesLocalAddr(tc.hostport); got != tc.want {
				t.Fatalf("hostMatchesLocalAddr(%q) = %v, want %v", tc.hostport, got, tc.want)
			}
		})
	}
}

// TestRequireAuthOrPeerTokenAcceptsValidPeerHeader: the proxy hop's whole
// point -- a request carrying a correctly target-bound
// X-Manet-Fleet-Peer-Auth header, with no session cookie at all, must be
// let through when r.Host both matches one of this node's own addresses
// AND matches the token's signed target.
func TestRequireAuthOrPeerTokenAcceptsValidPeerHeader(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	called := false
	handler := requireAuthOrPeerToken(FleetPeerAuthDomainTerminal)(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws/terminal?target=eud2", nil)
	req.Host = "127.0.0.1:8443"
	req.Header.Set(FleetPeerAuthHeader, mintFleetPeerToken("127.0.0.1:8443", FleetPeerAuthDomainTerminal))
	rr := httptest.NewRecorder()

	handler(rr, req)

	if !called {
		t.Fatalf("expected the wrapped handler to run for a valid peer-token header, got status %d", rr.Code)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

// TestRequireAuthOrPeerTokenRejectsInvalidOrMissing is the single most
// important test for the wrapper itself: it proves requireAuthOrPeerToken
// has NOT been accidentally turned into an unauthenticated bypass. A
// request with no cookie and a missing, empty, malformed, wrong-target,
// expired, or spoofed-non-local-Host peer-token header must be rejected,
// and the wrapped handler must never run.
func TestRequireAuthOrPeerTokenRejectsInvalidOrMissing(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	const localHost = "127.0.0.1:8443"
	staleToken := mintFleetPeerTokenAt(time.Now().Add(-2*fleetPeerTokenMaxSkew).Unix(), localHost, FleetPeerAuthDomainTerminal)
	wrongTargetToken := mintFleetPeerToken("127.0.0.1:9999", FleetPeerAuthDomainTerminal)
	// A token correctly signed for a non-local Host: proves that even a
	// perfectly valid signature is rejected if r.Host doesn't actually name
	// one of this node's own addresses (the sender's Host header is
	// otherwise self-reported and unverified).
	spoofedHostToken := mintFleetPeerToken("attacker.example.com", FleetPeerAuthDomainTerminal)

	cases := []struct {
		name      string
		host      string
		headerVal string
		setHeader bool
	}{
		{"missing_header", localHost, "", false},
		{"empty_header", localHost, "", true},
		{"malformed_token", localHost, "not-a-valid-token", true},
		{"stale_token", localHost, staleToken, true},
		{"wrong_target_token", localHost, wrongTargetToken, true},
		{"spoofed_non_local_host", "attacker.example.com", spoofedHostToken, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := requireAuthOrPeerToken(FleetPeerAuthDomainTerminal)(func(w http.ResponseWriter, r *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/ws/terminal?target=eud2", nil)
			req.Host = tc.host
			if tc.setHeader {
				req.Header.Set(FleetPeerAuthHeader, tc.headerVal)
			}
			rr := httptest.NewRecorder()

			handler(rr, req)

			if called {
				t.Fatalf("wrapped handler must not run without a valid cookie or peer token")
			}
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestRequireAuthOrPeerTokenStillAcceptsValidCookie: the original
// requireAuth path (a real browser session) must keep working unchanged
// for this route.
func TestRequireAuthOrPeerTokenStillAcceptsValidCookie(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\n")

	called := false
	handler := requireAuthOrPeerToken(FleetPeerAuthDomainTerminal)(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	token, err := sessions.create("fleet-secret")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/ws/terminal", nil)
	req.AddCookie(&http.Cookie{Name: PerfAuthCookie, Value: token})
	rr := httptest.NewRecorder()

	handler(rr, req)

	if !called {
		t.Fatalf("expected the wrapped handler to run for a valid session cookie, got status %d", rr.Code)
	}
}

// TestRequireAuthOrPeerTokenOnOpenNodeStillRequiresCookie is the direct
// regression test for the critical open-relay bug: on a node with
// require_auth=n but a real admin_password set, mintFleetPeerToken must
// never produce a usable token -- there is no way for such a node to relay
// an unauthenticated caller into a locked-down peer.
func TestRequireAuthOrPeerTokenOnOpenNodeStillRequiresCookie(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=n\n")

	if got := mintFleetPeerToken("locked-node.local", FleetPeerAuthDomainTerminal); got != "" {
		t.Fatalf("an open node (require_auth=n) must never mint a usable peer token, got %q", got)
	}
}
