package main

import "testing"

// Only whole NODE_<mac>_<FIELD>='value' lines count: a value split across
// lines or a record embedded mid-line (e.g. in a corrupted or hostile value)
// must not yield fields.
func TestRegistryREWholeLinesOnly(t *testing.T) {
	data := "NODE_aabb_HOSTNAME='good'\n" +
		"NODE_ccdd_HOSTNAME='multi\nline'\n" +
		"junk NODE_eeff_IS_GATEWAY='1'\n" +
		"NODE_0011_IPV4_ADDRESS='10.30.2.5'"
	got := registryRE.FindAllStringSubmatch(data, -1)
	if len(got) != 2 || got[0][1] != "aabb" || got[0][3] != "good" ||
		got[1][1] != "0011" || got[1][2] != "IPV4_ADDRESS" || got[1][3] != "10.30.2.5" {
		t.Fatalf("matches = %q", got)
	}
}
