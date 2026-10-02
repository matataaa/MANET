package main

import "testing"

func TestParseDirectNeighbors(t *testing.T) {
	out := `[B.A.T.M.A.N. adv 2025.4, MainIF/MAC: wlan2/0c:bf:74:00:28:d2 (bat0/0e:bf:74:00:28:d2 BATMAN_V)]
         Neighbor   last-seen      speed           IF
0c:bf:74:00:2b:ee    0.432s (        8.5) [     wlan2]
9c:04:b6:a0:aa:6c   75.020s (        7.1) [     wlan2]
00:0a:52:0f:1a:30    0.180s (       42.2) [     wlan0]
`
	got := parseDirectNeighbors(out)
	want := "0c:bf:74:00:2b:ee=8.5=wlan2,00:0a:52:0f:1a:30=42.2=wlan0"
	if got != want {
		t.Fatalf("parseDirectNeighbors:\n got %q\nwant %q", got, want)
	}
}
