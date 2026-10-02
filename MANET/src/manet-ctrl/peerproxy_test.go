package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsMeshPeerIP(t *testing.T) {
	conf := map[string]string{"ipv4_network": "10.30.2.0/24"}
	cases := map[string]bool{
		"10.30.2.186":   true,
		"10.30.2.1":     true,
		"10.30.3.5":     false, // outside the mesh
		"192.168.1.1":   false, // e.g. the gateway's uplink LAN
		"8.8.8.8":       false,
		"127.0.0.1":     false,
		"0.0.0.0":       false,
		"224.0.0.1":     false,
		"fe80::1":       false,
		"not-an-ip":     false,
		"10.30.2.186:1": false,
	}
	for ip, want := range cases {
		if got := isMeshPeerIP(ip, conf); got != want {
			t.Errorf("isMeshPeerIP(%q) = %v, want %v", ip, got, want)
		}
	}
	// Older images wrote ipv4_network without a prefix length.
	if !isMeshPeerIP("10.30.2.90", map[string]string{"ipv4_network": "10.30.2.0"}) {
		t.Errorf("bare ipv4_network must be treated as /24")
	}
}

func TestAPIPeerRejectsNonMeshTargetsAndPaths(t *testing.T) {
	withTempMeshConf(t, "ipv4_network=10.30.2.0/24\n")
	cases := []struct {
		path string
		want int
	}{
		{"/api/peer/192.168.1.1/api/version", http.StatusForbidden},
		{"/api/peer/8.8.8.8", http.StatusForbidden},
		{"/api/peer/127.0.0.1/api/version", http.StatusForbidden},
		{"/api/peer/10.30.2.186/index.html", http.StatusBadRequest},
		{"/api/peer/10.30.2.186/../etc", http.StatusBadRequest},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		apiPeer(rr, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rr.Code != c.want {
			t.Errorf("%s -> %d, want %d (%s)", c.path, rr.Code, c.want, rr.Body.String())
		}
	}
}

func TestPeerProxyURLKeepsQuery(t *testing.T) {
	if got := peerProxyURL("10.30.2.186", "/api/halow/channels", "domain=EU"); got != "https://10.30.2.186/api/halow/channels?domain=EU" {
		t.Fatalf("query not forwarded: %s", got)
	}
	if got := peerProxyURL("10.30.2.186", "/api/version", ""); got != "https://10.30.2.186/api/version" {
		t.Fatalf("unexpected URL: %s", got)
	}
}

func TestCopyPeerHeadersDropsSetCookie(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Add("Set-Cookie", "manet_perf_auth=peer-token; Path=/")
	dst := http.Header{}
	copyPeerHeaders(dst, src)
	if dst.Get("Set-Cookie") != "" {
		t.Fatalf("peer Set-Cookie must not be passed through")
	}
	if dst.Get("Content-Type") != "application/json" {
		t.Fatalf("other headers must be copied")
	}
}

const fleetAuthConf = "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=y\nipv4_network=10.30.2.0/24\n"

func proxiedRequest(path, host, token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	if token != "" {
		req.Header.Set(FleetPeerAuthHeader, token)
	}
	return req
}

func TestProxiedAPITokenAuthenticatesOnlyAPIRequestsToThisNode(t *testing.T) {
	withTempMeshConf(t, fleetAuthConf)
	withFreshSessions(t)

	apiToken := mintFleetPeerToken("127.0.0.1", FleetPeerAuthDomainAPI)
	if apiToken == "" {
		t.Fatal("expected a token on a require_auth=y node")
	}
	if !isAuthed(proxiedRequest("/api/admin/preferences", "127.0.0.1", apiToken)) {
		t.Fatalf("a valid API-domain token must authenticate an /api/ request")
	}
	if isAuthed(proxiedRequest("/api/admin/preferences", "127.0.0.1", "")) {
		t.Fatalf("no token, no session: must not be authenticated")
	}
	termToken := mintFleetPeerToken("127.0.0.1", FleetPeerAuthDomainTerminal)
	if isAuthed(proxiedRequest("/api/admin/preferences", "127.0.0.1", termToken)) {
		t.Fatalf("a terminal-domain token must not authenticate API requests")
	}
	if isAuthed(proxiedRequest("/ws/terminal", "127.0.0.1", apiToken)) {
		t.Fatalf("an API-domain token must not authenticate non-/api/ routes")
	}
	foreign := mintFleetPeerToken("10.30.2.250", FleetPeerAuthDomainAPI)
	if isAuthed(proxiedRequest("/api/admin/preferences", "10.30.2.250", foreign)) {
		t.Fatalf("a token bound to a host that isn't this node must be rejected")
	}
}

func TestPeerProxyAuthTokenRequiresLocalSession(t *testing.T) {
	withTempMeshConf(t, fleetAuthConf)
	withFreshSessions(t)

	anon := httptest.NewRequest(http.MethodPost, "/api/peer/10.30.2.186/api/admin/save", nil)
	if tok := peerProxyAuthToken(anon, "10.30.2.186"); tok != "" {
		t.Fatalf("an unauthenticated request must not get a peer token")
	}

	session, err := sessions.create("fleet-secret")
	if err != nil {
		t.Fatal(err)
	}
	authed := httptest.NewRequest(http.MethodPost, "/api/peer/10.30.2.186/api/admin/save", nil)
	authed.AddCookie(&http.Cookie{Name: PerfAuthCookie, Value: session})
	tok := peerProxyAuthToken(authed, "10.30.2.186")
	if tok == "" || !verifyFleetPeerToken(tok, "10.30.2.186", FleetPeerAuthDomainAPI) {
		t.Fatalf("a logged-in request must get a token bound to the peer and the API domain, got %q", tok)
	}

	relayed := httptest.NewRequest(http.MethodPost, "/api/peer/10.30.2.186/api/admin/save", nil)
	relayed.Host = "127.0.0.1"
	relayed.Header.Set(FleetPeerAuthHeader, mintFleetPeerToken("127.0.0.1", FleetPeerAuthDomainAPI))
	if tok := peerProxyAuthToken(relayed, "10.30.2.186"); tok != "" {
		t.Fatalf("a request authenticated only by a relayed token must not mint a new one")
	}
}

func TestPeerProxyAuthTokenNeverMintedOnOpenNode(t *testing.T) {
	withTempMeshConf(t, "admin_password=fleet-secret\nmesh_ssid=fleet-mesh\nrequire_auth=n\n")
	withFreshSessions(t)
	session, _ := sessions.create("fleet-secret")
	req := httptest.NewRequest(http.MethodPost, "/api/peer/10.30.2.186/api/admin/save", nil)
	req.AddCookie(&http.Cookie{Name: PerfAuthCookie, Value: session})
	if tok := peerProxyAuthToken(req, "10.30.2.186"); tok != "" {
		t.Fatalf("an open node (require_auth=n) must never relay a login, got %q", tok)
	}
}

func TestAPIPeerRejectsChaining(t *testing.T) {
	withTempMeshConf(t, "ipv4_network=10.30.2.0/24\n")
	rr := httptest.NewRecorder()
	apiPeer(rr, httptest.NewRequest(http.MethodGet, "/api/peer/10.30.2.186/api/peer/10.30.2.78/api/version", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("chained proxy request -> %d, want 400", rr.Code)
	}
}
