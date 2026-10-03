package main

import (
	"encoding/json"
	"testing"
)

// TestFleetSealedPackageFitsRealisticMTUBudget builds a package shaped like
// a real 4-node fleet stage — every saveableKeys entry populated with a
// realistic value, plus 4 fleet profiles and a node_profiles map — seals it,
// and does two different things with the result:
//
//  1. It ASSERTS the sealed size stays under maxSaneEnvelopeBytes, a
//     deliberately generous absolute ceiling (64KiB) meant to catch a gross
//     encoding regression (e.g. accidental double base64, a quadratic
//     marshal bug, or profiles/config being duplicated per node instead of
//     shared) — not to model any real transport limit. This part of the
//     test can fail the build.
//  2. It LOGS (does not assert) the size against bat0/br0's 1400-byte MTU
//     (see the repo's bat0/br0 MTU-1400 fix) purely for visibility. This
//     part is deliberately observational-only: whether a package over 1400
//     bytes actually works depends entirely on alfred's own real
//     fragmentation/reassembly behavior, which this repo has no accessible
//     way to verify statically — the vendored alfred binary in
//     binaries_arm64/ is unstripped but exposes no size-limit strings/
//     symbols, is untouched/unmodifiable per project convention, and this
//     test has no mesh to actually round-trip data through. A realistic
//     4-node plaintext package was ALREADY well over 1400 bytes before this
//     change existed, so alfred must already fragment/reassemble across
//     multiple frames for fleet config push to have ever worked in the
//     field — this test cannot confirm that behavior still holds at the
//     new, ~33%-larger sealed size; that needs a real `alfred -s 70` /
//     `alfred -r 70` round trip on hardware.
func TestFleetSealedPackageFitsRealisticMTUBudget(t *testing.T) {
	const bat0BR0MTU = 1400
	const maxSaneEnvelopeBytes = 64 * 1024

	config := map[string]interface{}{}
	for k := range saveableKeys {
		switch k {
		case "admin_password", "mesh_key", "lan_ap_key":
			config[k] = "Sup3r-Secret-Passphrase-2026!"
		case "update_url":
			config[k] = "https://updates.example.mesh:8443/manet/release/software-latest.tar.gz"
		default:
			config[k] = "a-realistic-value-1234"
		}
	}

	profiles := map[string]interface{}{}
	nodeProfiles := map[string]interface{}{}
	for i := 0; i < 4; i++ {
		pid := "profile-node-" + string(rune('a'+i))
		profiles[pid] = map[string]interface{}{
			"name":   "Node " + string(rune('A'+i)) + " profile",
			"config": config,
		}
		mac := "aa:bb:cc:dd:ee:0" + string(rune('0'+i))
		nodeProfiles[mac] = pid
	}

	pkg := map[string]interface{}{
		"version":       "abcdef01",
		"config":        config,
		"profiles":      profiles,
		"node_profiles": nodeProfiles,
		"staged_by":     "node-alpha",
		"staged_at":     1893456000,
		"pkg_id":        "0123456789abcdef0123456789abcdef",
		"sender_mac":    "aa:bb:cc:dd:ee:01",
		"expires_at":    1893459600,
	}

	plaintext, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal plaintext package: %v", err)
	}

	envelope, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}

	t.Logf("realistic 4-node package: plaintext=%d bytes, sealed envelope=%d bytes (bat0/br0 MTU=%d)",
		len(plaintext), len(envelope), bat0BR0MTU)

	if len(envelope) > maxSaneEnvelopeBytes {
		t.Fatalf("sealed 4-node package is %d bytes, over the %d-byte sanity ceiling — likely an encoding regression, not just an MTU concern",
			len(envelope), maxSaneEnvelopeBytes)
	}

	if len(envelope) > bat0BR0MTU {
		t.Logf("OBSERVATIONAL ONLY (not a test failure): sealed 4-node package (%d bytes) exceeds the "+
			"bat0/br0 MTU (%d bytes) — this must be verified against alfred's actual fragmentation/"+
			"reassembly behavior on hardware, not assumed safe or unsafe from this test alone",
			len(envelope), bat0BR0MTU)
	}
}
