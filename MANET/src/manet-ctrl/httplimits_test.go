package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadBodyRejectsOversizedBody(t *testing.T) {
	pad := strings.Repeat("x", maxJSONBody)
	req := httptest.NewRequest(http.MethodPost, "/api/perf-auth", strings.NewReader(`{"password":"`+pad+`"}`))
	if m := readBody(req); len(m) != 0 {
		t.Fatalf("oversized body was parsed: %d keys", len(m))
	}

	req = httptest.NewRequest(http.MethodPost, "/api/perf-auth", strings.NewReader(`{"password":"ok"}`))
	if m := readBody(req); m["password"] != "ok" {
		t.Fatalf("normal body not parsed: %v", m)
	}
}

func TestNewServerBoundsHeadersAndIdleOnly(t *testing.T) {
	s := newServer(":0", http.NotFoundHandler())
	if s.ReadHeaderTimeout <= 0 || s.IdleTimeout <= 0 {
		t.Fatalf("header/idle timeouts not set: %+v", s)
	}
	if s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Fatalf("Read/WriteTimeout would cut off streaming responses and uploads")
	}
}
