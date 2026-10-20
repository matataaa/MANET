package main

import (
	"strings"
	"testing"
)

func TestValidAppletName(t *testing.T) {
	for _, name := range []string{"atak-overlay", "mesh-chat", "voice_echo", "A1", strings.Repeat("a", 64)} {
		if !validAppletName(name) {
			t.Errorf("rejected valid name %q", name)
		}
	}
	for _, name := range []string{"", "-x", "../x", "a/b", "x')alert(1)//", "x\"y", "a b", "a.b", strings.Repeat("a", 65)} {
		if validAppletName(name) {
			t.Errorf("accepted invalid name %q", name)
		}
	}
}
