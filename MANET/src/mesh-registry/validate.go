package main

import (
	"log"
	"net"
	"regexp"
	"strings"
)

// Peer records arrive over alfred from any node that can reach the mesh, and
// end up in /var/run/mesh_node_registry, /etc/hosts, dnsmasq config and the
// web UI. A registry line used to carry a peer's hostname verbatim, so a
// hostname like x'$(cmd)' ran as root in any script that sourced the file.
// Everything a peer sends is checked here before it is stored.

// defaultMeshNetwork is mesh-manager's fallback when mesh.conf has no
// ipv4_network — the network this node's own address is allocated from.
const defaultMeshNetwork = "10.43.1.0/16"

var (
	hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	macRE       = regexp.MustCompile(`^[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}$`)
)

// validHostname reports whether s is a single DNS label: letters, digits and
// hyphens, 1-63 long, no leading or trailing hyphen.
func validHostname(s string) bool {
	return hostLabelRE.MatchString(s)
}

// validMAC accepts only colon-separated 6-byte MACs: the registry key is the
// MAC with colons stripped, so anything else would corrupt the key.
func validMAC(s string) bool {
	return macRE.MatchString(s)
}

// meshNetwork returns the configured mesh ipv4_network. A bare network
// without a prefix length (older images) is taken as /24, like manet-ctrl's
// isMeshPeerIP. Returns nil if it does not parse.
func meshNetwork(conf map[string]string) *net.IPNet {
	network := conf["ipv4_network"]
	if network == "" {
		network = defaultMeshNetwork
	}
	if !strings.Contains(network, "/") {
		network += "/24"
	}
	_, cidr, err := net.ParseCIDR(network)
	if err != nil || cidr.IP.To4() == nil {
		return nil
	}
	return cidr
}

// validMeshIPv4 reports whether s is a plain IPv4 host address inside
// network (mirrors manet-ctrl's isMeshPeerIP).
func validMeshIPv4(s string, network *net.IPNet) bool {
	ip := net.ParseIP(s)
	if network == nil || ip == nil || ip.To4() == nil || ip.String() != s {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return network.Contains(ip)
}

// safeValue reports whether v can be written inside a single-quoted
// registry value: no quote, newline or other control character.
func safeValue(v string) bool {
	for _, r := range v {
		if r == '\'' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// logged remembers rejections already reported. Only touched from the main
// ticker loop (run), so no locking is needed. Keys include peer-supplied
// values, so the set is cleared once it reaches loggedMax to stay bounded.
var logged = make(map[string]bool)

const loggedMax = 1024

// logOnce logs a rejection the first time it is seen, so a misbehaving peer
// does not flood the journal every 15s tick.
func logOnce(key, format string, args ...interface{}) {
	if logged[key] {
		return
	}
	if len(logged) >= loggedMax {
		logged = make(map[string]bool)
	}
	logged[key] = true
	log.Printf(format, args...)
}

// sanitizePeer validates a peer record received from alfred (or reloaded
// from the known-nodes cache). It returns false if the record has no usable
// MAC identity and must be dropped; otherwise invalid fields are cleared so
// the rest of the record (e.g. its IPv4 chunk claim, which mesh-manager uses
// for collision avoidance) still propagates.
func sanitizePeer(n *NodeInfo, network *net.IPNet) bool {
	if !validMAC(n.MAC) {
		logOnce("mac|"+n.MAC, "dropping peer with invalid MAC %q", n.MAC)
		return false
	}
	id := normMAC(n.MAC)

	if n.Hostname != "" && !validHostname(n.Hostname) {
		logOnce(id+"|hostname|"+n.Hostname, "peer %s: ignoring invalid hostname %q", id, n.Hostname)
		n.Hostname = ""
	}
	if n.IPv4 != "" && !validMeshIPv4(n.IPv4, network) {
		logOnce(id+"|ipv4|"+n.IPv4, "peer %s: ignoring IPv4 %q outside the mesh network", id, n.IPv4)
		n.IPv4 = ""
	}
	if n.MACAddresses != "" {
		var macs []string
		for _, m := range strings.Split(n.MACAddresses, ",") {
			if validMAC(m) {
				macs = append(macs, m)
			} else {
				logOnce(id+"|macs|"+m, "peer %s: ignoring invalid interface MAC %q", id, m)
			}
		}
		n.MACAddresses = strings.Join(macs, ",")
	}
	return true
}
