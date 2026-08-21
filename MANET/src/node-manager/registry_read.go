package main

import (
	"net"
	"os"
	"regexp"
	"strings"
)

const (
	registryFile      = "/var/run/mesh_node_registry"
	channelReportFile = "/var/run/mesh_channel_report.json"
)

// registryRE matches the flat NODE_<mac>_<FIELD>='value' lines mesh-registry
// writes — same convention gateway-manager already parses this file with.
var registryRE = regexp.MustCompile(`NODE_([A-Fa-f0-9]+)_([A-Z0-9_]+)='([^']*)'`)

// readRegistry parses /var/run/mesh_node_registry into per-node field maps,
// keyed by MAC (no separators, lowercase-as-written). node-manager has no
// registry reader today — this is the first one, mirroring how upstream's
// channel-election.sh/tourguide-manager.sh/quorum-checker.sh all read this
// same file.
func readRegistry(path string) map[string]map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	nodes := make(map[string]map[string]string)
	for _, m := range registryRE.FindAllStringSubmatch(string(data), -1) {
		mac, field, val := m[1], m[2], m[3]
		if nodes[mac] == nil {
			nodes[mac] = make(map[string]string)
		}
		nodes[mac][field] = val
	}
	return nodes
}

// myRegistryMAC returns this node's own registry key (mesh-registry keys
// entries by bat0/br0's MAC with separators stripped) — used to exclude our
// own registry entry when aggregating peer reports, since the freshly
// scanned local report is already included separately under "self" and
// would otherwise be double-counted alongside our own (staler) published
// copy of it.
func myRegistryMAC() string {
	for _, name := range []string{"bat0", "br0"} {
		iface, err := net.InterfaceByName(name)
		if err == nil && len(iface.HardwareAddr) > 0 {
			mac := iface.HardwareAddr.String()
			return strings.ReplaceAll(strings.ReplaceAll(mac, ":", ""), "-", "")
		}
	}
	return ""
}

// writeStateFile atomically writes a small local state file — same
// tmp+rename pattern used throughout this codebase (mesh-registry,
// mesh-manager, mesh-chat) for anything another process reads.
func writeStateFile(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
