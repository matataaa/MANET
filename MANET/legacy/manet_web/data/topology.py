import json
import time

from .config import load_kv_file, MESH_CONF_FILE, MESH_STATE_FILE
from .registry import norm_mac, parse_registry
from .batman import run_batctl_originators, run_batctl_neighbors, run_batctl_gateways, best_orig_entry_for_node, resolve_hop_count
from .interfaces import get_interfaces, enrich_interfaces_with_registry_mcs
from .system import get_my_mac, get_my_hostname, get_battery, get_connected_euds, get_running_services, get_local_uptime, fmt_uptime, get_peer_local_data


def assemble_local_data():
    conf     = load_kv_file(MESH_CONF_FILE)
    state    = load_kv_file(MESH_STATE_FILE)
    hostname = get_my_hostname()
    battery  = get_battery()
    ifaces   = get_interfaces()
    euds     = get_connected_euds()
    services = get_running_services()
    uptime   = get_local_uptime()

    # Pull self entry from registry for extra fields
    nodes_raw = parse_registry()
    my_mac    = get_my_mac()
    my_node   = {}
    for nid, ndata in nodes_raw.items():
        if norm_mac(ndata.get('MAC_ADDRESS', '')) == my_mac or ndata.get('HOSTNAME', '') == hostname:
            my_node = ndata
            break

    ifaces = enrich_interfaces_with_registry_mcs(ifaces, my_node)

    # GPS — read directly from gps-reader output for freshness; fall back to registry
    gps = {'available': False, 'lat': '', 'lon': '', 'alt': ''}
    try:
        with open('/run/gps_status.json') as _gf:
            _gd = json.load(_gf)
        if _gd.get('has_fix'):
            gps = {
                'available': True,
                'lat': str(_gd['latitude']),
                'lon': str(_gd['longitude']),
                'alt': str(_gd.get('altitude', '')),
            }
    except Exception:
        pass
    if not gps['available']:
        gps = {
            'available': bool(my_node.get('GPS_LATITUDE')),
            'lat':       my_node.get('GPS_LATITUDE', ''),
            'lon':       my_node.get('GPS_LONGITUDE', ''),
            'alt':       my_node.get('GPS_ALTITUDE', ''),
        }

    return {
        'hostname':  hostname,
        'ip':        (state.get('CURRENT_IPV4') or my_node.get('IPV4_ADDRESS', '')),
        'mac':       my_mac or '',
        'uptime':    uptime,
        'battery':   battery,
        'gps':       gps,
        'interfaces': ifaces,
        'euds':      euds,
        'services':  services,
        'eud_mode':  conf.get('eud', 'wired'),
        'ap_ssid':   conf.get('lan_ap_ssid', ''),
        'mesh_ssid': conf.get('mesh_ssid', ''),
    }


def assemble_status_data():
    conf       = load_kv_file(MESH_CONF_FILE)
    state      = load_kv_file(MESH_STATE_FILE)
    nodes_raw  = parse_registry()
    local_battery = get_battery()
    orig_tq, orig_map = run_batctl_originators()
    neighbors  = run_batctl_neighbors()
    gateways   = run_batctl_gateways()
    my_mac     = get_my_mac()
    my_host    = get_my_hostname()

    neighbor_macs = {n['mac'] for n in neighbors}
    gw_mac_map    = {g['mac']: g for g in gateways}
    selected_gw   = next((g['mac'] for g in gateways if g['selected']), None)

    # If batctl gwl has no gateways, fall back to registry IS_GATEWAY flags.
    # This covers nodes that have internet but haven't configured batctl gw server.
    registry_gw_nodes = [
        nid for nid, nd in nodes_raw.items()
        if nd.get('IS_GATEWAY', 'false').lower() == 'true'
    ]
    if not gateways and registry_gw_nodes:
        # Use the registry gateway with best TQ as the "selected" gateway
        def _node_tq(nid):
            nd = nodes_raw[nid]
            for m in nd.get('MAC_ADDRESSES', '').split(','):
                t = orig_tq.get(norm_mac(m.strip()))
                if t is not None:
                    return t
            return 0
        best_gw_nid = max(registry_gw_nodes, key=_node_tq)
        best_gw_nd  = nodes_raw[best_gw_nid]
        selected_gw = norm_mac(best_gw_nd.get('MAC_ADDRESS', ''))
        gateways    = [{'mac': selected_gw, 'tq': 0, 'selected': True}]

    node_list = []
    self_found = False

    for nid, ndata in nodes_raw.items():
        raw_mac  = ndata.get('MAC_ADDRESS', '')
        node_mac = norm_mac(raw_mac)
        hostname = ndata.get('HOSTNAME', 'unknown')

        # TQ: check primary mac then all macs
        tq = orig_tq.get(node_mac)
        if tq is None:
            for alt_mac in ndata.get('MAC_ADDRESSES', '').split(','):
                alt = norm_mac(alt_mac)
                if alt in orig_tq:
                    tq = orig_tq[alt]
                    break

        is_me = (my_mac and node_mac == my_mac) or (hostname == my_host)
        if is_me:
            tq = None
            self_found = True

        battery = {'percentage': int(ndata['BATTERY_PERCENTAGE'])} if ndata.get('BATTERY_PERCENTAGE') else None
        if is_me and local_battery and local_battery.get('percentage') is not None:
            battery = local_battery
        elif not is_me and ndata.get('IPV4_ADDRESS'):
            peer_battery = get_peer_local_data(ndata.get('IPV4_ADDRESS'), timeout=0.8).get('battery')
            if isinstance(peer_battery, dict) and peer_battery.get('percentage') is not None:
                battery = peer_battery

        all_node_macs = [norm_mac(m) for m in ndata.get('MAC_ADDRESSES', '').split(',') if m.strip()]
        is_direct = any(m in neighbor_macs for m in all_node_macs) or node_mac in neighbor_macs
        gw_info   = gw_mac_map.get(node_mac)
        best_link = {}
        if not is_me:
            for omac, odata in orig_map.items():
                if omac == node_mac or omac in all_node_macs:
                    if not best_link or odata.get('tq', 0) > best_link.get('tq', 0):
                        best_link = {
                            'iface':   odata.get('iface', ''),
                            'nexthop': odata.get('nexthop', ''),
                            'tq':      odata.get('tq'),
                        }

        node_list.append({
            'id':           nid,
            'hostname':     hostname,
            'mac':          raw_mac,
            'ip':           ndata.get('IPV4_ADDRESS', ''),
            'tq':           tq,
            'is_me':        is_me,
            'is_direct':    is_direct or is_me,
            'is_gateway':   ndata.get('IS_GATEWAY', 'false').lower() == 'true',
            'is_selected_gw': bool(selected_gw and (
                node_mac == selected_gw or
                selected_gw in [norm_mac(m) for m in ndata.get('MAC_ADDRESSES','').split(',') if m.strip()]
            )),
            'uptime':       fmt_uptime(ndata.get('UPTIME_SECONDS', '')),
            'cpu':          (f"{float(ndata['CPU_LOAD_AVERAGE']):.2f}" if ndata.get('CPU_LOAD_AVERAGE') else ''),
            'battery':      battery,
            'mumble':       ndata.get('IS_MUMBLE_SERVER', 'false').lower() == 'true',
            'mediamtx':     ndata.get('IS_MEDIAMTX_SERVER', 'false').lower() == 'true',
            'ntp':          ndata.get('IS_NTP_SERVER', 'false').lower() == 'true',
            'state':        ndata.get('NODE_STATE', 'ACTIVE'),
            'ch_2g':        ndata.get('DATA_CHANNEL_2_4', ''),
            'ch_5g':        ndata.get('DATA_CHANNEL_5_0', ''),
            'limp':         ndata.get('IS_IN_LIMP_MODE', 'false').lower() == 'true',
            'all_macs':     [norm_mac(m) for m in ndata.get('MAC_ADDRESSES', '').split(',') if m.strip()],
            'best_link':    best_link,
            'hop_count':    None,
            'last_seen':    ndata.get('LAST_REGISTRY_UPDATE', ndata.get('LAST_SEEN_TIMESTAMP', '0')),
        })

    # If self not in registry, inject a placeholder
    if not self_found and my_host:
        node_list.insert(0, {
            'id': 'self',
            'hostname': my_host,
            'mac': my_mac or '',
            'ip': (state.get('CURRENT_IPV4') or ''),
            'tq': None, 'is_me': True, 'is_direct': True,
            'is_gateway': False, 'is_selected_gw': False,
            'uptime': '', 'cpu': '', 'battery': None,
            'mumble': False, 'mediamtx': False, 'ntp': False,
            'state': 'ACTIVE', 'ch_2g': '', 'ch_5g': '', 'limp': False,
            'hop_count': None, 'last_seen': str(int(time.time())),
        })

    node_list.sort(key=lambda n: (not n['is_me'], -(n['tq'] if n['tq'] is not None else -1)))

    # ── Build topology edges from batctl o nexthop data ──
    # mac_to_node_id: every MAC (all interfaces) -> node_id
    mac_to_node_id = {}
    for node in node_list:
        for m in node.get('all_macs', []):
            mac_to_node_id[m] = node['id']
        mac_to_node_id[norm_mac(node['mac'])] = node['id']
    node_by_id = {node['id']: node for node in node_list}

    for node in node_list:
        if node.get('is_me'):
            continue
        node['hop_count'] = resolve_hop_count(node['id'], node_by_id, orig_map, mac_to_node_id)

    edges = []
    self_node = next((n for n in node_list if n['is_me']), None)
    if self_node:
        for node in node_list:
            if node['is_me']:
                continue
            node_all_macs = set(node.get('all_macs', []))

            # Find the best orig_map entry for this node (match any of its MACs)
            best_entry = None
            for omac, odata in orig_map.items():
                if omac in node_all_macs:
                    if best_entry is None or odata['tq'] > best_entry['tq']:
                        best_entry = odata

            if best_entry is None:
                # Node in registry but not in originator table — no path known
                edges.append({
                    'source':  self_node['id'],
                    'target':  node['id'],
                    'type':    'unknown',
                    'via':     None,
                    'tq':      node['tq'],
                })
                continue

            nexthop = best_entry['nexthop']
            # Is nexthop one of this node's own MACs? -> direct
            if nexthop in node_all_macs or node['is_direct']:
                edges.append({
                    'source': self_node['id'],
                    'target': node['id'],
                    'type':   'direct',
                    'via':    None,
                    'tq':     node['tq'],
                })
            else:
                # nexthop is a different node -> multi-hop via that node
                via_id = mac_to_node_id.get(nexthop)
                edges.append({
                    'source': self_node['id'],
                    'target': node['id'],
                    'type':   'multihop',
                    'via':    via_id,
                    'tq':     node['tq'],
                })

        # Also add edges between non-self nodes where we can infer adjacency:
        # if node A routes to node C via node B, we know B->C exists
        inferred = set()
        for edge in list(edges):
            if edge['type'] == 'multihop' and edge['via']:
                pair = tuple(sorted([edge['via'], edge['target']]))
                if pair not in inferred:
                    inferred.add(pair)
                    # TQ for inferred edge: use target node's tq (conservative)
                    target_node = next((n for n in node_list if n['id'] == edge['target']), None)
                    edges.append({
                        'source': edge['via'],
                        'target': edge['target'],
                        'type':   'inferred',
                        'via':    None,
                        'tq':     target_node['tq'] if target_node else None,
                    })

    return {
        'nodes':          node_list,
        'my_mac':         my_mac or '',
        'my_hostname':    my_host,
        'my_ip':          next((n['ip'] for n in node_list if n.get('is_me')), '') or (state.get('CURRENT_IPV4') or ''),
        'mesh_ssid':      conf.get('mesh_ssid', ''),
        'network':        conf.get('ipv4_network', ''),
        'gateway_count':  len(gateways),  # includes registry fallback
        'selected_gw':    selected_gw or '',
        'neighbors':      neighbors,
        'edges':          edges,
        'timestamp':      int(time.time()),
    }
