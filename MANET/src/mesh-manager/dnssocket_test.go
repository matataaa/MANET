package main

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const udpHeader = "   sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops\n"

// Primary 10.30.2.90 and loopback bound, secondary 10.30.2.91 missing: the
// state bind-dynamic leaves when br0's secondary is re-added after start.
const udpPrimaryOnly = udpHeader +
	"  101: 5A021E0A:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1001 2 0000000000000000 0\n" +
	"  102: 0100007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1002 2 0000000000000000 0\n" +
	"  103: 00000000:0043 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1003 2 0000000000000000 0\n" +
	"  104: 5B021E0A:0044 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1004 2 0000000000000000 0\n"

const udpSecondaryLine = "  105: 5B021E0A:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1005 2 0000000000000000 0\n"

func TestUDPListening(t *testing.T) {
	gw := net.ParseIP("10.30.2.91")
	cases := []struct {
		name  string
		table string
		want  bool
	}{
		{"secondary missing", udpPrimaryOnly, false},
		{"secondary bound", udpPrimaryOnly + udpSecondaryLine, true},
		// A wildcard :53 socket serves every address, including the gateway.
		{"wildcard", udpHeader + "  1: 00000000:0035 00000000:0000 07 0 0 0 0 0 1 2 0 0\n", true},
		{"header only", udpHeader, false},
		{"malformed lines", udpHeader + "garbage\n  1: zz:0035\n  2: 5B021E0A:zz\n", false},
	}
	for _, tc := range cases {
		got, err := udpListening(strings.NewReader(tc.table), gw, 53)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	if _, err := udpListening(strings.NewReader(udpHeader), net.ParseIP("fd00::1"), 53); err == nil {
		t.Error("IPv6 gateway: want error")
	}
}

func TestDNSSocketWatchGraceAndBackoff(t *testing.T) {
	var w dnsSocketWatch
	t0 := time.Unix(1_000_000, 0)
	pass := func(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

	if w.observe(false, pass(0)) {
		t.Fatal("restarted on the first miss; want one grace pass")
	}
	if !w.observe(false, pass(1)) {
		t.Fatal("no restart on the second consecutive miss")
	}
	// Backoff 1 min, then 2, 4, ... capped at 30.
	if w.observe(false, pass(1).Add(30*time.Second)) {
		t.Fatal("restarted inside the 1 min backoff")
	}
	if !w.observe(false, pass(2)) || w.backoff != 2*time.Minute {
		t.Fatalf("want restart with 2 min backoff, got backoff %v", w.backoff)
	}
	now := pass(2)
	for i := 0; i < 10; i++ {
		now = now.Add(w.backoff)
		if !w.observe(false, now) {
			t.Fatalf("no restart after backoff elapsed (iteration %d)", i)
		}
	}
	if w.backoff != dnsRestartBackoffMax {
		t.Fatalf("backoff %v, want capped at %v", w.backoff, dnsRestartBackoffMax)
	}

	// Seeing the socket resets everything, grace pass included.
	if w.observe(true, now) {
		t.Fatal("restart while listening")
	}
	if w.observe(false, now) || !w.observe(false, now) || w.backoff != dnsRestartBackoffMin {
		t.Fatalf("after recovery want grace pass then restart at %v backoff, got %v", dnsRestartBackoffMin, w.backoff)
	}
}

// fakeDNSCheck stands in for systemctl and /proc/net/udp.
func fakeDNSCheck(t *testing.T, active bool, table string) *[]string {
	t.Helper()
	var calls []string
	oldCmd, oldPath := systemctlCmd, procNetUDP
	t.Cleanup(func() { systemctlCmd, procNetUDP = oldCmd, oldPath })
	procNetUDP = filepath.Join(t.TempDir(), "udp")
	if table != "" {
		if err := os.WriteFile(procNetUDP, []byte(table), 0644); err != nil {
			t.Fatal(err)
		}
	}
	systemctlCmd = func(_ time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		if args[0] == "is-active" && !active {
			return "", errors.New("exit status 3")
		}
		return "", nil
	}
	return &calls
}

func countRestarts(calls []string) int {
	n := 0
	for _, c := range calls {
		if c == "restart dnsmasq.service" {
			n++
		}
	}
	return n
}

func TestEnsureDnsmasqServesDNS(t *testing.T) {
	gw := net.ParseIP("10.30.2.91")
	t0 := time.Unix(1_000_000, 0)
	cases := []struct {
		name     string
		active   bool
		table    string // "" = socket table unreadable
		restarts int
	}{
		{"missing socket", true, udpPrimaryOnly, 1},
		{"socket present", true, udpPrimaryOnly + udpSecondaryLine, 0},
		{"dnsmasq inactive", false, udpPrimaryOnly, 0},
		{"table unreadable", true, "", 0},
	}
	for _, tc := range cases {
		calls := fakeDNSCheck(t, tc.active, tc.table)
		im := &ipManager{}
		im.ensureDnsmasqServesDNS(gw, t0)                     // grace pass
		im.ensureDnsmasqServesDNS(gw, t0.Add(30*time.Second)) // restart if missing
		im.ensureDnsmasqServesDNS(gw, t0.Add(60*time.Second)) // inside backoff
		if got := countRestarts(*calls); got != tc.restarts {
			t.Errorf("%s: %d restarts, want %d (calls %v)", tc.name, got, tc.restarts, *calls)
		}
	}
}
