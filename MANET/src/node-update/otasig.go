package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// OTA package signing. Every tarball on the update server comes with
// <tarball>.sig: an Ed25519 signature, base64, over otaMessage(file name,
// version, SHA-256 of the file). The node only extracts a tarball whose
// signature verifies against one of the public keys shipped in
// otaKeysDir, so the transport no longer matters -- a release can be
// served over plain http from a gateway or a laptop off-grid. Binding the
// name and version stops a validly signed old release being re-served as
// a newer one (a rollback), or an overlay swapped in for a tools tarball.
//
// update_allow_unsigned=y in mesh.conf is the lab escape hatch: a failed
// check is then logged and the package installed anyway.
//
// The keys live under /usr so the tools tarball itself ships them: adding
// or rotating a key is an ordinary signed release.
var otaKeysDir = "/usr/local/share/manet/ota-keys"

func otaMessage(name, version string, digest []byte) []byte {
	return []byte("manet-ota-v1\n" + name + "\n" + version + "\n" + hex.EncodeToString(digest) + "\n")
}

func fileSHA256(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// loadOTAKeys reads every *.pub in dir: one base64 Ed25519 public key each.
func loadOTAKeys(dir string) ([]ed25519.PublicKey, error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.pub"))
	var keys []ed25519.PublicKey
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%s: not a base64 Ed25519 public key", f)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no OTA signing keys in %s", dir)
	}
	return keys, nil
}

func verifyOTA(file, name, version, sigB64 string, keys []ed25519.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sigB64))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("malformed signature")
	}
	digest, err := fileSHA256(file)
	if err != nil {
		return err
	}
	msg := otaMessage(name, version, digest)
	for _, k := range keys {
		if ed25519.Verify(k, msg, sig) {
			return nil
		}
	}
	return errors.New("signature does not match any installed OTA key")
}

// checkOTASignature verifies the tarball downloaded from url to file as
// release version. nil means: install it.
func checkOTASignature(url, file, version string) error {
	name := path.Base(url)
	err := func() error {
		sig, err := fetchText(url + ".sig")
		if err != nil {
			return fmt.Errorf("no signature: %v", err)
		}
		keys, err := loadOTAKeys(otaKeysDir)
		if err != nil {
			return err
		}
		return verifyOTA(file, name, version, sig, keys)
	}()
	if err == nil {
		log.Printf("%s v%s: signature OK", name, version)
		return nil
	}
	if isAffirmative(confValue("update_allow_unsigned"), false) {
		log.Printf("WARNING: %s v%s: %v -- installing anyway (update_allow_unsigned=y)", name, version, err)
		return nil
	}
	return fmt.Errorf("%s v%s: %v -- refusing to install (update_allow_unsigned=y overrides)", name, version, err)
}

// Release-side subcommands, run on the release machine:
//
//	node-update ota-keygen <private-key-file> <public-key-file>
//	node-update ota-sign <private-key-file> <version> <tarball>   (writes <tarball>.sig)
func otaCommand(args []string) error {
	switch {
	case len(args) == 3 && args[0] == "ota-keygen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err // never overwrite an existing key
		}
		if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv)); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.WriteFile(args[2], []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0644)
	case len(args) == 4 && args[0] == "ota-sign":
		data, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		priv, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
		if err != nil || len(priv) != ed25519.PrivateKeySize {
			return fmt.Errorf("%s: not a base64 Ed25519 private key", args[1])
		}
		version, file := args[2], args[3]
		digest, err := fileSHA256(file)
		if err != nil {
			return err
		}
		sig := ed25519.Sign(ed25519.PrivateKey(priv), otaMessage(filepath.Base(file), version, digest))
		return os.WriteFile(file+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0644)
	}
	return errors.New("usage: node-update ota-keygen <priv> <pub> | ota-sign <priv> <version> <tarball>")
}
