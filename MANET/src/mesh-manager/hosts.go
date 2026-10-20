package main

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// Peer hostnames and addresses come from alfred, i.e. from any node that can
// reach the mesh. They are written into /etc/hosts and dnsmasq's config, so a
// hostname carrying a newline could add arbitrary hosts entries or dnsmasq
// directives, and a peer could claim another node's name or point a name
// outside the mesh. Only unambiguous single-label names on mesh addresses
// are published.

var hostLabelRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// defaultMeshNetwork matches newIPManager's fallback.
const defaultMeshNetwork = "10.43.1.0/16"

type hostEntry struct {
	Name string
	IP   string
}

// meshNetworkCIDR returns the configured ipv4_network. A bare network
// without a prefix length (older images) is taken as /24, like manet-ctrl's
// isMeshPeerIP. Returns nil if it does not parse.
func meshNetworkCIDR(conf map[string]string) *net.IPNet {
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

// meshHostIP reports whether s is a plain IPv4 host address inside network,
// excluding its network and broadcast addresses.
func meshHostIP(s string, network *net.IPNet) bool {
	ip := net.ParseIP(s).To4()
	if network == nil || ip == nil || ip.String() != s || !network.Contains(ip) {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	ones, bits := network.Mask.Size()
	if bits-ones >= 2 {
		n := ipToInt(network.IP.To4())
		v := ipToInt(ip)
		if v == n || v == n|(1<<uint(bits-ones)-1) {
			return false
		}
	}
	return true
}

// meshHostEntries returns the registry's name/address pairs that are safe to
// publish, sorted by name, plus a description of each rejected entry. Names
// compare case-insensitively. "localhost" is never published, and selfName
// is only accepted from this node's own entry (selfMAC). A name claimed with
// two different addresses is dropped entirely: there is no way to tell which
// claim is genuine.
func meshHostEntries(nodes map[string]registryNode, network *net.IPNet, selfMAC, selfName string) ([]hostEntry, []string) {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	selfName = strings.ToLower(selfName)
	selfMAC = strings.ToLower(selfMAC)
	display := make(map[string]string)
	claims := make(map[string]map[string]bool)
	var rejected []string
	for _, id := range ids {
		n := nodes[id]
		if n.Hostname == "" || n.IP == "" {
			continue
		}
		lname := strings.ToLower(n.Hostname)
		switch {
		case !hostLabelRE.MatchString(n.Hostname):
			rejected = append(rejected, fmt.Sprintf("%s: invalid hostname %q", id, n.Hostname))
			continue
		case lname == "localhost":
			rejected = append(rejected, fmt.Sprintf("%s: reserved hostname %q", id, n.Hostname))
			continue
		case lname == selfName && (selfMAC == "" || strings.ToLower(n.MAC) != selfMAC):
			rejected = append(rejected, fmt.Sprintf("%s: claims this node's hostname %q", id, n.Hostname))
			continue
		case !meshHostIP(n.IP, network):
			rejected = append(rejected, fmt.Sprintf("%s: %q has address %q outside the mesh network", id, n.Hostname, n.IP))
			continue
		}
		if claims[lname] == nil {
			claims[lname] = make(map[string]bool)
			display[lname] = n.Hostname
		}
		claims[lname][n.IP] = true
	}

	var entries []hostEntry
	for lname, ips := range claims {
		if len(ips) != 1 {
			rejected = append(rejected, fmt.Sprintf("hostname %q claimed by %d addresses; omitted", display[lname], len(ips)))
			continue
		}
		for ip := range ips {
			entries = append(entries, hostEntry{Name: display[lname], IP: ip})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	sort.Strings(rejected)
	return entries, rejected
}

// hostsBlockText renders the managed /etc/hosts block.
func hostsBlockText(entries []hostEntry) string {
	var b strings.Builder
	b.WriteString(hostsBegin + "\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "%s    %s %s.mesh\n", e.IP, e.Name, e.Name)
	}
	b.WriteString(hostsEnd)
	return b.String()
}
