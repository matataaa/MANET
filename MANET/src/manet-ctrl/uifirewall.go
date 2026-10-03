package main

import (
	"log"
	"os"
	"strings"
	"time"
)

// UIFirewallScript limits who can reach the web UI ports (see its header):
// 443 only from the mesh side (lo, br0) unless ui_uplink_access=y, and SSH
// (22) likewise unless ssh_uplink_access=y.
const UIFirewallScript = "/usr/local/bin/manet-ui-firewall.sh"

// uplinkAccessChanged reports whether updates changes a key the firewall
// script reads, so a save or fleet push re-applies it right away rather
// than at the next minute tick.
func uplinkAccessChanged(updates, existingConf map[string]string) bool {
	for _, k := range []string{"ui_uplink_access", "ssh_uplink_access"} {
		if v, ok := updates[k]; ok && v != existingConf[k] {
			return true
		}
	}
	return false
}

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
