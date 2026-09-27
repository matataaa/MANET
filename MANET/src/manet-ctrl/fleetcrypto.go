package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// fleetcrypto.go authenticates every mesh-wide config/update package
// exchanged over Alfred slots 70/71 (and the activation multicast on top of
// slot 70), closing an unauthenticated-RCE-by-config-push vulnerability: any
// mesh member could previously rewrite admin_password/update_url fleet-wide
// with no signing at all. AES-256-GCM (stdlib crypto/aes + crypto/cipher)
// gives us authenticated encryption with one call; crypto/pbkdf2 (Go
// 1.24+) derives the key from admin_password without adding a dependency —
// golang.org/x/crypto is deliberately NOT used anywhere in this repo, and
// this keeps it that way.
const (
	fleetCryptoVersion = 2
	fleetCryptoAlg     = "a256gcm-pbkdf2-sha256"
	fleetCryptoIter    = 200000
	fleetKeyLenBytes   = 32
	fleetNonceLenBytes = 12
)

// fleetEnvelope is the wire format written to `alfred -s <slot>` and to the
// activation multicast socket. All binary fields are base64 — the existing
// `alfred -r` line parser in parseAlfredBest does simple `"`-delimited string
// surgery, so raw ciphertext bytes (which routinely contain `"`) would
// corrupt it; base64 avoids that entirely without touching that parser.
type fleetEnvelope struct {
	V     int    `json:"v"`
	Alg   string `json:"alg"`
	Iter  int    `json:"iter"`
	Nonce string `json:"nonce"`
	CT    string `json:"ct"`
}

// errEmptyAdminPassword is returned whenever admin_password is unset. Callers
// must treat this as "refuse to broadcast / refuse to accept" — there is no
// fallback to a fixed key or to mesh_key, either of which would fail open and
// be no better than the vulnerability this replaces. require_auth=n with an
// empty admin_password is a real provisioned default, so this path is
// reachable in practice, not just theoretical.
var errEmptyAdminPassword = errors.New("fleet: admin_password is empty, refusing fleet crypto operation")

var (
	fleetKeyMu       sync.Mutex
	fleetKeyCache    []byte
	fleetKeyPassword string
	fleetKeySSID     string
)

// deriveFleetKey derives the AES-256 key from admin_password alone (never
// mesh_key — the whole point of this change is that mesh membership alone
// must no longer be sufficient to push config). The salt is fixed and
// deterministic (sha256 of a static string + mesh_ssid), NOT random per
// packet: a random salt would force a fresh 200000-round PBKDF2 run on every
// received packet on a 10s poll loop, which is a trivial CPU-DoS via slot-70
// flooding. The derived key is cached and re-validated against the current
// (password, ssid) pair with a constant-time compare on every call, rather
// than re-derived every call, so the steady-state cost is one comparison, not
// one KDF run.
func deriveFleetKey(password, meshSSID string) ([]byte, error) {
	if password == "" {
		return nil, errEmptyAdminPassword
	}

	fleetKeyMu.Lock()
	defer fleetKeyMu.Unlock()

	if fleetKeyCache != nil &&
		subtle.ConstantTimeCompare([]byte(fleetKeyPassword), []byte(password)) == 1 &&
		subtle.ConstantTimeCompare([]byte(fleetKeySSID), []byte(meshSSID)) == 1 {
		return fleetKeyCache, nil
	}

	salt := sha256.Sum256([]byte("manet-fleet-key-v2|" + meshSSID))
	key, err := pbkdf2.Key(sha256.New, password, salt[:], fleetCryptoIter, fleetKeyLenBytes)
	if err != nil {
		return nil, fmt.Errorf("fleet: pbkdf2 key derivation failed: %w", err)
	}

	fleetKeyCache = key
	fleetKeyPassword = password
	fleetKeySSID = meshSSID
	return key, nil
}

// fleetAAD binds the sealed envelope to its exact protocol version/algorithm/
// iteration count and to the Alfred slot it's destined for. Binding the slot
// means a slot-70 config envelope can never be replayed into slot 71's open
// call (or vice versa) even though both use the identical seal/open code —
// GCM authentication fails immediately on an AAD mismatch.
func fleetAAD(slot string) []byte {
	return []byte(fmt.Sprintf("%d|%s|%d|%s", fleetCryptoVersion, fleetCryptoAlg, fleetCryptoIter, slot))
}

func fleetGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("fleet: aes cipher init failed: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("fleet: gcm init failed: %w", err)
	}
	return gcm, nil
}

// fleetSeal encrypts plaintext for the given label ("70" or "71" for the
// Alfred slots, or "70-mcast" for the activation multicast, which shares
// slot 70's trust domain but uses its own distinct AAD label — see
// fleetMcastSendActivation) and returns the JSON-marshaled envelope ready to
// hand to `alfred -s <slot>` (via stdin) or a UDP socket.
func fleetSeal(slot string, plaintext []byte, password, meshSSID string) ([]byte, error) {
	key, err := deriveFleetKey(password, meshSSID)
	if err != nil {
		return nil, err
	}
	gcm, err := fleetGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, fleetNonceLenBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("fleet: nonce generation failed: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, fleetAAD(slot))

	env := fleetEnvelope{
		V:     fleetCryptoVersion,
		Alg:   fleetCryptoAlg,
		Iter:  fleetCryptoIter,
		Nonce: base64.StdEncoding.EncodeToString(nonce),
		CT:    base64.StdEncoding.EncodeToString(ct),
	}
	data, err := json.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("fleet: envelope marshal failed: %w", err)
	}
	return data, nil
}

// fleetOpen authenticates and decrypts an envelope received for the given
// slot. It hard-rejects anything that isn't a well-formed v2 envelope for the
// expected version/algorithm/iteration count (no dual-accept of the old
// unauthenticated v1 plaintext format — see the rollout policy in the design
// doc) and never panics on malformed input: truncated/bit-flipped/garbage
// base64/empty/non-JSON payloads all just return an error.
//
// There is deliberately NO fallback to a previous/rotated admin_password
// here. An earlier version of this code tried one, and it was wrong on two
// counts: it never expired or was revoked (a leaked-then-rotated password
// could still push fleet-wide config forever), and it added a second full
// PBKDF2 derivation on every failed-with-current-key open — a cheap,
// unauthenticated CPU/lock-contention DoS via the multicast listener (no
// login needed to reach UDP 17070 on br0). A node that misses a rotation
// needs its admin_password fixed locally (SSH) — acceptable manual recovery
// for a small, operator-controlled fleet.
func fleetOpen(slot string, data []byte, password, meshSSID string) ([]byte, error) {
	var env fleetEnvelope
	if jsonErr := json.Unmarshal(data, &env); jsonErr != nil {
		return nil, fmt.Errorf("fleet: invalid envelope json: %w", jsonErr)
	}
	if env.V != fleetCryptoVersion || env.Alg != fleetCryptoAlg || env.Iter != fleetCryptoIter {
		return nil, fmt.Errorf("fleet: unsupported envelope (v=%d alg=%q iter=%d)", env.V, env.Alg, env.Iter)
	}
	nonce, decErr := base64.StdEncoding.DecodeString(env.Nonce)
	if decErr != nil || len(nonce) != fleetNonceLenBytes {
		return nil, errors.New("fleet: invalid envelope nonce")
	}
	ct, decErr := base64.StdEncoding.DecodeString(env.CT)
	if decErr != nil {
		return nil, errors.New("fleet: invalid envelope ciphertext encoding")
	}

	key, err := deriveFleetKey(password, meshSSID)
	if err != nil {
		return nil, err
	}
	gcm, err := fleetGCM(key)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, fleetAAD(slot))
	if err != nil {
		return nil, errors.New("fleet: envelope authentication failed (wrong admin_password, tampered payload, or wrong slot)")
	}
	return pt, nil
}

// newPkgID generates the 32-hex-char, crypto/rand-sourced identifier that is
// the real replay-record key for a staged fleet config package (NOT
// `version`, which is only 8 hex chars of a non-cryptographic hash of the
// config content+time and isn't a security-grade identifier on its own).
func newPkgID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("fleet: pkg_id generation failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}
