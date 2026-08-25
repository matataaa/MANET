package main

import (
	"math"
	"testing"
)

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in   string
		want [3]int
		ok   bool
	}{
		{"1.4.0", [3]int{1, 4, 0}, true},
		{"0.0.1", [3]int{0, 0, 1}, true},
		{" 2.10.3 \n", [3]int{2, 10, 3}, true},
		{"1.4", [3]int{}, false},
		{"1.4.0.1", [3]int{}, false},
		{"", [3]int{}, false},
		{"a.b.c", [3]int{}, false},
		{"1.x.0", [3]int{}, false},
		// A dirty/pre-release-suffixed tag (e.g. accidentally cut from
		// "release-v0.0.1-test" instead of a clean "release-v0.0.1") must
		// fail to parse rather than silently truncate — see checkAndUpdate's
		// self-recording of remoteVerStr for why this matters.
		{"0.0.1-test", [3]int{}, false},
	}
	for _, c := range cases {
		got, ok := parseSemver(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseSemver(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b [3]int
		want bool
	}{
		{[3]int{1, 0, 0}, [3]int{0, 0, 0}, true},
		{[3]int{1, 4, 0}, [3]int{1, 4, 0}, false},
		{[3]int{1, 4, 0}, [3]int{1, 5, 0}, false},
		{[3]int{1, 5, 0}, [3]int{1, 4, 9}, true},
		{[3]int{0, 0, 0}, [3]int{0, 0, 1}, false},
		{[3]int{2, 0, 0}, [3]int{1, 99, 99}, true},
	}
	for _, c := range cases {
		if got := semverGreater(c.a, c.b); got != c.want {
			t.Errorf("semverGreater(%v, %v) = %v; want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestParseOverlayVersion(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"0.538", 0.538, true},
		{"0.530", 0.53, true},
		{" 1 \n", 1, true},
		{"2", 2, true},
		{"", 0, false},
		{"a.b", 0, false},
		{"0.538\n08/2026", 0, false},
	}
	for _, c := range cases {
		got, ok := parseOverlayVersion(c.in)
		if ok != c.ok || (ok && math.Abs(got-c.want) > 1e-9) {
			t.Errorf("parseOverlayVersion(%q) = %v, %v; want %v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestIsAffirmative(t *testing.T) {
	cases := []struct {
		in        string
		defaultOn bool
		want      bool
	}{
		// auto_update-style: opt-out, absent/unset means enabled.
		{"", true, true},
		{"n", true, false},
		{"N", true, false},
		{"no", true, false},
		{"0", true, false},
		{"y", true, true},
		{"anything-else", true, true},
		// auto_update_overlay-style: opt-in, absent/unset means disabled —
		// a missing value must never mean "enabled" for something with no
		// rollback.
		{"", false, false},
		{"y", false, true},
		{"Y", false, true},
		{"yes", false, true},
		{"1", false, true},
		{"n", false, false},
		{"anything-else", false, false},
	}
	for _, c := range cases {
		if got := isAffirmative(c.in, c.defaultOn); got != c.want {
			t.Errorf("isAffirmative(%q, %v) = %v; want %v", c.in, c.defaultOn, got, c.want)
		}
	}
}

func TestBandwidthOK(t *testing.T) {
	// No /etc/mesh.conf in this test environment, so confValue("auto_update_min_mbps")
	// always returns "" here and bandwidthOK falls back to defaultMinMbps (10).
	cases := []struct {
		mbps float64
		typ  string
		want bool
	}{
		{0, "wired", true},         // wired never has a real ceiling to check
		{0.0001, "wired", true},    // wired passes regardless of the number
		{0, "unknown", false},      // can't reach manet-ctrl — fail closed
		{1000, "unknown", false},   // even a high number is untrusted when the type is unknown
		{5, "halow-mesh", false},   // below the 10 Mbps fallback default
		{9.99, "wifi-mesh", false}, // just below
		{10, "wifi-mesh", true},    // exactly at the threshold passes
		{15, "halow-mesh", true},   // above threshold
	}
	for _, c := range cases {
		if got := bandwidthOK(c.mbps, c.typ); got != c.want {
			t.Errorf("bandwidthOK(%v, %q) = %v; want %v", c.mbps, c.typ, got, c.want)
		}
	}
}
