package main

import (
	"net"
	"testing"
)

// EUD4's live mesh-eud.conf (chunk 7 of 10.30.2.0/24) with only the bind line
// changed: an already-configured node gets exactly one rewrite, no churn.
const eud4DnsmasqConf = `interface=br0
bind-dynamic
dhcp-range=10.30.2.92,10.30.2.101,4m
dhcp-option=3,10.30.2.91
dhcp-option=6,10.30.2.91
domain=mesh
local=/mesh/
resolv-file=/run/systemd/resolve/resolv.conf
address=/manet.mesh/10.30.2.91
address=/perf.mesh/10.30.2.91
log-dhcp
`

func TestDnsmasqConfText(t *testing.T) {
	c := chunkIPs{
		Primary:   net.ParseIP("10.30.2.90"),
		Secondary: net.ParseIP("10.30.2.91"),
		DHCPStart: net.ParseIP("10.30.2.92"),
		DHCPEnd:   net.ParseIP("10.30.2.101"),
	}
	if got := dnsmasqConfText(c); got != eud4DnsmasqConf {
		t.Errorf("dnsmasqConfText:\n%s\nwant:\n%s", got, eud4DnsmasqConf)
	}
}
