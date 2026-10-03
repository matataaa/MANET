package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseGateways(t *testing.T) {
	// Captured on EUD3 (batctl 2025.3, batman-adv 2025.4), plus synthetic lines.
	out := `* 0c:bf:74:00:28:d2 (      111.6) 00:0a:52:0f:1b:f7 [     wlan1]: 10.0/2.0 MBit
  9c:04:b6:a0:aa:13 (        6.2) 9c:04:b6:a0:aa:13 [     wlan2]: 120.0/24.0 MBit
  9C:04:B6:A0:AA:6C (        6.2) 9c:04:b6:a0:aa:6c [     wlan2]: 50.0/10.0 MBit
garbage line`
	want := []gateway{
		{MAC: "0c:bf:74:00:28:d2", Score: 10.0}, // path 111.6, uplink 10: bottleneck 10
		{MAC: "9c:04:b6:a0:aa:13", Score: 6.2},  // ties on 6.2 sort by MAC
		{MAC: "9c:04:b6:a0:aa:6c", Score: 6.2},
	}
	if got := parseGateways(out); !reflect.DeepEqual(got, want) {
		t.Fatalf("parseGateways:\n got %+v\nwant %+v", got, want)
	}
	if got := parseGateways(""); got != nil {
		t.Fatalf("empty output: got %+v", got)
	}
}

func TestSelectorPlan(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	a := gateway{MAC: "aa", Score: 6}
	b := gateway{MAC: "bb", Score: 5}
	c := gateway{MAC: "cc", Score: 20}

	// First pick: every gateway, best first, immediately.
	s := newSelector(t0)
	cands, urgent := s.plan([]gateway{a, b}, false, t0)
	if !reflect.DeepEqual(cands, []string{"aa", "bb"}) || urgent == "" {
		t.Fatalf("first pick: %v %q", cands, urgent)
	}
	s.switched("aa", t0)

	// Steady: keep current.
	if cands, urgent = s.plan([]gateway{a, b}, true, t0.Add(10*time.Second)); !reflect.DeepEqual(cands, []string{"aa"}) || urgent != "" {
		t.Fatalf("steady: %v %q", cands, urgent)
	}

	// A much better gateway within switchHold of the first pick: no move.
	if cands, _ = s.plan([]gateway{c, a}, true, t0.Add(100*time.Second)); cands[0] != "aa" {
		t.Fatalf("within hold: %v", cands)
	}

	// After the hold it must stay better for switchSustain.
	t1 := t0.Add(switchHold)
	if cands, _ = s.plan([]gateway{c, a}, true, t1); cands[0] != "aa" {
		t.Fatalf("pending start: %v", cands)
	}
	if cands, _ = s.plan([]gateway{c, a}, true, t1.Add(switchSustain-time.Second)); cands[0] != "aa" {
		t.Fatalf("not yet sustained: %v", cands)
	}
	if cands, _ = s.plan([]gateway{c, a}, true, t1.Add(switchSustain)); cands[0] != "cc" {
		t.Fatalf("sustained: %v", cands)
	}
}

func TestSelectorSustainResetsOnDip(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	a := gateway{MAC: "aa", Score: 6}
	good := gateway{MAC: "cc", Score: 20}
	dip := gateway{MAC: "cc", Score: 7}

	s := newSelector(t0)
	s.switched("aa", t0.Add(-switchHold))                    // hold already over
	s.plan([]gateway{good, a}, true, t0)                     // pending starts
	s.plan([]gateway{dip, a}, true, t0.Add(30*time.Second))  // not better: pending cleared
	s.plan([]gateway{good, a}, true, t0.Add(40*time.Second)) // pending restarts at 40 s
	if cands, _ := s.plan([]gateway{good, a}, true, t0.Add(70*time.Second)); cands[0] != "aa" {
		t.Fatalf("sustain should restart after a dip: %v", cands)
	}
	if cands, _ := s.plan([]gateway{good, a}, true, t0.Add(100*time.Second)); cands[0] != "cc" {
		t.Fatalf("sustained after restart: %v", cands)
	}
}

func TestNoticeablyBetter(t *testing.T) {
	cases := []struct {
		cand, cur float64
		want      bool
	}{
		{9, 6, true},      // 1.5x and +3
		{8.9, 6, false},   // under 1.5x
		{3, 1, true},      // 3x and +2
		{2.9, 1, false},   // 2.9x but only +1.9
		{150, 110, false}, // +40 but only 1.36x
	}
	for _, c := range cases {
		if got := noticeablyBetter(c.cand, c.cur); got != c.want {
			t.Errorf("noticeablyBetter(%v, %v) = %v, want %v", c.cand, c.cur, got, c.want)
		}
	}
}

func TestSelectorFailover(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	a := gateway{MAC: "aa", Score: 6}
	b := gateway{MAC: "bb", Score: 3}

	s := newSelector(t0)
	s.switched("aa", t0)

	// One missed ping: keep it.
	if cands, urgent := s.plan([]gateway{a, b}, false, t0.Add(10*time.Second)); cands[0] != "aa" || urgent != "" {
		t.Fatalf("one miss: %v %q", cands, urgent)
	}
	// Second missed ping: move now, despite the hold, skipping aa.
	if cands, urgent := s.plan([]gateway{a, b}, false, t0.Add(20*time.Second)); !reflect.DeepEqual(cands, []string{"bb"}) || urgent == "" {
		t.Fatalf("two misses: %v %q", cands, urgent)
	}

	// Current gateway no longer announced: move now to the best remaining.
	s = newSelector(t0)
	s.switched("aa", t0)
	if cands, urgent := s.plan([]gateway{b}, false, t0.Add(10*time.Second)); !reflect.DeepEqual(cands, []string{"bb"}) || urgent == "" {
		t.Fatalf("gone: %v %q", cands, urgent)
	}

	// No gateways at all: forget the current one.
	s.plan(nil, false, t0.Add(20*time.Second))
	if s.cur != "" {
		t.Fatalf("no gateways: cur=%q", s.cur)
	}
}

func TestBandwidthArg(t *testing.T) {
	cases := []struct {
		in   string
		arg  string
		down float64
		ok   bool
	}{
		{"10M/10M", "10mbit/10mbit", 10, true},
		{"100M/100M", "100mbit/100mbit", 100, true},
		{"20mbit/4mbit", "20mbit/4mbit", 20, true},
		{"5000/1000", "5000kbit/1000kbit", 5, true},
		{"5000kbit", "5000kbit", 5, true},
		{"fast", "", 0, false},
		{"10M/x", "", 0, false},
	}
	for _, c := range cases {
		arg, down, ok := bandwidthArg(c.in)
		if arg != c.arg || down != c.down || ok != c.ok {
			t.Errorf("bandwidthArg(%q) = %q, %v, %v; want %q, %v, %v", c.in, arg, down, ok, c.arg, c.down, c.ok)
		}
	}
	if v, ok := announcedDownMbit("server (announced bw: 10.0/2.0 MBit)"); !ok || v != 10 {
		t.Errorf("announcedDownMbit: %v %v", v, ok)
	}
	if _, ok := announcedDownMbit("client (selection class: 5.0 MBit)"); ok {
		t.Errorf("announcedDownMbit on client output should fail")
	}
}
