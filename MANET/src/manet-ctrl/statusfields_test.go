package main

import (
	"os"
	"regexp"
	"testing"
)

// The Config tab fills its edit form from /api/admin/status and saves every
// field in config.js's meshFields on each save. A field the status omits
// renders as its first option and is written back on every save: that is how
// each Config save silently set ui_uplink_access=n and locked the UI out of
// a gateway's LAN. Every meshFields key must come back in current_config.
func TestAdminStatusReturnsEveryConfigTabField(t *testing.T) {
	withTempMeshConf(t, "ui_uplink_access=y\nssh_uplink_access=y\n")

	js, err := os.ReadFile("../../rootfs/usr/local/share/manet/www/js/config.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)const meshFields = \[(.*?)\];`).FindSubmatch(js)
	if m == nil {
		t.Fatal("meshFields not found in config.js")
	}
	fields := regexp.MustCompile(`'([a-z0-9_]+)'`).FindAllSubmatch(m[1], -1)
	if len(fields) == 0 {
		t.Fatal("no fields parsed from meshFields")
	}

	cfg := assembleAdminStatus(true).CurrentConfig
	for _, f := range fields {
		if _, ok := cfg[string(f[1])]; !ok {
			t.Errorf("config.js meshFields has %q but /api/admin/status does not return it", f[1])
		}
	}
	for _, k := range []string{"ui_uplink_access", "ssh_uplink_access"} {
		if got := cfg[k]; got != "y" {
			t.Errorf("%s = %q, want the stored y", k, got)
		}
	}
}
