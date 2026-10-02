package main

import (
	"log"
	"os"
	"strings"
	"time"
)

// UIFirewallScript limits who can reach the web UI ports (see its header):
// 443 only from the mesh side (lo, br0) unless ui_uplink_access=y.
const UIFirewallScript = "/usr/local/bin/manet-ui-firewall.sh"

// runUIFirewall (re)applies the UI firewall. The script is idempotent and a
// no-op when nothing changed, so calling it often is cheap.
func runUIFirewall() {
	if _, err := os.Stat(UIFirewallScript); err != nil {
		return
	}
	if out, err := runCmd(10*time.Second, UIFirewallScript); err != nil {
		log.Printf("ui firewall: %v: %s", err, strings.TrimSpace(out))
	}
}

// uiFirewallLoop applies the UI firewall at startup, so port 443 is limited
// from boot (mesh-manager only runs the script once a DHCP pool exists),
// and every minute after, which also restores the table if an nftables
// restart flushed the ruleset.
func uiFirewallLoop() {
	for {
		runUIFirewall()
		time.Sleep(time.Minute)
	}
}
