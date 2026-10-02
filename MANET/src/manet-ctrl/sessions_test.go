package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func withFreshSessions(t *testing.T) {
	t.Helper()
	orig := sessions
	sessions = &sessionStore{sessions: make(map[[32]byte]session)}
	t.Cleanup(func() { sessions = orig })
}

func login(t *testing.T, password, remoteAddr string, existing *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/perf-auth", strings.NewReader(`{"password":"`+password+`"}`))
	req.RemoteAddr = remoteAddr
	if existing != nil {
		req.AddCookie(existing)
	}
	rr := httptest.NewRecorder()
	apiPerfAuth(rr, req)
	return rr
}

func sessionCookie(t *testing.T, rr *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rr.Result().Cookies() {
		if c.Name == PerfAuthCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in response (status %d): %s", PerfAuthCookie, rr.Code, rr.Body.String())
	return nil
}

func authedWith(c *http.Cookie) bool {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/status", nil)
	if c != nil {
		req.AddCookie(c)
	}
	return isAuthed(req)
}

const authConf = "admin_password=s3cret\nrequire_auth=y\n"

func TestLoginIssuesDistinctHttpOnlySessions(t *testing.T) {
	withTempMeshConf(t, authConf)
	withFreshSessions(t)

	rr := login(t, "s3cret", "10.0.0.1:1000", nil)
	if cc := rr.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("login response Cache-Control = %q, want no-store", cc)
	}
	a := sessionCookie(t, rr)
	b := sessionCookie(t, login(t, "s3cret", "10.0.0.2:1000", nil))
	if a.Value == b.Value {
		t.Fatalf("two logins produced the same token")
	}
	if !a.HttpOnly || a.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie must be HttpOnly + SameSite=Strict, got HttpOnly=%v SameSite=%v", a.HttpOnly, a.SameSite)
	}
	if a.MaxAge != int(sessionLifetime.Seconds()) {
		t.Fatalf("cookie MaxAge = %d, want %d", a.MaxAge, int(sessionLifetime.Seconds()))
	}
	if !authedWith(a) || !authedWith(b) {
		t.Fatalf("both sessions should be valid")
	}
	if authedWith(&http.Cookie{Name: PerfAuthCookie, Value: "forged"}) || authedWith(nil) {
		t.Fatalf("forged or missing cookie must not authenticate")
	}
}

func TestLogoutRevokesOnlyThatSession(t *testing.T) {
	withTempMeshConf(t, authConf)
	withFreshSessions(t)

	a := sessionCookie(t, login(t, "s3cret", "10.0.0.1:1000", nil))
	b := sessionCookie(t, login(t, "s3cret", "10.0.0.2:1000", nil))

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(a)
	rr := httptest.NewRecorder()
	apiLogout(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("logout: %d %s", rr.Code, rr.Body.String())
	}
	if c := sessionCookie(t, rr); c.MaxAge >= 0 {
		t.Fatalf("logout must expire the cookie, got MaxAge=%d", c.MaxAge)
	}
	if authedWith(a) {
		t.Fatalf("logged-out session is still valid")
	}
	if !authedWith(b) {
		t.Fatalf("logout must not end other sessions")
	}
}

func TestReloginReplacesBrowserSession(t *testing.T) {
	withTempMeshConf(t, authConf)
	withFreshSessions(t)

	old := sessionCookie(t, login(t, "s3cret", "10.0.0.1:1000", nil))
	fresh := sessionCookie(t, login(t, "s3cret", "10.0.0.1:1000", old))
	if authedWith(old) || !authedWith(fresh) {
		t.Fatalf("re-login must revoke the old token and issue a working new one")
	}
}

func TestPasswordChangeEndsSessions(t *testing.T) {
	withTempMeshConf(t, authConf)
	withFreshSessions(t)

	c := sessionCookie(t, login(t, "s3cret", "10.0.0.1:1000", nil))
	if err := saveKVFile(MeshConfFile, map[string]string{"admin_password": "rotated"}); err != nil {
		t.Fatal(err)
	}
	if authedWith(c) {
		t.Fatalf("session issued under the old password is still valid")
	}
}

func TestSessionExpires(t *testing.T) {
	withTempMeshConf(t, authConf)
	withFreshSessions(t)

	c := sessionCookie(t, login(t, "s3cret", "10.0.0.1:1000", nil))
	for k, s := range sessions.sessions {
		s.created = time.Now().Add(-sessionLifetime - time.Second)
		sessions.sessions[k] = s
	}
	if authedWith(c) {
		t.Fatalf("expired session is still valid")
	}
}

func TestSessionStoreEvictsOldest(t *testing.T) {
	withFreshSessions(t)

	first, _ := sessions.create("pw")
	var last string
	for i := 0; i < maxSessions; i++ {
		last, _ = sessions.create("pw")
	}
	if len(sessions.sessions) != maxSessions {
		t.Fatalf("store holds %d sessions, want %d", len(sessions.sessions), maxSessions)
	}
	if sessions.valid(first, "pw") || !sessions.valid(last, "pw") {
		t.Fatalf("expected the oldest session evicted and the newest kept")
	}
}

func TestLoginThrottling(t *testing.T) {
	withTempMeshConf(t, authConf)
	withFreshSessions(t)

	for i := 0; i < loginFailsPerAddr; i++ {
		if rr := login(t, "wrong", "10.0.0.9:1000", nil); rr.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, rr.Code)
		}
	}
	rr := login(t, "s3cret", "10.0.0.9:2000", nil)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 after %d failures (even with the right password), got %d", loginFailsPerAddr, rr.Code)
	}
	if ra, err := strconv.Atoi(rr.Header().Get("Retry-After")); err != nil || ra < 1 || ra > int(loginWindow.Seconds()) {
		t.Fatalf("bad Retry-After %q", rr.Header().Get("Retry-After"))
	}
	if rr := login(t, "s3cret", "10.0.0.10:1000", nil); rr.Code != http.StatusOK {
		t.Fatalf("another client must still be able to log in, got %d", rr.Code)
	}

	// Node-wide limit: failures spread across many addresses.
	for i := 0; len(sessions.failures) < loginFailsPerNode; i++ {
		login(t, "wrong", "10.1.0."+strconv.Itoa(i)+":1000", nil)
	}
	if rr := login(t, "s3cret", "10.2.0.1:1000", nil); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429 once the node-wide limit is reached, got %d", rr.Code)
	}
	if len(sessions.failures) > loginFailsPerNode {
		t.Fatalf("failure list grew past its bound: %d", len(sessions.failures))
	}
}

func TestOpenNodeNeedsNoSession(t *testing.T) {
	withTempMeshConf(t, "admin_password=s3cret\nrequire_auth=n\n")
	withFreshSessions(t)
	if !authedWith(nil) {
		t.Fatalf("require_auth=n must stay open")
	}
}
