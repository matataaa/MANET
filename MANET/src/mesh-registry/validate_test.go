package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestValidHostname(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"EUD1-MANET-d418", true},
		{"a", true},
		{strings.Repeat("a", 63), true},
		{strings.Repeat("a", 64), false},
		{"", false},
		{"-lead", false},
		{"trail-", false},
		{"two.labels", false},
		{"under_score", false},
		{"x'$(reboot)'", false},
		{"$(id)", false},
		{"a b", false},
		{"evil\n10.0.0.1 victim", false},
	}
	for _, tt := range tests {
		if got := validHostname(tt.in); got != tt.want {
			t.Errorf("validHostname(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestValidMeshIPv4(t *testing.T) {
	network := meshNetwork(map[string]string{"ipv4_network": "10.30.2.0/24"})
	tests := []struct {
		in   string
		want bool
	}{
		{"10.30.2.91", true},
		{"10.30.3.1", false},
		{"192.168.1.1", false},
		{"127.0.0.1", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"::1", false},
		{"10.30.2.91'", false},
		{"10.30.2.91\n", false},
		{"010.030.002.091", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := validMeshIPv4(tt.in, network); got != tt.want {
			t.Errorf("validMeshIPv4(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	if validMeshIPv4("10.30.2.91", nil) {
		t.Error("validMeshIPv4 with no network accepted an address")
	}
}

func TestMeshNetwork(t *testing.T) {
	tests := []struct {
		conf string
		want string
	}{
		{"10.30.2.0/24", "10.30.2.0/24"},
		{"10.30.2.0", "10.30.2.0/24"},
		{"", "10.43.0.0/16"}, // defaultMeshNetwork, masked
	}
	for _, tt := range tests {
		got := meshNetwork(map[string]string{"ipv4_network": tt.conf})
		if got == nil || got.String() != tt.want {
			t.Errorf("meshNetwork(%q) = %v, want %s", tt.conf, got, tt.want)
		}
	}
	if got := meshNetwork(map[string]string{"ipv4_network": "garbage"}); got != nil {
		t.Errorf("meshNetwork(garbage) = %v, want nil", got)
	}
}

func TestSanitizePeer(t *testing.T) {
	network := meshNetwork(map[string]string{"ipv4_network": "10.30.2.0/24"})
	tests := []struct {
		name     string
		in       NodeInfo
		keep     bool
		hostname string
		ipv4     string
		macs     string
	}{
		{
			name: "valid",
			in:   NodeInfo{MAC: "0e:bf:74:00:28:d2", Hostname: "EUD1", IPv4: "10.30.2.10", MACAddresses: "0e:bf:74:00:28:d2,0c:bf:74:00:28:d2"},
			keep: true, hostname: "EUD1", ipv4: "10.30.2.10", macs: "0e:bf:74:00:28:d2,0c:bf:74:00:28:d2",
		},
		{
			name: "quote injection in hostname",
			in:   NodeInfo{MAC: "0e:bf:74:00:28:d2", Hostname: "x'$(touch /tmp/pwn)'", IPv4: "10.30.2.10"},
			keep: true, hostname: "", ipv4: "10.30.2.10",
		},
		{
			name: "newline in hostname",
			in:   NodeInfo{MAC: "0e:bf:74:00:28:d2", Hostname: "a\nNODE_X_HOSTNAME='b'", IPv4: "10.30.2.10"},
			keep: true, hostname: "", ipv4: "10.30.2.10",
		},
		{
			name: "out-of-subnet ip",
			in:   NodeInfo{MAC: "0e:bf:74:00:28:d2", Hostname: "EUD1", IPv4: "192.168.1.1"},
			keep: true, hostname: "EUD1", ipv4: "",
		},
		{
			name: "bad interface mac dropped from list",
			in:   NodeInfo{MAC: "0e:bf:74:00:28:d2", MACAddresses: "0e:bf:74:00:28:d2,$(id)"},
			keep: true, macs: "0e:bf:74:00:28:d2",
		},
		{
			name: "invalid primary mac drops peer",
			in:   NodeInfo{MAC: "0e:bf:74:00:28:d2'", Hostname: "EUD1"},
			keep: false,
		},
		{
			name: "dotted mac drops peer",
			in:   NodeInfo{MAC: "0ebf.7400.28d2"},
			keep: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := tt.in
			if got := sanitizePeer(&n, network); got != tt.keep {
				t.Fatalf("sanitizePeer keep = %v, want %v", got, tt.keep)
			}
			if !tt.keep {
				return
			}
			if n.Hostname != tt.hostname || n.IPv4 != tt.ipv4 || n.MACAddresses != tt.macs {
				t.Errorf("got hostname=%q ipv4=%q macs=%q, want %q %q %q",
					n.Hostname, n.IPv4, n.MACAddresses, tt.hostname, tt.ipv4, tt.macs)
			}
		})
	}
}

// registryLineRE is the strict shape every registry line must have: the
// pattern the Go readers and shell scripts parse with.
var registryLineRE = regexp.MustCompile(`^NODE_[0-9A-Fa-f]{12}_[A-Z0-9_]+='[^'\x00-\x1f\x7f]*'$`)

func TestWriteNodeRejectsInjection(t *testing.T) {
	payloads := []string{
		"x'$(touch /tmp/pwn)'",
		"a\nNODE_000000000000_IS_NTP_SERVER='true'",
		"tab\there",
		"`id`",
	}
	for _, p := range payloads {
		var b strings.Builder
		writeNodeWithState(&b, NodeInfo{
			MAC: "0e:bf:74:00:28:d2", Hostname: p, Applets: p, ChannelReport: p, GPSLat: p,
		}, true)
		for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
			if !registryLineRE.MatchString(line) {
				t.Errorf("payload %q produced unsafe line %q", p, line)
			}
		}
		// Backticks are inert inside single quotes and carry no newline, so
		// they pass through; everything else must have been blanked.
		if p != "`id`" && strings.Contains(b.String(), "HOSTNAME='"+p) {
			t.Errorf("payload %q written verbatim", p)
		}
	}

	var b strings.Builder
	writeNodeWithState(&b, NodeInfo{MAC: "0e:bf:74:00:28:d2'$(id)", Hostname: "EUD1"}, true)
	if b.Len() != 0 {
		t.Errorf("node with invalid MAC was written: %q", b.String())
	}
}
