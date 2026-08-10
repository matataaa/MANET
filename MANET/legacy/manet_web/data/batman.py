import subprocess
import re

from .registry import norm_mac


def run_batctl_originators():
    """Parse `batctl o` into two structures:
      tq_map:   {mac -> best_tq_norm}  (indexes both orig + nexthop MACs)
      orig_map: {orig_mac -> {tq, nexthop, iface}}  (best path per originator)

    BATMAN_V reports throughput in Mbit/s (>255); BATMAN_IV uses 0-255 LQ.
    Both are normalised to 0-255.
    """
    tq_map   = {}
    orig_map = {}  # orig_mac -> {'tq': int, 'nexthop': str, 'iface': str}

    def _set_tq(mac, tq):
        if mac and (mac not in tq_map or tq > tq_map[mac]):
            tq_map[mac] = tq

    try:
        r = subprocess.run(['batctl', 'o', '-n'],
                           capture_output=True, text=True, timeout=5)
        orig_best = {}  # orig_mac -> (best_tq_float, nexthop_mac, outgoing_iface)
        for line in r.stdout.splitlines():
            m = re.match(
                r'[\s*]+([0-9a-f:]{17})\s+[\d.]+(?:ms|s)\s+\(\s*([\d.]+)\)\s+([0-9a-f:]{17})(?:\s+\[\s*(\S+)\s*\])?',
                line)
            if m:
                orig    = norm_mac(m.group(1))
                tq      = float(m.group(2))
                nexthop = norm_mac(m.group(3))
                iface   = (m.group(4) or '').strip()
                prev = orig_best.get(orig)
                if prev is None or tq > prev[0]:
                    orig_best[orig] = (tq, nexthop, iface)

        for orig, (tq, nexthop, iface) in orig_best.items():
            tq_norm = int(min(tq / 1000 * 255, 255)) if tq > 255 else int(tq)
            _set_tq(orig, tq_norm)
            if nexthop != orig:
                _set_tq(nexthop, tq_norm)
            orig_map[orig] = {'tq': tq_norm, 'nexthop': nexthop, 'iface': iface}
    except Exception:
        pass
    return tq_map, orig_map


def run_batctl_neighbors():
    """Return list of {iface, mac, tq} from `batctl n`."""
    neighbors = []
    try:
        r = subprocess.run(['batctl', 'n', '-n'],
                           capture_output=True, text=True, timeout=5)
        for line in r.stdout.splitlines():
            # wlan0   aa:bb:cc:dd:ee:ff   0.500ms   (240)
            m = re.match(r'\s*(\S+)\s+([0-9a-f:]{17})\s+[\d.]+(?:ms|s)\s+\(\s*([\d.]+)\)', line)
            if m:
                raw_tq = float(m.group(3))
                tq_norm = int(min(raw_tq / 1000 * 255, 255)) if raw_tq > 255 else int(raw_tq)
                neighbors.append({
                    'iface': m.group(1),
                    'mac':   norm_mac(m.group(2)),
                    'tq':    tq_norm
                })
    except Exception:
        pass
    return neighbors


def run_batctl_gateways():
    """Return list of {mac, tq, selected} from `batctl gwl`.

    BATMAN_V format:
      => <gw_mac>  <age>s (  <Mbit/s>)  <nexthop_mac> [<if>]
    BATMAN_IV format:
      => <gw_mac>  <age>ms (<lq>)  <nexthop_mac> [<if>]
    Header lines and the self-node are skipped.
    """
    gateways = []
    try:
        r = subprocess.run(['batctl', 'gwl', '-n'],
                           capture_output=True, text=True, timeout=5)
        for line in r.stdout.splitlines():
            # Skip header / blank lines
            if not line.strip() or line.startswith('[') or line.strip().startswith('Router'):
                continue
            selected = line.lstrip().startswith('=>')
            # Extract first MAC on the line (the gateway's originator MAC)
            mac_m = re.search(r'([0-9a-f]{2}(?::[0-9a-f]{2}){5})', line)
            # Extract throughput/LQ: handles both "( 100.0)" and "(255)"
            tq_m  = re.search(r'\(\s*([\d.]+)\s*\)', line)
            if mac_m:
                raw_tq = float(tq_m.group(1)) if tq_m else 0.0
                tq_norm = int(min(raw_tq / 1000 * 255, 255)) if raw_tq > 255 else int(raw_tq)
                gateways.append({
                    'mac':      norm_mac(mac_m.group(1)),
                    'tq':       tq_norm,
                    'selected': selected,
                })
    except Exception:
        pass
    return gateways


def best_orig_entry_for_node(node, orig_map):
    if not isinstance(node, dict):
        return None
    node_all_macs = set(node.get('all_macs', []))
    raw_mac = norm_mac(node.get('mac', ''))
    if raw_mac:
        node_all_macs.add(raw_mac)
    best_entry = None
    for omac, odata in orig_map.items():
        if omac in node_all_macs:
            if best_entry is None or odata.get('tq', 0) > best_entry.get('tq', 0):
                best_entry = odata
    return best_entry


def resolve_hop_count(node_id, node_by_id, orig_map, mac_to_node_id, visited=None):
    if visited is None:
        visited = set()
    if node_id in visited:
        return None
    visited.add(node_id)

    node = node_by_id.get(node_id)
    if not node or node.get('is_me'):
        return None
    if node.get('is_direct'):
        return 1

    best_entry = best_orig_entry_for_node(node, orig_map)
    if not best_entry:
        return None

    node_all_macs = set(node.get('all_macs', []))
    raw_mac = norm_mac(node.get('mac', ''))
    if raw_mac:
        node_all_macs.add(raw_mac)

    nexthop = norm_mac(best_entry.get('nexthop', ''))
    if not nexthop:
        return None
    if nexthop in node_all_macs:
        return 1

    via_id = mac_to_node_id.get(nexthop)
    if not via_id or via_id == node_id:
        return None

    via_hops = resolve_hop_count(via_id, node_by_id, orig_map, mac_to_node_id, visited)
    if via_hops is None:
        return None
    return via_hops + 1
