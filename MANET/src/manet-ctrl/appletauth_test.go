package main

import (
	"net/http/httptest"
	"testing"
)

// An applet backend is a root process; only applets that mark themselves
// public may be driven through /api/applets/<name>/proxy without a login.
func TestAppletBackendAllowed(t *testing.T) {
	cases := []struct {
		name     string
		conf     string
		public   bool
		want     bool
		wantCode int
	}{
		{"locked node, private applet", "require_auth=y\nadmin_password=pw\n", false, false, 401},
		{"locked node, public applet", "require_auth=y\nadmin_password=pw\n", true, true, 200},
		{"open node, private applet", "require_auth=n\nadmin_password=pw\n", false, true, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempMeshConf(t, tc.conf)
			m := &appletManifest{}
			m.Backend.Public = tc.public
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/api/applets/mesh-wireguard/proxy/config", nil)
			if got := appletBackendAllowed(w, r, m); got != tc.want {
				t.Fatalf("appletBackendAllowed = %v, want %v", got, tc.want)
			}
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}
