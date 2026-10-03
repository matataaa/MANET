package main

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Cross-site request forgery guard. Login state alone can't stop a web
// page an EUD user has open elsewhere from posting to this node: with
// require_auth=n there is no login at all, and the API reads a JSON body
// whatever its Content-Type, so a plain cross-site form/fetch POST would
// reach /api/terminal/exec. Browsers send an Origin header on every
// cross-origin POST/PUT/DELETE and on every websocket handshake; when one
// is present it must name this very scheme://host:port. Requests without
// Origin (curl, the mesh CLI, the Android app, node-to-node proxy hops)
// are not browser-forged and pass.

// sameOrigin reports whether r carries no Origin header or one matching
// the host it was sent to. Ports are compared too, defaults filled in: a
// page served from another port of this node (e.g. an applet backend's own
// listener) is a different origin.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false // includes the opaque "null" origin
	}
	reqScheme := "http"
	if r.TLS != nil {
		reqScheme = "https"
	}
	return strings.EqualFold(u.Scheme, reqScheme) &&
		strings.EqualFold(withDefaultPort(u.Host, u.Scheme), withDefaultPort(r.Host, reqScheme))
}

func withDefaultPort(host, scheme string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	port := "80"
	if strings.EqualFold(scheme, "https") {
		port = "443"
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), port)
}

// originGuard rejects cross-origin state-changing requests and websocket
// handshakes; plain reads (GET/HEAD/OPTIONS) can't be forged into actions.
func originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isRead := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		isUpgrade := strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
		if (!isRead || isUpgrade) && !sameOrigin(r) {
			writeJSON(w, 403, map[string]interface{}{"ok": false, "error": "Cross-origin request refused"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
