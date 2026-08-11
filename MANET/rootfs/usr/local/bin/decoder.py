#!/usr/bin/env python3
import sys
import base64
import argparse
import json
import NodeInfo_pb2


def format_shell_float(value, decimals=2):
    return f"{float(value):.{decimals}f}"


def decode_one(b64_string):
    serialized_message = base64.b64decode(b64_string)
    node_info = NodeInfo_pb2.NodeInfo()
    node_info.ParseFromString(serialized_message)

    lines = []
    lines.append(f"HOSTNAME='{node_info.hostname}'")
    if node_info.mac_addresses:
        lines.append(f"MAC_ADDRESS='{node_info.mac_addresses[0]}'")
    else:
        lines.append("MAC_ADDRESS=''")
    lines.append(f"MAC_ADDRESSES='{','.join(node_info.mac_addresses)}'")
    lines.append(f"IPV4_ADDRESS='{node_info.ipv4_address}'")
    lines.append(f"IPV4_CHUNK={node_info.ipv4_chunk}")
    lines.append(f"SYNCTHING_ID='{node_info.syncthing_id}'")
    lines.append(f"TQ_AVERAGE={node_info.tq_average}")
    lines.append(f"IS_INTERNET_GATEWAY={str(node_info.is_internet_gateway).lower()}")
    lines.append(f"GATEWAY_IFACE='{node_info.gateway_iface}'")
    lines.append(f"IS_MUMBLE_SERVER={str(node_info.is_mumble_server).lower()}")
    lines.append(f"IS_NTP_SERVER={str(node_info.is_ntp_server).lower()}")
    lines.append(f"IS_TAK_SERVER={str(node_info.is_tak_server).lower()}")
    lines.append(f"IS_MEDIAMTX_SERVER={str(node_info.is_mediamtx_server).lower()}")
    lines.append(f"UPTIME_SECONDS={node_info.uptime_seconds}")
    lines.append(f"BATTERY_PERCENTAGE={node_info.battery_percentage}")
    lines.append(f"CPU_LOAD_AVERAGE={format_shell_float(node_info.cpu_load_average)}")
    if node_info.HasField("location"):
        lines.append(f"GPS_LATITUDE='{node_info.location.latitude}'")
        lines.append(f"GPS_LONGITUDE='{node_info.location.longitude}'")
        lines.append(f"GPS_ALTITUDE='{format_shell_float(node_info.location.altitude)}'")
    else:
        lines.append("GPS_LATITUDE=''")
        lines.append("GPS_LONGITUDE=''")
        lines.append("GPS_ALTITUDE=''")
    lines.append(f"DATA_CHANNEL_2_4='{node_info.data_channel_2_4}'")
    lines.append(f"DATA_CHANNEL_5_0='{node_info.data_channel_5_0}'")
    lines.append(f"PARTITION_SIZE={node_info.partition_size}")
    lines.append(f"LAST_SEEN_TIMESTAMP={node_info.last_seen_timestamp}")
    lines.append(f"IS_IN_LIMP_MODE={str(node_info.is_in_limp_mode).lower()}")
    lines.append(f"LAST_TOURGUIDE_TIMESTAMP={node_info.last_tourguide_timestamp}")
    lines.append(f"LAST_TOURGUIDE_RADIO='{node_info.last_tourguide_radio}'")

    state_names = {0: "ACTIVE", 1: "SHUTTING_DOWN"}
    lines.append(f"NODE_STATE='{state_names.get(node_info.node_state, 'ACTIVE')}'")
    lines.append(f"CONFIG_ACK_VERSION='{node_info.config_ack_version}'")
    lines.append(f"HALOW_TX_MCS='{node_info.halow_tx_mcs}'")
    lines.append(f"HALOW_RX_MCS='{node_info.halow_rx_mcs}'")
    lines.append(f"HALOW_MCS_PEER='{node_info.halow_mcs_peer}'")
    lines.append(f"WIFI_24_TX_MCS='{node_info.wifi_24_tx_mcs}'")
    lines.append(f"WIFI_24_RX_MCS='{node_info.wifi_24_rx_mcs}'")
    lines.append(f"WIFI_5_TX_MCS='{node_info.wifi_5_tx_mcs}'")
    lines.append(f"WIFI_5_RX_MCS='{node_info.wifi_5_rx_mcs}'")

    report_list = []
    for result in node_info.channel_report.results:
        report_list.append({
            "channel":     result.channel,
            "noise_floor": result.noise_floor,
            "bss_count":   result.bss_count
        })
    lines.append(f"CHANNEL_REPORT_JSON='{json.dumps({'results': report_list})}'")
    return lines


def main():
    parser = argparse.ArgumentParser(description="Decode NodeInfo protobuf message.")
    parser.add_argument("b64_string", nargs="?", help="Base64 encoded protobuf string.")
    parser.add_argument("--batch", action="store_true",
                        help="Read one base64 string per line from stdin, output records separated by %%%.")
    args = parser.parse_args()

    if args.batch:
        for line in sys.stdin:
            b64 = line.strip()
            if not b64:
                continue
            try:
                for out in decode_one(b64):
                    print(out)
                print("%%%")
            except Exception as e:
                print(f"DECODE_ERROR='{e}'")
                print("%%%")
        return

    if not args.b64_string:
        parser.error("b64_string is required when not using --batch")

    try:
        for out in decode_one(args.b64_string):
            print(out)
    except Exception as e:
        print(f"Error decoding message: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    main()
