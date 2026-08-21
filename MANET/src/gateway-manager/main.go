package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"
)

const (
	meshConfFile      = "/etc/mesh.conf"
	registryFile      = "/var/run/mesh_node_registry"
	uplinkStateFile   = "/run/manet-uplink.env"
	gatewayStateFile  = "/var/run/mesh-gateway.state"
	upstreamIfaceFile = "/var/run/upstream_iface"

	probeFailThreshold = 3
)

var probeTargets = []string{"1.1.1.1", "8.8.8.8", "9.9.9.9"}
var consecutiveProbeFailures int

type Config struct {
	Enabled      bool
	NAT          bool
	MSSClamp     bool
	Bandwidth    string
	EUDBandwidth string
	PollSec      int
}

func loadConfig() Config {
	cfg := Config{
		Enabled:  true,
		NAT:      true,
		MSSClamp: true,
		PollSec:  10,
	}
	kv := loadKV(meshConfFile)
	if v, ok := kv["gateway"]; ok {
		cfg.Enabled = v != "n"
	}
	if v, ok := kv["gateway_nat"]; ok {
		cfg.NAT = v != "n"
	}
	if v, ok := kv["gateway_mss_clamp"]; ok {
		cfg.MSSClamp = v != "n"
	}
	if v, ok := kv["gateway_bandwidth"]; ok && v != "" {
		cfg.Bandwidth = v
	}
	if v, ok := kv["eud_bandwidth"]; ok && v != "" && v != "0" {
		cfg.EUDBandwidth = v
	}
	if v, ok := kv["gateway_poll"]; ok && v != "" {
		n := 0
		fmt.Sscanf(v, "%d", &n)
		if n >= 5 && n <= 300 {
			cfg.PollSec = n
		}
	}
	return cfg
}

var Version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-version" || os.Args[1] == "--version") {
		fmt.Println(Version)
		return
	}

	log.SetFlags(0)
	log.SetPrefix("")
	log.Printf("starting (version %s)", Version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := loadConfig()
	log.Printf("gateway-manager starting (poll=%ds gw=%v nat=%v mss=%v bw=%s)",
		cfg.PollSec, cfg.Enabled, cfg.NAT, cfg.MSSClamp, cfg.Bandwidth)

	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	poll(cfg)

	ticker := time.NewTicker(time.Duration(cfg.PollSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("shutting down")
			return
		case <-sighup:
			old := cfg
			cfg = loadConfig()
			log.Printf("config reloaded (gw=%v nat=%v mss=%v bw=%s)",
				cfg.Enabled, cfg.NAT, cfg.MSSClamp, cfg.Bandwidth)
			if old.PollSec != cfg.PollSec {
				ticker.Reset(time.Duration(cfg.PollSec) * time.Second)
			}
			poll(cfg)
		case <-ticker.C:
			poll(cfg)
		}
	}
}

func poll(cfg Config) {
	isGW := isGateway()

	if isGW && !cfg.Enabled {
		log.Println("gateway mode disabled in config — demoting")
		run("batctl", "gw_mode", "client")
		os.Remove(gatewayStateFile)
		clearNAT()
		isGW = false
	}

	if isGW {
		pollGateway(cfg)
	} else {
		pollClient(cfg)
	}

	enforceEUDBandwidth(cfg.EUDBandwidth)
}

func pollGateway(cfg Config) {
	iface := upstreamIface()
	if iface == "" {
		demoteGateway("no upstream interface")
		return
	}

	if !ifaceHasCarrier(iface) {
		demoteGateway(fmt.Sprintf("%s lost carrier", iface))
		return
	}

	if ifaceBridgedToBr0(iface) {
		demoteGateway(fmt.Sprintf("%s bridged to br0 (EUD mode)", iface))
		return
	}

	if internetProbe(iface) {
		consecutiveProbeFailures = 0
	} else {
		consecutiveProbeFailures++
		if consecutiveProbeFailures >= probeFailThreshold {
			demoteGateway(fmt.Sprintf("internet unreachable via %s (%d consecutive failures)", iface, consecutiveProbeFailures))
			return
		}
		log.Printf("internet probe failed on %s (%d/%d)", iface, consecutiveProbeFailures, probeFailThreshold)
	}

	if cfg.Bandwidth != "" {
		cur := strings.TrimSpace(runOut("batctl", "gw_mode"))
		if !strings.Contains(cur, cfg.Bandwidth) {
			run("batctl", "gw_mode", "server", cfg.Bandwidth)
			log.Printf("batman gateway bandwidth set to %s", cfg.Bandwidth)
		}
	}

	if cfg.NAT {
		if !natPresent(iface) {
			applyNAT(iface, cfg.MSSClamp)
		}
	} else {
		if natHasAnyMasquerade() {
			clearNAT()
		}
	}

	cur := runOut("ip", "route", "show", "default")
	if strings.Contains(cur, "dev br0") {
		run("ip", "route", "del", "default", "dev", "br0")
		log.Println("removed conflicting br0 default route")
	}
}

func pollClient(cfg Config) {
	if natHasAnyMasquerade() || filterTableExists() {
		clearNAT()
	}

	gwMAC := batmanGatewayMAC()
	if gwMAC == "" {
		// No node is currently announcing gateway status. A default route
		// installed on an earlier poll would still point at a node that
		// stopped NATing — traffic would silently black-hole instead of
		// failing visibly — so withdraw it rather than leaving it in place.
		cur := runOut("ip", "route", "show", "default")
		if strings.Contains(cur, "dev br0") {
			run("ip", "route", "del", "default", "dev", "br0")
			log.Println("withdrew stale default route (no gateway announced)")
		}
		return
	}

	gwIP := lookupRegistryIP(gwMAC)
	if gwIP == "" {
		return
	}

	localIP := br0IPv4()
	if localIP == "" {
		return
	}

	cur := runOut("ip", "route", "show", "default")
	if strings.Contains(cur, "via "+gwIP+" dev br0") {
		return
	}

	if !pingReachable(gwIP) {
		return
	}

	run("ip", "route", "replace", "default", "via", gwIP, "dev", "br0", "src", localIP)
	log.Printf("default route → %s via br0 (src %s)", gwIP, localIP)
}

// --- Gateway state ---

func isGateway() bool {
	_, err1 := os.Stat(gatewayStateFile)
	_, err2 := os.Stat(uplinkStateFile)
	return err1 == nil || err2 == nil
}

func upstreamIface() string {
	if data, err := os.ReadFile(uplinkStateFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "UPLINK_IFACE=") {
				return strings.TrimPrefix(line, "UPLINK_IFACE=")
			}
		}
	}
	if data, err := os.ReadFile(upstreamIfaceFile); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

// --- NAT / firewall ---

func natPresent(iface string) bool {
	out := runOut("nft", "list", "chain", "ip", "nat", "postrouting")
	return strings.Contains(out, fmt.Sprintf("oifname \"%s\" masquerade", iface))
}

func natHasAnyMasquerade() bool {
	out := runOut("nft", "list", "chain", "ip", "nat", "postrouting")
	return strings.Contains(out, "masquerade")
}

func filterTableExists() bool {
	out := runOut("nft", "list", "tables")
	return strings.Contains(out, "inet filter")
}

func applyNAT(iface string, mssClamp bool) {
	run("sysctl", "-q", "net.ipv4.ip_forward=1")

	run("nft", "add", "table", "ip", "nat")
	run("nft", "add", "chain", "ip", "nat", "postrouting",
		"{ type nat hook postrouting priority srcnat; policy accept; }")
	run("nft", "flush", "chain", "ip", "nat", "postrouting")
	run("nft", "add", "rule", "ip", "nat", "postrouting",
		"oifname", iface, "masquerade")

	if mssClamp {
		run("nft", "add", "table", "ip", "mangle")
		run("nft", "add", "chain", "ip", "mangle", "forward",
			"{ type filter hook forward priority mangle; policy accept; }")
		run("nft", "flush", "chain", "ip", "mangle", "forward")
		run("nft", "add", "rule", "ip", "mangle", "forward",
			"tcp", "flags", "syn", "tcp", "option", "maxseg", "size", "set", "rt", "mtu")
	}

	log.Printf("NAT masquerade active on %s (mss_clamp=%v)", iface, mssClamp)
	go meshHook("gateway-up", "IFACE="+iface)
}

func clearNAT() {
	run("nft", "delete", "table", "inet", "filter")
	run("nft", "flush", "chain", "ip", "nat", "postrouting")
	run("nft", "flush", "chain", "ip", "mangle", "forward")
	log.Println("NAT/firewall rules cleared")
	go meshHook("gateway-down")
}

// --- Batman gateway discovery ---

var gwListRE = regexp.MustCompile(`^\*\s+([0-9a-fA-F:]{17})`)

func batmanGatewayMAC() string {
	out := runOut("batctl", "gwl")
	for _, line := range strings.Split(out, "\n") {
		if m := gwListRE.FindStringSubmatch(line); len(m) > 1 {
			return strings.ToLower(m[1])
		}
	}
	return ""
}

// --- Registry lookup ---

var registryRE = regexp.MustCompile(`NODE_([A-Fa-f0-9]+)_([A-Z0-9_]+)='([^']*)'`)

func lookupRegistryIP(mac string) string {
	data, err := os.ReadFile(registryFile)
	if err != nil {
		return ""
	}
	mac = strings.ToLower(mac)
	nodes := make(map[string]map[string]string)
	for _, m := range registryRE.FindAllStringSubmatch(string(data), -1) {
		id, field, val := m[1], m[2], m[3]
		if nodes[id] == nil {
			nodes[id] = make(map[string]string)
		}
		nodes[id][field] = val
	}
	for _, n := range nodes {
		macs := strings.ToLower(n["MAC_ADDRESSES"])
		if strings.Contains(macs, mac) {
			if ip := n["IPV4_ADDRESS"]; ip != "" {
				return ip
			}
		}
	}
	return ""
}

// --- Network helpers ---

func br0IPv4() string {
	out := runOut("ip", "-4", "-o", "addr", "show", "dev", "br0")
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		for i, f := range fields {
			if f == "inet" && i+1 < len(fields) {
				parts := strings.SplitN(fields[i+1], "/", 2)
				return parts[0]
			}
		}
	}
	return ""
}

func pingReachable(ip string) bool {
	err := exec.Command("ping", "-c", "1", "-W", "1", ip).Run()
	return err == nil
}

func internetProbe(iface string) bool {
	for _, target := range probeTargets {
		if exec.Command("ping", "-c", "1", "-W", "2", "-I", iface, target).Run() == nil {
			return true
		}
	}

	// Some uplinks filter or rate-limit ICMP to public IPs while TCP/UDP
	// pass through fine — a ping-only probe flapped gateway mode on and off
	// on an otherwise working LAN. Fall back to an HTTPS request and finally
	// a bare TCP connect before concluding the uplink is actually down.
	// probeTargets doubles as the fallback target list: 1.1.1.1, 8.8.8.8,
	// and 9.9.9.9 all serve DNS-over-HTTPS on :443, so both checks hit a
	// real listening service without introducing a DNS dependency.
	for _, target := range probeTargets {
		if exec.Command("curl", "--interface", iface, "--connect-timeout", "2",
			"-s", "-k", "-o", "/dev/null", "https://"+target+"/").Run() == nil {
			return true
		}
	}
	for _, target := range probeTargets {
		if exec.Command("curl", "--interface", iface, "--connect-timeout", "2",
			"-s", "telnet://"+target+":443").Run() == nil {
			return true
		}
	}
	return false
}

func ifaceHasCarrier(iface string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/carrier", iface))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}

func ifaceBridgedToBr0(iface string) bool {
	out := runOut("ip", "link", "show", iface)
	return strings.Contains(out, "master br0")
}

func demoteGateway(reason string) {
	log.Printf("demoting gateway: %s", reason)
	consecutiveProbeFailures = 0
	os.Remove(gatewayStateFile)
	os.Remove(uplinkStateFile)
	run("batctl", "gw_mode", "client")
	clearNAT()
	exec.Command("/usr/local/bin/manet-uplink-dispatch.sh", "reconcile").Run()
}

// --- Config file loading ---

func loadKV(path string) map[string]string {
	m := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		val = strings.Trim(val, "'\"")
		m[key] = val
	}
	return m
}

// --- Command helpers ---

func run(name string, args ...string) {
	exec.Command(name, args...).Run()
}

func runOut(name string, args ...string) string {
	out, _ := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(out))
}

// --- EUD bandwidth shaping ---

var lastEUDBwState string

func enforceEUDBandwidth(capMbit string) {
	iface := "br0"
	ifbDev := "ifb0"

	if capMbit == "" {
		if lastEUDBwState != "" {
			run("tc", "qdisc", "del", "dev", iface, "root")
			run("tc", "qdisc", "del", "dev", iface, "ingress")
			run("tc", "qdisc", "del", "dev", ifbDev, "root")
			run("ip", "link", "set", ifbDev, "down")
			lastEUDBwState = ""
			log.Println("EUD bandwidth caps removed")
		}
		return
	}

	leaseIPs := readLeaseIPs()
	stateKey := capMbit + ":" + strings.Join(leaseIPs, ",")
	if stateKey == lastEUDBwState {
		return
	}

	rate := capMbit + "mbit"
	run("tc", "qdisc", "del", "dev", iface, "root")
	run("tc", "qdisc", "del", "dev", iface, "ingress")
	run("tc", "qdisc", "del", "dev", ifbDev, "root")

	if len(leaseIPs) == 0 {
		lastEUDBwState = stateKey
		return
	}

	// Download shaping (br0 egress)
	run("tc", "qdisc", "add", "dev", iface, "root", "handle", "1:", "htb", "default", "99")
	run("tc", "class", "add", "dev", iface, "parent", "1:", "classid", "1:99", "htb", "rate", "1000mbit")

	for i, ip := range leaseIPs {
		classID := fmt.Sprintf("1:%d", 10+i)
		handleID := fmt.Sprintf("%d:", 10+i)
		run("tc", "class", "add", "dev", iface, "parent", "1:", "classid", classID, "htb", "rate", rate, "ceil", rate)
		run("tc", "qdisc", "add", "dev", iface, "parent", classID, "handle", handleID, "sfq", "perturb", "10")
		run("tc", "filter", "add", "dev", iface, "parent", "1:", "protocol", "ip", "prio", "1", "u32", "match", "ip", "dst", ip+"/32", "flowid", classID)
	}

	// Upload shaping (br0 ingress via IFB redirect)
	run("modprobe", "ifb")
	run("ip", "link", "add", ifbDev, "type", "ifb")
	run("ip", "link", "set", ifbDev, "up")
	run("tc", "qdisc", "add", "dev", iface, "handle", "ffff:", "ingress")
	run("tc", "filter", "add", "dev", iface, "parent", "ffff:", "protocol", "ip", "u32",
		"match", "u32", "0", "0", "action", "mirred", "egress", "redirect", "dev", ifbDev)

	run("tc", "qdisc", "add", "dev", ifbDev, "root", "handle", "1:", "htb", "default", "99")
	run("tc", "class", "add", "dev", ifbDev, "parent", "1:", "classid", "1:99", "htb", "rate", "1000mbit")

	for i, ip := range leaseIPs {
		classID := fmt.Sprintf("1:%d", 10+i)
		handleID := fmt.Sprintf("%d:", 10+i)
		run("tc", "class", "add", "dev", ifbDev, "parent", "1:", "classid", classID, "htb", "rate", rate, "ceil", rate)
		run("tc", "qdisc", "add", "dev", ifbDev, "parent", classID, "handle", handleID, "sfq", "perturb", "10")
		run("tc", "filter", "add", "dev", ifbDev, "parent", "1:", "protocol", "ip", "prio", "1", "u32", "match", "ip", "src", ip+"/32", "flowid", classID)
	}

	lastEUDBwState = stateKey
	log.Printf("EUD bandwidth caps applied: %s mbit symmetric for %d devices", capMbit, len(leaseIPs))
}

func readLeaseIPs() []string {
	now := time.Now().Unix()
	for _, path := range []string{"/var/lib/misc/dnsmasq.leases", "/tmp/dnsmasq.leases", "/run/dnsmasq.leases"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var ips []string
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			exp := int64(0)
			fmt.Sscanf(fields[0], "%d", &exp)
			if exp > 0 && exp < now {
				continue
			}
			ips = append(ips, fields[2])
		}
		return ips
	}
	return nil
}

func meshHook(event string, args ...string) {
	cmdArgs := append([]string{event}, args...)
	exec.Command("/usr/local/bin/mesh-hook", cmdArgs...).Run()
}
