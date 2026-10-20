package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestMeshHostIP(t *testing.T) {
	network := meshNetworkCIDR(map[string]string{"ipv4_network": "10.30.2.0/24"})
	tests := []struct {
		in   string
		want bool
	}{
		{"10.30.2.91", true},
		{"10.30.2.0", false},   // network address
		{"10.30.2.255", false}, // broadcast
		{"10.30.3.1", false},
		{"127.0.0.1", false},
		{"::1", false},
		{"10.30.2.91 evil", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := meshHostIP(tt.in, network); got != tt.want {
			t.Errorf("meshHostIP(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
	if meshHostIP("10.30.2.91", nil) {
		t.Error("meshHostIP with no network accepted an address")
	}
	if got := meshNetworkCIDR(map[string]string{"ipv4_network": "10.30.2.0"}); got == nil || got.String() != "10.30.2.0/24" {
		t.Errorf("bare network = %v, want 10.30.2.0/24", got)
	}
}

func TestMeshHostEntries(t *testing.T) {
	network := meshNetworkCIDR(map[string]string{"ipv4_network": "10.30.2.0/24"})
	const selfMAC = "0e:bf:74:00:28:d2"
	tests := []struct {
		name  string
		nodes map[string]registryNode
		want  []hostEntry
	}{
		{
			name: "valid peers sorted by name",
			nodes: map[string]registryNode{
				"B": {Hostname: "EUD2", IP: "10.30.2.20"},
				"A": {Hostname: "EUD3", IP: "10.30.2.30"},
			},
			want: []hostEntry{{"EUD2", "10.30.2.20"}, {"EUD3", "10.30.2.30"}},
		},
		{
			name: "quote and command substitution",
			nodes: map[string]registryNode{
				"A": {Hostname: "x'$(reboot)'", IP: "10.30.2.20"},
				"B": {Hostname: "$(id)", IP: "10.30.2.21"},
			},
		},
		{
			name: "newline injection",
			nodes: map[string]registryNode{
				"A": {Hostname: "evil\n10.30.2.5 victim", IP: "10.30.2.20"},
			},
		},
		{
			name: "out of subnet",
			nodes: map[string]registryNode{
				"A": {Hostname: "EUD2", IP: "8.8.8.8"},
			},
		},
		{
			name: "localhost reserved in any case",
			nodes: map[string]registryNode{
				"A": {Hostname: "LocalHost", IP: "10.30.2.20"},
			},
		},
		{
			name: "peer claiming this node's name",
			nodes: map[string]registryNode{
				"SELF": {Hostname: "EUD1", IP: "10.30.2.10", MAC: selfMAC},
				"PEER": {Hostname: "eud1", IP: "10.30.2.66", MAC: "0e:00:00:00:00:01"},
			},
			want: []hostEntry{{"EUD1", "10.30.2.10"}},
		},
		{
			name: "conflicting claims dropped, identical collapse",
			nodes: map[string]registryNode{
				"A": {Hostname: "EUD2", IP: "10.30.2.20"},
				"B": {Hostname: "eud2", IP: "10.30.2.21"},
				"C": {Hostname: "EUD3", IP: "10.30.2.30"},
				"D": {Hostname: "EUD3", IP: "10.30.2.30"},
			},
			want: []hostEntry{{"EUD3", "10.30.2.30"}},
		},
		{
			name: "dotted name",
			nodes: map[string]registryNode{
				"A": {Hostname: "evil.example.com", IP: "10.30.2.20"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := meshHostEntries(tt.nodes, network, selfMAC, "EUD1")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("meshHostEntries = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHostsBlockText(t *testing.T) {
	got := hostsBlockText([]hostEntry{{"EUD2", "10.30.2.20"}})
	want := hostsBegin + "\n10.30.2.20    EUD2 EUD2.mesh\n" + hostsEnd
	if got != want {
		t.Errorf("hostsBlockText:\n%q\nwant\n%q", got, want)
	}
}

// A value is never allowed to continue onto the next line: with `[^']*` a
// stray newline let one field's value swallow the following lines.
func TestRegREOneLinePerValue(t *testing.T) {
	data := "NODE_0EBF740028D2_HOSTNAME='evil\n10.30.2.5 victim'\nNODE_0EBF740028D2_IPV4_ADDRESS='10.30.2.20'\n"
	var fields []string
	for _, m := range regRE.FindAllStringSubmatch(data, -1) {
		fields = append(fields, m[2]+"="+m[3])
	}
	if want := "IPV4_ADDRESS=10.30.2.20"; strings.Join(fields, ",") != want {
		t.Errorf("regRE parsed %v, want [%s]", fields, want)
	}
}
