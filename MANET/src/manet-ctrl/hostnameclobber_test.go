package main

import (
	"testing"
)

// TestFleetApplyConfigNeverRenamesReceiverHostname is the regression test
// for the hardware-confirmed bug: a completely normal "load current config
// -> edit one unrelated field -> save" fleet-push flow naturally round-trips
// the STAGING node's own node_hostname value alongside the actual edit
// (since the stage payload is built from a GET of current config), and
// fleetApplyConfig used to apply that literally on every OTHER node,
// renaming its real OS hostname prefix to the sender's. This happened live
// during hardware testing from a bare callsign edit alone and needed manual
// SSH recovery on two nodes.
//
// This simulates exactly that: a pkg["config"] containing the STAGING
// node's own node_hostname plus one unrelated field change (callsign),
// applied via fleetApplyConfig as if received by a DIFFERENT node with its
// own distinct existing node_hostname. The receiver's hostname must be
// completely unchanged; the unrelated field must still apply.
func TestFleetApplyConfigNeverRenamesReceiverHostname(t *testing.T) {
	const receiverOriginal = "node_hostname=eud3-original\nmesh_ssid=meshnet\ncallsign=OLDCALL\n"
	withTempMeshConf(t, receiverOriginal)

	// This is what a normal stage payload looks like when a DIFFERENT node
	// (the sender) staged it: its own current node_hostname rides along
	// with the operator's actual edit (callsign), exactly as
	// fleetCollectEditState/configSave naturally submit every field, not
	// just changed ones.
	pkg := map[string]interface{}{
		"config": map[string]interface{}{
			"node_hostname": "eud1-sender-prefix",
			"mesh_ssid":     "meshnet", // unchanged from the receiver's own value
			"callsign":      "NEWCALL", // the operator's actual, intended edit
		},
	}

	fleetApplyConfig(pkg)

	after := loadKVFile(MeshConfFile)
	if after["node_hostname"] != "eud3-original" {
		t.Fatalf("receiver's node_hostname was clobbered by the sender's value: got %q, want %q",
			after["node_hostname"], "eud3-original")
	}
	if after["callsign"] != "NEWCALL" {
		t.Fatalf("expected the unrelated real edit (callsign) to still apply, got %q", after["callsign"])
	}
}

// TestDropLocalIdentityKeys is the direct unit test for the guard itself:
// node_hostname must be removed unconditionally, regardless of value,
// while every other key is left untouched.
func TestDropLocalIdentityKeys(t *testing.T) {
	m := map[string]string{
		"node_hostname": "some-sender-value",
		"callsign":      "NEWCALL",
		"mesh_ssid":     "meshnet",
	}
	dropLocalIdentityKeys(m)
	if _, ok := m["node_hostname"]; ok {
		t.Fatalf("node_hostname must be unconditionally dropped, got %q", m["node_hostname"])
	}
	if m["callsign"] != "NEWCALL" || m["mesh_ssid"] != "meshnet" {
		t.Fatalf("dropLocalIdentityKeys must not touch unrelated keys, got %+v", m)
	}
}
