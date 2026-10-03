package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOriginGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := originGuard(ok)

	cases := []struct {
		name     string
		method   string
		host     string
		origin   string
		upgrade  bool
		wantCode int
	}{
		{"same-origin POST", "POST", "10.30.2.5", "https://10.30.2.5", false, 200},
		{"same-origin POST explicit port", "POST", "10.30.2.5:443", "https://10.30.2.5", false, 200},
		{"no Origin (curl, CLI, app)", "POST", "10.30.2.5", "", false, 200},
		{"cross-site POST", "POST", "10.30.2.5", "https://evil.example", false, 403},
		{"opaque null origin", "POST", "10.30.2.5", "null", false, 403},
		{"other port on same node", "POST", "10.30.2.5", "http://10.30.2.5:9800", false, 403},
		{"cross-site GET is a read", "GET", "10.30.2.5", "https://evil.example", false, 200},
		{"cross-site websocket", "GET", "10.30.2.5", "https://evil.example", true, 403},
		{"same-origin websocket", "GET", "chat.mesh", "https://chat.mesh", true, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://"+tc.host+"/api/terminal/exec", nil)
			r.Host = tc.host
			r.TLS = &tls.ConnectionState{}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.upgrade {
				r.Header.Set("Upgrade", "websocket")
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}
