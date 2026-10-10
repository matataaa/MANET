package main

import "testing"

// Trimmed `iw dev` output from EUD2: HaLow mesh point + onboard Wi-Fi AP.
const iwDevSample = `phy#0
	Interface wlan2
		ifindex 4
		wdev 0x1
		addr 0c:bf:74:00:2b:ee
		type mesh point
		channel 108 (5540 MHz), width: 40 MHz, center1: 5550 MHz
		txpower 24.00 dBm
phy#1
	Interface wlan3
		ifindex 3
		wdev 0x100000001
		addr 2c:cf:67:73:d4:43
		ssid MANET-EUD-6773d442
		type AP
		channel 36 (5180 MHz), width: 20 MHz, center1: 5180 MHz
		txpower 5.00 dBm
`

func TestParseIWDevOutput(t *testing.T) {
	devs := parseIWDevOutput(iwDevSample)
	want := map[string]iwDev{
		"wlan2": {Name: "wlan2", Type: "mesh point", Channel: "108", TxPower: "24.00", Freq: "5540", Wiphy: "0", Width: "40"},
		"wlan3": {Name: "wlan3", Type: "AP", SSID: "MANET-EUD-6773d442", Channel: "36", TxPower: "5.00", Freq: "5180", Wiphy: "1", Width: "20"},
	}
	if len(devs) != len(want) {
		t.Fatalf("got %d devs, want %d: %+v", len(devs), len(want), devs)
	}
	for name, w := range want {
		if got := devs[name]; got != w {
			t.Errorf("%s:\n got  %+v\n want %+v", name, got, w)
		}
	}
}
