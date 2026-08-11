package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

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

	ip := stateIP(state)
	if ip == "" {
		ip = myReg["IPV4_ADDRESS"]
	}

	gps := getGPS(myReg)
	throttle := getThrottle()

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
	}
}

func assembleStatusData() StatusData {
	conf := loadKVFile(MeshConfFile)
	state := loadKVFile(MeshStateFile)
	registry := parseRegistry()
	myMAC := getMyMAC()
	myHostname := getMyHostname()
	myBattery := getBattery()

	tqMap, origMap := runBatctlOriginators()
	neighbors := runBatctlNeighbors()
	gateways := runBatctlGateways()

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
			Uptime:       uptimeStr,
			CPU:          cpuStr,
			Battery:      bat,
			Mumble:       rn["IS_MUMBLE_SERVER"] == "true",
			MediaMTX:     rn["IS_MEDIAMTX_SERVER"] == "true",
			NTP:          rn["IS_NTP_SERVER"] == "true",
			State:        rn["NODE_STATE"],
			Ch2G:         rn["DATA_CHANNEL_2_4"],
			Ch5G:         rn["DATA_CHANNEL_5_0"],
			Limp:         rn["IS_IN_LIMP_MODE"] == "true",
			AllMACs:      allMACs,
			BestLink:     bestLink,
			LastSeen:     rn["LAST_SEEN_TIMESTAMP"],
		}

		for _, m := range allMACs {
			macToNodeID[m] = rn["id"]
		}

		nodes = append(nodes, node)
		nodeByID[rn["id"]] = &nodes[len(nodes)-1]
	}

	// Merge batctl-discovered nodes not already covered by registry
	knownMACs := make(map[string]bool)
	knownMACs[myMAC] = true
	for _, n := range nodes {
		for _, m := range n.AllMACs {
			knownMACs[m] = true
		}
	}
	for mac, orig := range origMap {
		if knownMACs[mac] {
			continue
		}
		knownMACs[mac] = true
		tq := orig.TQ
		node := Node{
			ID:       mac,
			Hostname: mac,
			MAC:      mac,
			TQ:       &tq,
			IsDirect: neighborMACs[mac],
			AllMACs:  []string{mac},
			BestLink: map[string]interface{}{"iface": orig.Iface, "nexthop": orig.Nexthop, "tq": orig.TQ},
		}
		macToNodeID[mac] = mac
		nodes = append(nodes, node)
		nodeByID[mac] = &nodes[len(nodes)-1]
	}
	for _, nb := range neighbors {
		if knownMACs[nb.MAC] {
			continue
		}
		knownMACs[nb.MAC] = true
		tq := nb.TQ
		node := Node{
			ID:       nb.MAC,
			Hostname: nb.MAC,
			MAC:      nb.MAC,
			TQ:       &tq,
			IsDirect: true,
			AllMACs:  []string{nb.MAC},
			BestLink: make(map[string]interface{}),
		}
		macToNodeID[nb.MAC] = nb.MAC
		nodes = append(nodes, node)
		nodeByID[nb.MAC] = &nodes[len(nodes)-1]
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
			Battery:  myBattery,
			Uptime:   selfUptime,
			AllMACs:  []string{myMAC},
			BestLink: make(map[string]interface{}),
			LastSeen: "",
		}
		nodes = append([]Node{selfNode}, nodes...)
	}

	// Attach local applets to self node
	localApplets := scanLocalApplets()
	for i := range nodes {
		if nodes[i].IsMe {
			nodes[i].Applets = localApplets
			break
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
			edges = append(edges, Edge{Source: selfID, Target: n.ID, Type: "direct", TQ: &tq})
		} else {
			via := orig.Nexthop
			tq := orig.TQ
			edges = append(edges, Edge{Source: selfID, Target: n.ID, Type: "multihop", Via: via, TQ: &tq})
			if viaNodeID, ok := macToNodeID[via]; ok {
				edges = append(edges, Edge{Source: viaNodeID, Target: n.ID, Type: "inferred", TQ: &tq})
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
		Edges:        edges,
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
