package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The voice and QoS routes are registered without requireAuth because their
// GETs feed the UI and, since e9a822d, PTT, volume and channel selection are
// EUD actions (the Android app has no login). Everything else these handlers
// change must still be refused without a login on a locked node.
func TestVoiceQoSStateChangesNeedLogin(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		path    string
		body    string
	}{
		{"qos enable", apiQoS, "/api/qos", `{"enabled":false}`},
		{"qos band", apiQoS, "/api/qos", `{"service":"voice","band":2}`},
		{"voice stop", apiVoice, "/api/voice", `{"action":"stop"}`},
		{"voice restart", apiVoice, "/api/voice", `{"action":"restart"}`},
		{"voice config", apiVoice, "/api/voice", `{"ptt_mode":"vox"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTempMeshConf(t, "require_auth=y\nadmin_password=pw\n")
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			tc.handler(w, r)
			if w.Code != 401 {
				t.Fatalf("status = %d, want 401", w.Code)
			}
		})
	}
}
