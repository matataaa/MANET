import re
from .config import REGISTRY_FILE


def norm_mac(mac):
    return mac.lower().replace('-', ':').strip()


def parse_registry():
    """Parse /var/run/mesh_node_registry into a dict of node dicts."""
    nodes = {}
    try:
        with open(REGISTRY_FILE) as f:
            content = f.read()
        pattern = re.compile(r"NODE_([A-Fa-f0-9]+)_([A-Z0-9_]+)='([^']*)'")
        for m in pattern.finditer(content):
            nid, field, value = m.groups()
            if nid not in nodes:
                nodes[nid] = {'id': nid}
            nodes[nid][field] = value
    except Exception:
        pass
    return nodes
