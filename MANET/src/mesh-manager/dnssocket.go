package main

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// procNetUDP and systemctlCmd are variables so tests can stand in for them.
var (
	procNetUDP   = "/proc/net/udp"
	systemctlCmd = run
)

const (
	// A missing socket must be seen on this many consecutive passes before
	// a restart: bind-dynamic binds a newly added address asynchronously, and
	// configureDnsmasq may have restarted dnsmasq moments ago.
	dnsSocketMissesBeforeRestart = 2
	dnsRestartBackoffMin         = time.Minute
	dnsRestartBackoffMax         = 30 * time.Minute
)

// dnsSocketWatch decides when dnsmasq must be restarted because it has no DNS
// socket on the EUD gateway (the br0 secondary, handed out as dhcp-option=6).
// bind-dynamic does not always bind an address that is (re)added to br0 after
// dnsmasq started; EUDs then get DHCP leases but no DNS, indefinitely, since
// the config text never changes. Restarts back off (1 min doubling to 30 min)
// so a socket that never appears cannot turn into a restart loop.
type dnsSocketWatch struct {
	misses    int
	backoff   time.Duration
	notBefore time.Time
	restarted bool // a restart happened and the socket has not been seen since
}

// observe records one check and reports whether dnsmasq should be restarted now.
func (w *dnsSocketWatch) observe(listening bool, now time.Time) bool {
	if listening {
		w.misses = 0
		w.backoff = 0
		w.notBefore = time.Time{}
		return false
	}
	w.misses++
	if w.misses < dnsSocketMissesBeforeRestart || now.Before(w.notBefore) {
		return false
	}
	if w.backoff == 0 {
		w.backoff = dnsRestartBackoffMin
	} else if w.backoff *= 2; w.backoff > dnsRestartBackoffMax {
		w.backoff = dnsRestartBackoffMax
	}
	w.notBefore = now.Add(w.backoff)
	return true
}

// udpListening reports whether a /proc/net/udp table has a socket bound to
// ip:port, or to the wildcard address on port (which also serves ip). Only
// IPv4: the EUD gateway is an IPv4 address.
func udpListening(r io.Reader, ip net.IP, port int) (bool, error) {
	want := ip.To4()
	if want == nil {
		return false, fmt.Errorf("not an IPv4 address: %s", ip)
	}
	sc := bufio.NewScanner(r)
	header := true
	for sc.Scan() {
		if header {
			header = false
			continue
		}
		// "  12: 5B021E0A:0035 00000000:0000 07 ..." — local_address is the
		// address as a host-order hex u32 (little-endian on arm64 and x86).
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		addrHex, portHex, ok := strings.Cut(fields[1], ":")
		if !ok || len(addrHex) != 8 {
			continue
		}
		p, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || int(p) != port {
			continue
		}
		raw, err := hex.DecodeString(addrHex)
		if err != nil {
			continue
		}
		got := make(net.IP, 4)
		binary.LittleEndian.PutUint32(got, binary.BigEndian.Uint32(raw))
		if got.Equal(want) || got.Equal(net.IPv4zero) {
			return true, nil
		}
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("reading socket table: %w", err)
	}
	return false, nil
}

// ensureDnsmasqServesDNS restarts an active dnsmasq that has no UDP :53 socket
// on gw, subject to dnsSocketWatch's grace pass and backoff. A probe that
// fails (dnsmasq state unknown, socket table unreadable) never restarts.
func (im *ipManager) ensureDnsmasqServesDNS(gw net.IP, now time.Time) {
	if _, err := systemctlCmd(3*time.Second, "systemctl", "is-active", "--quiet", "dnsmasq.service"); err != nil {
		return // inactive or unknown: not this check's job
	}
	f, err := os.Open(procNetUDP)
	if err != nil {
		log.Printf("dnsmasq DNS check: %v", err)
		return
	}
	listening, err := udpListening(f, gw, 53)
	f.Close()
	if err != nil {
		log.Printf("dnsmasq DNS check: %s: %v", procNetUDP, err)
		return
	}
	w := &im.dnsWatch
	if listening && w.restarted {
		log.Printf("dnsmasq DNS check: socket on EUD gateway %s:53 is back", gw)
		w.restarted = false
	}
	if !w.observe(listening, now) {
		return
	}
	log.Printf("dnsmasq has no DNS socket on EUD gateway %s:53 (EUDs get leases but no DNS); restarting, next attempt no sooner than %s",
		gw, w.backoff)
	w.restarted = true
	if out, err := systemctlCmd(10*time.Second, "systemctl", "restart", "dnsmasq.service"); err != nil {
		log.Printf("dnsmasq DNS check: restart failed: %v: %s", err, strings.TrimSpace(out))
	}
}
