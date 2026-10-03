package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	testPassword  = "correct-horse-battery-staple"
	testPassword2 = "a-different-password"
	testSSID      = "test-mesh"
)

func TestFleetSealOpenRoundTrip(t *testing.T) {
	plaintext := []byte(`{"version":"abc123","staged_at":1234}`)

	envelope, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}

	got, err := fleetOpen("70", envelope, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetOpen failed on a freshly sealed envelope: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestFleetOpenWrongPasswordFailsClosed(t *testing.T) {
	plaintext := []byte(`{"version":"abc123"}`)
	envelope, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("fleetOpen panicked on a wrong password: %v", r)
		}
	}()
	if _, err := fleetOpen("70", envelope, testPassword2, testSSID); err == nil {
		t.Fatalf("fleetOpen succeeded with the wrong password")
	}
}

func TestFleetOpenWrongSSIDFailsClosed(t *testing.T) {
	// meshSSID feeds the (deterministic, non-secret) salt -- a different
	// SSID derives a completely different key, so this must fail exactly
	// like a wrong password, not silently succeed.
	plaintext := []byte(`{"version":"abc123"}`)
	envelope, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}
	if _, err := fleetOpen("70", envelope, testPassword, "a-different-mesh-ssid"); err == nil {
		t.Fatalf("fleetOpen succeeded with a different mesh_ssid")
	}
}

// TestFleetOpenMalformedInputsRejectedWithoutPanic exercises inputs that
// never even reach the GCM Open call (bad JSON, bad base64, truncation) —
// these must be rejected during parsing, not just at the crypto layer, and
// must never panic on attacker-controlled bytes.
func TestFleetOpenMalformedInputsRejectedWithoutPanic(t *testing.T) {
	valid, err := fleetSeal("70", []byte(`{"version":"x"}`), testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}

	cases := map[string][]byte{
		"empty":                []byte(``),
		"not_json":             []byte(`this is not json at all {{{`),
		"truncated_envelope":   valid[:len(valid)/2],
		"garbage_base64_ct":    []byte(`{"v":2,"alg":"a256gcm-pbkdf2-sha256","iter":200000,"nonce":"AAAAAAAAAAAAAAAA","ct":"not-valid-base64!!!"}`),
		"garbage_base64_nonce": []byte(`{"v":2,"alg":"a256gcm-pbkdf2-sha256","iter":200000,"nonce":"not-valid-base64!!!","ct":"AAAA"}`),
		"empty_json_object":    []byte(`{}`),
	}

	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("fleetOpen panicked on case %q: %v", name, r)
				}
			}()
			if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
				t.Fatalf("case %q: expected fleetOpen to reject, got success", name)
			}
		})
	}
}

// TestFleetOpenHeaderFieldTamperRejected covers the v/alg/iter equality
// check, which rejects a downgrade/mismatch attempt BEFORE the envelope's
// nonce/ciphertext are even decoded, let alone GCM-opened. This is a real,
// valuable rejection path (it's what makes a v1-vs-v2 downgrade attempt
// impossible) but it is NOT the same thing as exercising the GCM
// authentication tag itself — see TestFleetOpenPostDecodeGCMTamperRejected
// below for that.
func TestFleetOpenHeaderFieldTamperRejected(t *testing.T) {
	plaintext := []byte(`{"version":"x"}`)
	envelope, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}

	var env fleetEnvelope
	if err := json.Unmarshal(envelope, &env); err != nil {
		t.Fatalf("failed to unmarshal envelope for tampering: %v", err)
	}

	t.Run("tamper_v", func(t *testing.T) {
		tampered := env
		tampered.V = 1
		data, _ := json.Marshal(tampered)
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected rejection after tampering v")
		}
	})
	t.Run("tamper_alg", func(t *testing.T) {
		tampered := env
		tampered.Alg = "none"
		data, _ := json.Marshal(tampered)
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected rejection after tampering alg")
		}
	})
	t.Run("tamper_iter", func(t *testing.T) {
		tampered := env
		tampered.Iter = 1
		data, _ := json.Marshal(tampered)
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected rejection after tampering iter")
		}
	})
}

// flipByte returns a copy of b with the byte at index i XORed with 0xFF.
func flipByte(b []byte, i int) []byte {
	out := append([]byte(nil), b...)
	out[i] ^= 0xFF
	return out
}

// TestFleetOpenPostDecodeGCMTamperRejected operates on the DECODED
// nonce/ciphertext bytes, past every earlier parsing/header shortcut, so it
// actually exercises `gcm.Open`'s authentication tag check — the thing the
// header-field and base64/JSON tests above do NOT reach.
func TestFleetOpenPostDecodeGCMTamperRejected(t *testing.T) {
	plaintext := []byte(`{"version":"x","staged_at":1}`)
	envelope, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}
	var env fleetEnvelope
	if err := json.Unmarshal(envelope, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	ctBytes, err := base64.StdEncoding.DecodeString(env.CT)
	if err != nil {
		t.Fatalf("decode ct: %v", err)
	}
	nonceBytes, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		t.Fatalf("decode nonce: %v", err)
	}
	if len(ctBytes) < 2 {
		t.Fatalf("sealed ciphertext unexpectedly short (%d bytes) for this test to flip start/middle/end distinctly", len(ctBytes))
	}

	buildEnvelope := func(ct, nonce []byte) []byte {
		e := env
		e.CT = base64.StdEncoding.EncodeToString(ct)
		e.Nonce = base64.StdEncoding.EncodeToString(nonce)
		data, _ := json.Marshal(e)
		return data
	}

	t.Run("ct_flip_start", func(t *testing.T) {
		data := buildEnvelope(flipByte(ctBytes, 0), nonceBytes)
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected GCM auth failure after flipping the first ciphertext byte")
		}
	})
	t.Run("ct_flip_middle", func(t *testing.T) {
		data := buildEnvelope(flipByte(ctBytes, len(ctBytes)/2), nonceBytes)
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected GCM auth failure after flipping a middle ciphertext byte")
		}
	})
	t.Run("ct_flip_end", func(t *testing.T) {
		data := buildEnvelope(flipByte(ctBytes, len(ctBytes)-1), nonceBytes)
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected GCM auth failure after flipping the last ciphertext byte (this is the GCM tag itself)")
		}
	})
	t.Run("ct_empty", func(t *testing.T) {
		data := buildEnvelope([]byte{}, nonceBytes)
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected rejection of an empty ciphertext")
		}
	})
	t.Run("nonce_flip", func(t *testing.T) {
		data := buildEnvelope(ctBytes, flipByte(nonceBytes, 0))
		if _, err := fleetOpen("70", data, testPassword, testSSID); err == nil {
			t.Fatalf("expected GCM auth failure after flipping a nonce byte")
		}
	})

	// Sanity: the untouched envelope still opens correctly, proving the
	// above rejections are really due to the specific tamper, not some
	// unrelated bug.
	if got, err := fleetOpen("70", envelope, testPassword, testSSID); err != nil || string(got) != string(plaintext) {
		t.Fatalf("untampered envelope failed to open cleanly: got=%q err=%v", got, err)
	}
}

func TestFleetOpenSlotBindingPreventsCrossSlotReplay(t *testing.T) {
	plaintext := []byte(`{"version":"x","staged_at":1}`)

	envelope70, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal(70) failed: %v", err)
	}
	envelope71, err := fleetSeal("71", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal(71) failed: %v", err)
	}

	if _, err := fleetOpen("71", envelope70, testPassword, testSSID); err == nil {
		t.Fatalf("a slot-70 envelope was accepted by fleetOpen(\"71\", ...)")
	}
	if _, err := fleetOpen("70", envelope71, testPassword, testSSID); err == nil {
		t.Fatalf("a slot-71 envelope was accepted by fleetOpen(\"70\", ...)")
	}

	// Sanity: each still opens fine on its own slot.
	if _, err := fleetOpen("70", envelope70, testPassword, testSSID); err != nil {
		t.Fatalf("slot-70 envelope failed to open on slot 70: %v", err)
	}
	if _, err := fleetOpen("71", envelope71, testPassword, testSSID); err != nil {
		t.Fatalf("slot-71 envelope failed to open on slot 71: %v", err)
	}
}

// TestFleetSealOpenMcastLabelRoundTrip covers the "70-mcast" label used by
// fleetMcastSendActivation/fleetMcastListener — it had zero direct test
// coverage of its own before this. Confirms both that it round-trips
// correctly and that it is NOT interchangeable with plain "70" (a real
// slot-70 config envelope must never be accepted by the mcast activation
// open path, or vice versa), even though "70-mcast" shares slot 70's overall
// trust domain (same admin_password/mesh_ssid derive the same key).
func TestFleetSealOpenMcastLabelRoundTrip(t *testing.T) {
	plaintext := []byte(`{"type":"fleet_activate","version":"x","activate_at":1}`)

	mcastEnvelope, err := fleetSeal("70-mcast", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal(\"70-mcast\") failed: %v", err)
	}
	got, err := fleetOpen("70-mcast", mcastEnvelope, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetOpen(\"70-mcast\") failed on its own envelope: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("70-mcast round-trip mismatch: got %q, want %q", got, plaintext)
	}

	slot70Envelope, err := fleetSeal("70", plaintext, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal(\"70\") failed: %v", err)
	}
	if _, err := fleetOpen("70-mcast", slot70Envelope, testPassword, testSSID); err == nil {
		t.Fatalf("a plain slot-70 envelope was accepted by fleetOpen(\"70-mcast\", ...)")
	}
	if _, err := fleetOpen("70", mcastEnvelope, testPassword, testSSID); err == nil {
		t.Fatalf("a 70-mcast envelope was accepted by fleetOpen(\"70\", ...)")
	}
}

// buildAlfredLine reproduces the exact `alfred -r <slot>` text line shape
// parseAlfredBest's string-surgery parser expects: `{ "<mac>", "<escaped>" }`
// where <escaped> is the JSON-string-escaped form of payload (mirroring how
// alfred embeds an opaque data blob as a quoted string in its own report
// output).
func buildAlfredLine(mac string, payload []byte) string {
	escaped, _ := json.Marshal(string(payload))
	return fmt.Sprintf(`{ "%s", %s }`, mac, string(escaped))
}

func TestParseAlfredBestVerifyThenRank(t *testing.T) {
	myMAC := "aa:aa:aa:aa:aa:aa"

	// Legitimate, older package.
	oldPkg := []byte(`{"version":"v-old","staged_at":1000}`)
	oldEnvelope, err := fleetSeal("70", oldPkg, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}

	// Attacker's bogus entry: not a valid envelope at all (can't decrypt,
	// doesn't have the right shape), but its raw bytes are crafted to LOOK
	// newer if something naively parsed an outer "staged_at" field before
	// verifying anything — reproducing the historic pick-then-verify bug.
	bogusPayload := []byte(`{"staged_at":99999999,"version":"attacker-wins-if-unverified"}`)

	lines := strings.Join([]string{
		buildAlfredLine("bb:bb:bb:bb:bb:bb", oldEnvelope),
		buildAlfredLine("cc:cc:cc:cc:cc:cc", bogusPayload),
	}, "\n")

	best := parseAlfredBest([]byte(lines), map[string]bool{strings.ReplaceAll(myMAC, ":", ""): true}, "70", "staged_at", testPassword, testSSID)
	if best == nil {
		t.Fatalf("parseAlfredBest returned nil, expected the legitimate package to win")
	}

	var pkg map[string]interface{}
	if err := json.Unmarshal(best, &pkg); err != nil {
		t.Fatalf("failed to unmarshal winning package: %v", err)
	}
	if v, _ := pkg["version"].(string); v != "v-old" {
		t.Fatalf("expected the legitimate (authenticated) package to win, got version %q", v)
	}
}

func TestParseAlfredBestSkipsOwnEntry(t *testing.T) {
	myMAC := "aa:aa:aa:aa:aa:aa"
	pkg := []byte(`{"version":"mine","staged_at":5000}`)
	envelope, err := fleetSeal("70", pkg, testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}
	line := buildAlfredLine(myMAC, envelope)

	best := parseAlfredBest([]byte(line), map[string]bool{strings.ReplaceAll(myMAC, ":", ""): true}, "70", "staged_at", testPassword, testSSID)
	if best != nil {
		t.Fatalf("parseAlfredBest should have skipped this node's own entry, got %s", best)
	}
}

// alfred labels this node's entries with br0's MAC, not bat0's (getMyMAC):
// any of the node's own MACs must be skipped, whatever its case or format,
// while a peer's entry still wins.
func TestParseAlfredBestSkipsAnyOwnInterfaceMAC(t *testing.T) {
	own := map[string]bool{
		"0ebf740028d2": true, // bat0
		"aad38e5aac79": true, // br0, what alfred records as the source
	}
	mine, err := fleetSeal("71", []byte(`{"channel":"check","triggered_at":300}`), testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}
	peer, err := fleetSeal("71", []byte(`{"channel":"software","triggered_at":200}`), testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}
	lines := strings.Join([]string{
		buildAlfredLine("AA:D3:8E:5A:AC:79", mine),
		buildAlfredLine("9c:04:b6:a0:aa:13", peer),
	}, "\n")

	best := parseAlfredBest([]byte(lines), own, "71", "triggered_at", testPassword, testSSID)
	var pkg map[string]interface{}
	if err := json.Unmarshal(best, &pkg); err != nil {
		t.Fatalf("failed to unmarshal winning package: %v", err)
	}
	if c, _ := pkg["channel"].(string); c != "software" {
		t.Fatalf("own br0-labelled entry should be skipped and the peer's win, got channel %q", c)
	}
}

func TestParseAlfredBestRanksNewerAuthenticatedEntry(t *testing.T) {
	myMAC := "aa:aa:aa:aa:aa:aa"

	older, err := fleetSeal("70", []byte(`{"version":"older","staged_at":100}`), testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}
	newer, err := fleetSeal("70", []byte(`{"version":"newer","staged_at":200}`), testPassword, testSSID)
	if err != nil {
		t.Fatalf("fleetSeal failed: %v", err)
	}

	lines := strings.Join([]string{
		buildAlfredLine("bb:bb:bb:bb:bb:bb", older),
		buildAlfredLine("cc:cc:cc:cc:cc:cc", newer),
	}, "\n")

	best := parseAlfredBest([]byte(lines), map[string]bool{strings.ReplaceAll(myMAC, ":", ""): true}, "70", "staged_at", testPassword, testSSID)
	var pkg map[string]interface{}
	if err := json.Unmarshal(best, &pkg); err != nil {
		t.Fatalf("failed to unmarshal winning package: %v", err)
	}
	if v, _ := pkg["version"].(string); v != "newer" {
		t.Fatalf("expected the newer authenticated entry to win, got %q", v)
	}
}
