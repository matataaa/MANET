package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	statusCacheMu   sync.Mutex
	statusCache     *StatusData
	statusCacheTime time.Time
	statusCacheTTL  = 3 * time.Second

	localCacheMu   sync.Mutex
	localCache     *LocalData
	localCacheTime time.Time
	localCacheTTL  = 3 * time.Second

	batSnapMu   sync.Mutex
	batSnap     *batmanSnapshot
	batSnapTime time.Time
	batSnapTTL  = 3 * time.Second
)

// batmanSnapshot is the shared batctl o/n/gwl read used by both the topology
// graph (assembleStatusData) and the mesh tab (apiMesh). These three calls
// were the expensive/duplicated part of /api/mesh — it was re-running them
// fresh on every request even though the topology cache above was already
// polling the exact same tables every 3s. Sharing one cache means an
// uncoordinated mesh-tab refresh can no longer double the batctl load.
type batmanSnapshot struct {
	TQMap     map[string]int
	OrigMap   map[string]BatOriginator
	Neighbors []BatNeighbor
	Gateways  []BatGateway
}

func cachedBatmanSnapshot() batmanSnapshot {
	batSnapMu.Lock()
	defer batSnapMu.Unlock()
	if batSnap != nil && time.Since(batSnapTime) < batSnapTTL {
		return *batSnap
	}
	tqMap, origMap := runBatctlOriginators()
	snap := batmanSnapshot{
		TQMap:     tqMap,
		OrigMap:   origMap,
		Neighbors: runBatctlNeighbors(),
		Gateways:  runBatctlGateways(),
	}
	batSnap = &snap
	batSnapTime = time.Now()
	return snap
}

// computeUplink reports this node's current best throughput toward its
// mesh gateway, for node-update's auto-update bandwidth gate. A node that
// is itself the selected gateway has no mesh hop in the way — report it as
// wired with no ceiling, matching the "ethernet always OK" assumption.
// Otherwise reuse the same batman-adv throughput estimate already computed
// for the topology "Real Rate" column, for the route toward whichever
// gateway is currently selected.
func computeUplink() (mbps float64, uplinkType string) {
	// If this node is itself running as the mesh gateway (gw_mode=server),
	// there's no mesh hop between it and the internet — updates download
	// over its own uplink directly. batman-adv's gwl/originator tables
	// never list a node as a gateway candidate to itself, so this can't be
	// detected via selectedGW below (that comparison was always false in
	// practice) — it has to come from local gw_mode instead.
	if out, err := runCmdStdout(5*time.Second, "batctl", "gw_mode"); err == nil &&
		strings.HasPrefix(strings.TrimSpace(out), "server") {
		return 0, "wired"
	}

	snap := cachedBatmanSnapshot()

	selectedGW := ""
	for _, gw := range snap.Gateways {
		if gw.Selected {
			selectedGW = gw.MAC
			break
		}
	}
	// No selected gateway found is NOT the same as "wired, no ceiling" —
	// that would make the bandwidth gate always pass when we actually have
	// no idea what the link looks like. Report unknown so callers fail
	// closed instead.
	if selectedGW == "" {
		return 0, "unknown"
	}

	orig, ok := snap.OrigMap[selectedGW]
	if !ok || orig.RawTP <= 0 {
		return 0, "unknown"
	}
	uplinkType = "wifi-mesh"
	if orig.Iface == "wlan2" {
		uplinkType = "halow-mesh"
	}
	return orig.RawTP, uplinkType
}

func cachedStatusData() StatusData {
	statusCacheMu.Lock()
	defer statusCacheMu.Unlock()
	if statusCache != nil && time.Since(statusCacheTime) < statusCacheTTL {
		return *statusCache
	}
	data := assembleStatusData()
	statusCache = &data
	statusCacheTime = time.Now()
	return data
}

func cachedLocalData() LocalData {
	localCacheMu.Lock()
	defer localCacheMu.Unlock()
	if localCache != nil && time.Since(localCacheTime) < localCacheTTL {
		return *localCache
	}
	data := assembleLocalData()
	localCache = &data
	localCacheTime = time.Now()
	return data
}

func stateIP(state map[string]string) string {
	if ip := state["CURRENT_IPV4"]; ip != "" {
		return ip
	}
	return state["PERSISTENT_IPV4"]
}

func assembleLocalData() LocalData {
	conf := loadKVFile(MeshConfFile)
	state := loadKVFile(MeshStateFile)
	hostname := getMyHostname()
	battery := getBattery()
	ifaces := getInterfaces()
	euds := getEUDs()
	services := getRunningServices()
	uptime := getUptime()
	registry := parseRegistry()
	myMAC := getMyMAC()

	// Find self in registry
	var myReg RegistryNode
	for _, rn := range registry {
		mac := normMAC(rn["MAC_ADDRESS"])
		if mac == myMAC {
			myReg = rn
			break
		}
		if rn["HOSTNAME"] == hostname {
			myReg = rn
			break
		}
	}
	if myReg == nil {
		myReg = make(RegistryNode)
	}

	ifaces = enrichIfacesWithMCS(ifaces, myReg)
	ifaces = enrichIfacesWithHalow(ifaces)

	ip := stateIP(state)
	if ip == "" {
		ip = myReg["IPV4_ADDRESS"]
	}

	gps := getGPS(myReg)
	throttle := getThrottle()
	network := getNetworkState()
	uplinkMbps, uplinkType := computeUplink()

	return LocalData{
		Hostname:   hostname,
		IP:         ip,
		MAC:        myMAC,
		Uptime:     uptime,
		Battery:    battery,
		GPS:        gps,
		Interfaces: ifaces,
		EUDs:       euds,
		Services:   services,
		EUDMode:    confGet(conf, "eud", "wired"),
		APSSID:     conf["lan_ap_ssid"],
		MeshSSID:   conf["mesh_ssid"],
		Throttle:   throttle,
		Network:    network,
		System:     getSystemStats(),
		Airtime:    currentAirtime(),
		UplinkMbps: uplinkMbps,
		UplinkType: uplinkType,
	}
}

func assembleStatusData() StatusData {
	conf := loadKVFile(MeshConfFile)
	state := loadKVFile(MeshStateFile)
	registry := parseRegistry()
	myMAC := getMyMAC()
	myHostname := getMyHostname()
	myBattery := getBattery()

	snap := cachedBatmanSnapshot()
	tqMap, origMap := snap.TQMap, snap.OrigMap
	neighbors := snap.Neighbors
	gateways := snap.Gateways
	nowTS := fmt.Sprintf("%d", time.Now().Unix())

	// Build neighbor MAC set for direct detection
	neighborMACs := make(map[string]bool)
	for _, n := range neighbors {
		neighborMACs[n.MAC] = true
	}

	// Gateway MAC set
	gwMACs := make(map[string]bool)
	selectedGW := ""
	for _, gw := range gateways {
		gwMACs[gw.MAC] = true
		if gw.Selected {
			selectedGW = gw.MAC
		}
	}

	myIP := stateIP(state)

	// Collect MACs already in registry
	regMACs := make(map[string]bool)
	for _, rn := range registry {
		mac := normMAC(rn["MAC_ADDRESS"])
		if mac != "" {
			regMACs[mac] = true
		}
		for _, m := range strings.Split(rn["MAC_ADDRESSES"], ",") {
			m = normMAC(m)
			if m != "" {
				regMACs[m] = true
			}
		}
	}

	// For batman-visible MACs not in registry, inject cached data so they
	// keep their hostname/IP instead of appearing as bare MACs.
	for mac := range origMap {
		if regMACs[mac] {
			continue
		}
		if cached, ok := getCachedRegistryNode(mac); ok {
			registry[cached["id"]] = cached
		}
	}
	for _, nb := range neighbors {
		if regMACs[nb.MAC] {
			continue
		}
		if cached, ok := getCachedRegistryNode(nb.MAC); ok {
			registry[cached["id"]] = cached
		}
	}

	// Build node list from registry
	var nodes []Node
	macToNodeID := make(map[string]string)
	nodeByID := make(map[string]*Node)
	foundSelf := false

	for _, rn := range registry {
		mac := normMAC(rn["MAC_ADDRESS"])
		var allMACs []string
		allMACs = append(allMACs, mac)
		if extra := rn["MAC_ADDRESSES"]; extra != "" {
			for _, m := range strings.Split(extra, ",") {
				nm := normMAC(m)
				if nm != "" && nm != mac {
					allMACs = append(allMACs, nm)
				}
			}
		}

		isMe := mac == myMAC
		if isMe {
			foundSelf = true
			if myIP == "" {
				myIP = rn["IPV4_ADDRESS"]
			}
		}

		// Check direct connectivity
		isDirect := false
		for _, m := range allMACs {
			if neighborMACs[m] {
				isDirect = true
				break
			}
		}

		// Get TQ
		var tq *int
		if !isMe {
			bestTQ := 0
			for _, m := range allMACs {
				if t, ok := tqMap[m]; ok && t > bestTQ {
					bestTQ = t
				}
			}
			if bestTQ > 0 {
				tq = &bestTQ
			}
		}

		// Is gateway?
		isGW := rn["IS_GATEWAY"] == "true"
		isSelectedGW := false
		for _, m := range allMACs {
			if gwMACs[m] {
				isGW = true
			}
			if m == selectedGW {
				isSelectedGW = true
			}
		}

		// Uptime
		uptimeStr := ""
		if s := rn["UPTIME_SECONDS"]; s != "" {
			if secs, err := strconv.ParseFloat(s, 64); err == nil {
				uptimeStr = fmtUptime(secs)
			}
		}

		// CPU
		cpuStr := ""
		if c := rn["CPU_LOAD_AVERAGE"]; c != "" {
			if v, err := strconv.ParseFloat(c, 64); err == nil {
				cpuStr = fmt.Sprintf("%.2f", v)
			}
		}

		// Battery from peer
		var bat *BatteryInfo
		if isMe {
			bat = myBattery
		} else if pct := rn["BATTERY_PERCENTAGE"]; pct != "" {
			if p, err := strconv.Atoi(pct); err == nil && p > 0 {
				bat = &BatteryInfo{Percentage: &p}
			}
		}

		// Best link
		bestLink := make(map[string]interface{})
		if orig := bestOrigForNode(allMACs, origMap); orig != nil {
			bestLink["iface"] = orig.Iface
			bestLink["nexthop"] = orig.Nexthop
			bestLink["tq"] = orig.TQ
			if orig.RawTP > 0 {
				bestLink["throughput"] = orig.RawTP
			}
		}

		lastSeen := rn["LAST_SEEN_TIMESTAMP"]
		if !isMe && (isDirect || tq != nil) {
			lastSeen = nowTS
		}

		var gps GPS
		if isMe {
			gps = getGPS(rn)
		} else {
			gps = registryGPS(rn)
		}

		node := Node{
			ID:           rn["id"],
			Hostname:     rn["HOSTNAME"],
			MAC:          rn["MAC_ADDRESS"],
			IP:           rn["IPV4_ADDRESS"],
			TQ:           tq,
			IsMe:         isMe,
			IsDirect:     isDirect,
			IsGateway:    isGW,
			IsSelectedGW: isSelectedGW,
			GPS:          gps,
			Uptime:       uptimeStr,
			CPU:          cpuStr,
			Battery:      bat,
			NTP:          rn["IS_NTP_SERVER"] == "true",
			State:        rn["NODE_STATE"],
			Ch2G:         rn["DATA_CHANNEL_2_4"],
			Ch5G:         rn["DATA_CHANNEL_5_0"],
			Limp:         rn["IS_IN_LIMP_MODE"] == "true",
			AllMACs:      allMACs,
			BestLink:     bestLink,
			LastSeen:     lastSeen,
			Applets:      parseAppletsBrief(rn["APPLETS"]),
		}

		for _, m := range allMACs {
			macToNodeID[m] = rn["id"]
		}

		nodes = append(nodes, node)
		nodeByID[rn["id"]] = &nodes[len(nodes)-1]
	}

	// Inject self if not in registry
	if !foundSelf {
		selfUptime := getUptime()
		selfNode := Node{
			ID:       myMAC,
			Hostname: myHostname,
			MAC:      myMAC,
			IP:       myIP,
			IsMe:     true,
			GPS:      getGPS(RegistryNode{}),
			Battery:  myBattery,
			Uptime:   selfUptime,
			AllMACs:  []string{myMAC},
			BestLink: make(map[string]interface{}),
			LastSeen: "",
		}
		nodes = append([]Node{selfNode}, nodes...)
	}

	// Override self applets with fresh local scan (more current than registry)
	if localApplets := scanLocalApplets(); len(localApplets) > 0 {
		for i := range nodes {
			if nodes[i].IsMe {
				nodes[i].Applets = localApplets
				break
			}
		}
	}

	// Sort: self first, then by TQ descending
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].IsMe {
			return true
		}
		if nodes[j].IsMe {
			return false
		}
		ti, tj := 0, 0
		if nodes[i].TQ != nil {
			ti = *nodes[i].TQ
		}
		if nodes[j].TQ != nil {
			tj = *nodes[j].TQ
		}
		return ti > tj
	})

	// Resolve hop counts
	for i := range nodes {
		if !nodes[i].IsMe {
			hops := resolveHopCount(nodes[i], origMap, macToNodeID, nodeByID, nil)
			if hops > 0 {
				h := hops
				nodes[i].HopCount = &h
			}
		}
	}

	// Resolve self node ID (may differ from myMAC when sourced from registry)
	selfID := myMAC
	for _, n := range nodes {
		if n.IsMe {
			selfID = n.ID
			break
		}
	}

	// Build edges using node IDs so D3 forceLink can resolve them
	var edges []Edge
	for _, n := range nodes {
		if n.IsMe {
			continue
		}
		orig := bestOrigForNode(n.AllMACs, origMap)
		if orig == nil {
			if n.State != "" {
				edges = append(edges, Edge{Source: selfID, Target: n.ID, Type: "unknown"})
			}
			continue
		}

		nexthopIsNode := false
		for _, m := range n.AllMACs {
			if m == orig.Nexthop {
				nexthopIsNode = true
				break
			}
		}

		if n.IsDirect || nexthopIsNode {
			tq := orig.TQ
			e := Edge{Source: selfID, Target: n.ID, Type: "direct", TQ: &tq, Iface: orig.Iface}
			if orig.RawTP > 0 {
				tp := orig.RawTP
				e.Throughput = &tp
			}
			edges = append(edges, e)
		} else {
			via := orig.Nexthop
			tq := orig.TQ
			e := Edge{Source: selfID, Target: n.ID, Type: "multihop", Via: via, TQ: &tq, Iface: orig.Iface}
			if orig.RawTP > 0 {
				tp := orig.RawTP
				e.Throughput = &tp
			}
			edges = append(edges, e)
			if viaNodeID, ok := macToNodeID[via]; ok {
				ie := Edge{Source: viaNodeID, Target: n.ID, Type: "inferred", TQ: &tq}
				if orig.RawTP > 0 {
					tp := orig.RawTP
					ie.Throughput = &tp
				}
				edges = append(edges, ie)
			}
		}
	}

	// Build peer-to-peer edges from registry neighbor data
	for _, n := range nodes {
		if n.IsMe {
			continue
		}
		rn := findRegistryNode(registry, n.AllMACs)
		if rn == nil {
			continue
		}
		peerNeighbors := strings.Split(rn["DIRECT_NEIGHBORS"], ",")
		for _, entry := range peerNeighbors {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			// Format: MAC[=tq_or_throughput[=iface]] — the iface segment is
			// newer (mesh-registry may still be running an older build
			// fleet-wide), so parse positionally rather than via
			// LastIndex, which would otherwise misparse "=iface" as the
			// numeric segment and silently drop TQ/throughput for any
			// entry that has it.
			parts := strings.Split(entry, "=")
			nbMAC := parts[0]
			var peerTQ *int
			var peerTP *float64
			var peerIface string
			if len(parts) >= 2 {
				if raw, err := strconv.ParseFloat(parts[1], 64); err == nil {
					tq := normTQ(raw)
					peerTQ = &tq
					if isBatmanV() && raw > 0 {
						peerTP = &raw
					}
				}
			}
			if len(parts) >= 3 {
				peerIface = parts[2]
			}
			nbNodeID, ok := macToNodeID[nbMAC]
			if !ok || nbNodeID == selfID || nbNodeID == n.ID {
				continue
			}
			dup := false
			for _, e := range edges {
				s := e.Source
				t := e.Target
				if (s == n.ID && t == nbNodeID) || (s == nbNodeID && t == n.ID) {
					dup = true
					break
				}
			}
			if !dup {
				edges = append(edges, Edge{Source: n.ID, Target: nbNodeID, Type: "direct", TQ: peerTQ, Throughput: peerTP, Iface: peerIface})
			}
		}
	}

	// Gateway count: from gateways or registry fallback
	gwCount := len(gateways)
	if gwCount == 0 {
		for _, n := range nodes {
			if n.IsGateway {
				gwCount++
			}
		}
	}
	if selectedGW == "" {
		// Fallback: pick best TQ gateway from registry
		bestTQ := 0
		for _, n := range nodes {
			if n.IsGateway && n.TQ != nil && *n.TQ > bestTQ {
				bestTQ = *n.TQ
				selectedGW = n.MAC
			}
		}
	}

	// Mark gateway route edges
	var gwNodeID string
	for _, n := range nodes {
		if n.IsSelectedGW {
			gwNodeID = n.ID
			break
		}
	}
	if gwNodeID != "" {
		var gwVia string
		for i := range edges {
			if edges[i].Source == selfID && edges[i].Target == gwNodeID {
				edges[i].GWRoute = true
				gwVia = edges[i].Via
			}
		}
		if gwVia != "" {
			viaNodeID := macToNodeID[gwVia]
			for i := range edges {
				s, t := edges[i].Source, edges[i].Target
				if (s == viaNodeID && t == gwNodeID) || (s == gwNodeID && t == viaNodeID) {
					edges[i].GWRoute = true
				}
				if (s == selfID && t == viaNodeID) || (s == viaNodeID && t == selfID) {
					edges[i].GWRoute = true
				}
			}
		}
	}

	// Keep all nodes (UI greys out stale ones), but prune edges for offline nodes
	onlineIDs := make(map[string]bool)
	for _, n := range nodes {
		if n.IsMe || n.TQ != nil || n.IsDirect {
			onlineIDs[n.ID] = true
		}
	}
	var activeEdges []Edge
	for _, e := range edges {
		if onlineIDs[e.Source] && onlineIDs[e.Target] {
			activeEdges = append(activeEdges, e)
		}
	}

	return StatusData{
		Nodes:        nodes,
		MyMAC:        myMAC,
		MyHostname:   myHostname,
		MyIP:         myIP,
		MeshSSID:     conf["mesh_ssid"],
		Network:      confGet(conf, "ipv4_network", "10.30.2.0/24"),
		GatewayCount: gwCount,
		SelectedGW:   selectedGW,
		Neighbors:    neighbors,
		Edges:        activeEdges,
		Timestamp:    time.Now().Unix(),
	}
}

func resolveHopCount(n Node, origMap map[string]BatOriginator, macToNodeID map[string]string, nodeByID map[string]*Node, visited map[string]bool) int {
	if n.IsMe {
		return 0
	}
	if n.IsDirect {
		return 1
	}
	if visited == nil {
		visited = make(map[string]bool)
	}
	if visited[n.ID] {
		return 0
	}
	visited[n.ID] = true

	orig := bestOrigForNode(n.AllMACs, origMap)
	if orig == nil {
		return 0
	}

	for _, m := range n.AllMACs {
		if m == orig.Nexthop {
			return 1
		}
	}

	if viaID, ok := macToNodeID[orig.Nexthop]; ok {
		if viaNode, ok2 := nodeByID[viaID]; ok2 {
			hops := resolveHopCount(*viaNode, origMap, macToNodeID, nodeByID, visited)
			if hops > 0 {
				return hops + 1
			}
		}
	}
	return 0
}

func findRegistryNode(registry map[string]RegistryNode, allMACs []string) RegistryNode {
	for _, rn := range registry {
		rnMAC := normMAC(rn["MAC_ADDRESS"])
		for _, m := range allMACs {
			if m == rnMAC {
				return rn
			}
		}
	}
	return nil
}

func getPeerLocalData(peerIP string, timeout time.Duration) map[string]interface{} {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(fmt.Sprintf("http://%s:80/api/local", peerIP))
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return result
}
