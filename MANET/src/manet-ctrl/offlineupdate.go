package main

// Offline updates. Every node keeps a package store, PackageDir, laid out
// like the update server (see docs/AUTO_UPDATE.md): node-update links each
// package it installs into it, and this file adds what the UI uploads (a
// release .zip, or the loose release files) and what is fetched from
// another node's store. The store is served read-only to the mesh at
// /api/offline-update/files/, so one node that has a release -- uploaded
// from a laptop in the field, or installed once over the internet -- can
// update the rest of the fleet without internet.
//
// Installing always goes through node-update with the store as its source
// (a "source=" line in the trigger file): same signature check, same
// power-cut recovery and jittered reboot as an online update.

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	PackageDir      = "/var/lib/manet-packages"
	nodeUpdateBin   = "/usr/local/bin/node-update"
	deviceModelFile = "/proc/device-tree/model"
	// installedVersionFile is where node-update records each channel's
	// installed version.
	installedVersionFile = map[string]string{
		"software": "/etc/manet_release_version.txt",
		"overlay":  "/etc/manet_overlay_version.txt",
	}
)

const (
	// maxOfflineUpload fits a release .zip with both channels (~80 MB
	// today) with room to grow, and refuses one that has the ~3 GB SD card
	// image in it.
	maxOfflineUpload = 400 << 20
	// offlineFetchTimeout: a package crossing several HaLow hops can take
	// well over node-update's 10 minute download limit.
	offlineFetchTimeout = 60 * time.Minute
)

type offlinePackage struct {
	File    string `json:"file"`
	Version string `json:"version"`
	Size    int64  `json:"size"`
	Signed  bool   `json:"signed"`
}

type offlineStore struct {
	Board    string          `json:"board"`
	Software *offlinePackage `json:"software"`
	Overlay  *offlinePackage `json:"overlay"`
	// Fetching is the node this one is fetching a package from, if any.
	Fetching string `json:"fetching,omitempty"`
}

var (
	// offlineBusy serialises writes to PackageDir from uploads and fetches.
	offlineBusy     sync.Mutex
	offlineFetchMu  sync.Mutex
	offlineFetching string
)

var offlineChannels = []string{"software", "overlay"}

// offlineFiles names a channel's package and version file for board, as
// on the update server.
func offlineFiles(channel, board string) (tarball, versionFile string) {
	if channel == "overlay" {
		return board + "-sbc-overlay.tar.gz", board + "-overlay-version.txt"
	}
	return board + "-tools.tar.gz", "manet_release_version.txt"
}

// offlineFileNames is every file name the store holds for board.
func offlineFileNames(board string) map[string]bool {
	names := map[string]bool{}
	for _, ch := range offlineChannels {
		tarball, versionFile := offlineFiles(ch, board)
		names[tarball] = true
		names[tarball+".sig"] = true
		names[versionFile] = true
	}
	return names
}

// nodeBoard matches node-update's boardType: the board prefix of this
// node's package names, "" when unknown.
func nodeBoard() string {
	data, err := os.ReadFile(deviceModelFile)
	if err != nil {
		return ""
	}
	model := string(data)
	switch {
	case strings.Contains(model, "Raspberry Pi 5"):
		return "rpi5"
	case strings.Contains(model, "Raspberry Pi 4"), strings.Contains(model, "Raspberry Pi Compute Module 4"):
		return "cm4"
	}
	return ""
}

func firstLineOf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 4096))
	line, _, _ := strings.Cut(strings.TrimSpace(string(data)), "\n")
	return strings.TrimSpace(line)
}

func readOfflinePackage(dir, channel, board string) *offlinePackage {
	tarball, versionFile := offlineFiles(channel, board)
	st, err := os.Stat(filepath.Join(dir, tarball))
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	version := firstLineOf(filepath.Join(dir, versionFile))
	if version == "" {
		return nil
	}
	_, sigErr := os.Stat(filepath.Join(dir, tarball+".sig"))
	return &offlinePackage{File: tarball, Version: version, Size: st.Size(), Signed: sigErr == nil}
}

func readOfflineStore() offlineStore {
	s := offlineStore{Board: nodeBoard()}
	if s.Board != "" {
		s.Software = readOfflinePackage(PackageDir, "software", s.Board)
		s.Overlay = readOfflinePackage(PackageDir, "overlay", s.Board)
	}
	offlineFetchMu.Lock()
	s.Fetching = offlineFetching
	offlineFetchMu.Unlock()
	return s
}

func setOfflineFetching(ip string) {
	offlineFetchMu.Lock()
	offlineFetching = ip
	offlineFetchMu.Unlock()
}

// parseChannelVersion parses a release version (X.Y.Z) or an overlay
// version (plain decimal), the same formats node-update compares.
func parseChannelVersion(channel, v string) ([3]float64, bool) {
	v = strings.TrimSpace(v)
	if channel == "overlay" {
		f, err := strconv.ParseFloat(v, 64)
		return [3]float64{f}, err == nil
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]float64{}, false
	}
	var out [3]float64
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]float64{}, false
		}
		out[i] = float64(n)
	}
	return out, true
}

// versionNewer reports whether a is a newer channel version than b. An
// unparsable a is never newer; an unparsable or empty b is older than
// anything.
func versionNewer(channel, a, b string) bool {
	va, ok := parseChannelVersion(channel, a)
	if !ok {
		return false
	}
	vb, ok := parseChannelVersion(channel, b)
	if !ok {
		return true
	}
	for i := range va {
		if va[i] != vb[i] {
			return va[i] > vb[i]
		}
	}
	return false
}

func installedVersion(channel string) string {
	return firstLineOf(installedVersionFile[channel])
}

// commitOfflinePackages checks the packages staged in dir and moves them
// into PackageDir: all of them, or none when any is unusable. A package
// needs its version file; it must pass node-update's signature check
// unless update_allow_unsigned=y; and it must not replace a newer one the
// store already holds. Returns the channels committed.
func commitOfflinePackages(dir, board string) ([]string, error) {
	v := strings.ToLower(loadKVFile(MeshConfFile)["update_allow_unsigned"])
	allowUnsigned := v == "y" || v == "yes" || v == "1"
	var channels []string
	for _, ch := range offlineChannels {
		tarball, versionFile := offlineFiles(ch, board)
		if _, err := os.Stat(filepath.Join(dir, tarball)); err != nil {
			continue
		}
		version := firstLineOf(filepath.Join(dir, versionFile))
		if version == "" {
			return nil, fmt.Errorf("%s needs %s next to it", tarball, versionFile)
		}
		if _, ok := parseChannelVersion(ch, version); !ok {
			return nil, fmt.Errorf("%s: unreadable version %q", versionFile, version)
		}
		if held := readOfflinePackage(PackageDir, ch, board); held != nil && versionNewer(ch, held.Version, version) {
			return nil, fmt.Errorf("this node already holds %s v%s, newer than v%s", tarball, held.Version, version)
		}
		out, err := runCmd(5*time.Minute, nodeUpdateBin, "ota-verify", version, filepath.Join(dir, tarball))
		if err != nil {
			msg := strings.TrimSpace(out)
			if msg == "" {
				msg = err.Error()
			}
			if !allowUnsigned {
				return nil, fmt.Errorf("%s v%s: signature check failed: %s", tarball, version, msg)
			}
			log.Printf("offline update: %s v%s: signature check failed (%s), kept anyway: update_allow_unsigned=y", tarball, version, msg)
		}
		channels = append(channels, ch)
	}
	if len(channels) == 0 {
		t, _ := offlineFiles("software", board)
		o, _ := offlineFiles("overlay", board)
		return nil, fmt.Errorf("no package for this node: expected %s or %s, each with its .sig and version file", t, o)
	}

	if err := os.MkdirAll(PackageDir, 0755); err != nil {
		return nil, err
	}
	for _, ch := range channels {
		tarball, versionFile := offlineFiles(ch, board)
		if err := os.Rename(filepath.Join(dir, tarball), filepath.Join(PackageDir, tarball)); err != nil {
			return nil, err
		}
		if err := os.Rename(filepath.Join(dir, tarball+".sig"), filepath.Join(PackageDir, tarball+".sig")); err != nil {
			os.Remove(filepath.Join(PackageDir, tarball+".sig"))
		}
		// Last, as node-update does: whoever reads the new version finds
		// the matching package.
		if err := os.Rename(filepath.Join(dir, versionFile), filepath.Join(PackageDir, versionFile)); err != nil {
			return nil, err
		}
		log.Printf("offline update: holding %s", tarball)
	}
	return channels, nil
}

func saveStream(r io.Reader, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// extractOfflineZip copies the store's files out of a release .zip into
// dir by base name, wherever they sit inside it, and lists what it left out.
func extractOfflineZip(zipPath, dir string, wanted map[string]bool) (ignored []string, err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("not a readable .zip: %v", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(f.Name)
		if !wanted[name] {
			ignored = append(ignored, name)
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		err = saveStream(io.LimitReader(rc, maxOfflineUpload), filepath.Join(dir, name))
		rc.Close()
		if err != nil {
			return nil, err
		}
	}
	return ignored, nil
}

// apiOfflineUpdate: GET is the store's contents (no auth, like
// update-status); POST (auth) uploads a package into it.
func apiOfflineUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 200, readOfflineStore())
		return
	}
	if !checkAuth(w, r) {
		return
	}
	board := nodeBoard()
	if board == "" {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": "unknown board type"})
		return
	}
	if !offlineBusy.TryLock() {
		writeJSON(w, 409, map[string]interface{}{"ok": false, "error": "another offline package is being stored, try again shortly"})
		return
	}
	defer offlineBusy.Unlock()

	r.Body = http.MaxBytesReader(w, r.Body, maxOfflineUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": "expected a multipart file upload"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(PackageDir), 0755); err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	// Next to PackageDir, so committing is a rename on the same filesystem.
	tmp, err := os.MkdirTemp(filepath.Dir(PackageDir), ".manet-packages-upload-")
	if err != nil {
		writeJSON(w, 500, map[string]interface{}{"ok": false, "error": err.Error()})
		return
	}
	defer os.RemoveAll(tmp)

	wanted := offlineFileNames(board)
	ignored := []string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": offlineUploadError(err)})
			return
		}
		name := filepath.Base(part.FileName())
		switch {
		case part.FileName() == "":
		case strings.HasSuffix(strings.ToLower(name), ".zip"):
			zipPath := filepath.Join(tmp, ".upload.zip")
			err = saveStream(part, zipPath)
			if err == nil {
				var skipped []string
				skipped, err = extractOfflineZip(zipPath, tmp, wanted)
				ignored = append(ignored, skipped...)
			}
			os.Remove(zipPath)
		case wanted[name]:
			err = saveStream(part, filepath.Join(tmp, name))
		default:
			ignored = append(ignored, name)
		}
		part.Close()
		if err != nil {
			writeJSON(w, 400, map[string]interface{}{"ok": false, "error": offlineUploadError(err)})
			return
		}
	}

	channels, err := commitOfflinePackages(tmp, board)
	if err != nil {
		writeJSON(w, 400, map[string]interface{}{"ok": false, "error": err.Error(), "ignored": ignored})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"ok": true, "stored": channels, "ignored": ignored, "store": readOfflineStore()})
}

func offlineUploadError(err error) string {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		return fmt.Sprintf("upload is larger than %d MB; leave the .img out", maxOfflineUpload>>20)
	}
	return "upload failed: " + err.Error()
}

// apiOfflineFiles serves the store to other nodes (and anyone who can
// reach the UI): released packages, signatures and version files only.
func apiOfflineFiles(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/offline-update/files/")
	board := nodeBoard()
	if board == "" || !offlineFileNames(board)[name] || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(PackageDir, name))
}

// validOfflineSource is a mesh node address an offline package can come from.
func validOfflineSource(ip string) bool {
	return isMeshPeerIP(ip, loadKVFile(MeshConfFile))
}

// offlineInstall installs channel ("software", "overlay" or "both") from
// the store of the node at sourceIP: straight from PackageDir when that is
// this node, otherwise after fetching the package from there into
// PackageDir, in the background (the store's status shows it as Fetching).
func offlineInstall(channel, sourceIP string) error {
	if hostMatchesLocalAddr(sourceIP) {
		return triggerLocalUpdateFrom(channel, PackageDir)
	}
	if !offlineBusy.TryLock() {
		return errors.New("an offline package is already being stored on this node")
	}
	setOfflineFetching(sourceIP)
	go func() {
		defer offlineBusy.Unlock()
		defer setOfflineFetching("")
		if err := fetchOfflinePackage(channel, sourceIP); err != nil {
			log.Printf("offline update from %s: %v", sourceIP, err)
			return
		}
		if err := triggerLocalUpdateFrom(channel, PackageDir); err != nil {
			log.Printf("offline update from %s: %v", sourceIP, err)
		}
	}()
	return nil
}

// fetchOfflinePackage copies channel's package from sourceIP's store into
// this node's, skipping a package that is not newer than what is installed
// here or that this store already holds. Called with offlineBusy held.
func fetchOfflinePackage(channel, sourceIP string) error {
	board := nodeBoard()
	if board == "" {
		return errors.New("unknown board type")
	}
	client := &http.Client{Timeout: offlineFetchTimeout, Transport: &http.Transport{TLSClientConfig: peerTLSConfig}}
	base := "https://" + sourceIP + "/api/offline-update"

	resp, err := client.Get(base)
	if err != nil {
		return err
	}
	var remote offlineStore
	err = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&remote)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("reading its package list: %v", err)
	}
	if remote.Board != board {
		return fmt.Errorf("it holds %q packages, this node is %q", remote.Board, board)
	}

	tmp, err := os.MkdirTemp(filepath.Dir(PackageDir), ".manet-packages-fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	fetched := 0
	for _, ch := range offlineChannels {
		if channel != ch && channel != "both" {
			continue
		}
		rp := remote.Software
		if ch == "overlay" {
			rp = remote.Overlay
		}
		if rp == nil {
			log.Printf("offline update: %s holds no %s package", sourceIP, ch)
			continue
		}
		if !versionNewer(ch, rp.Version, installedVersion(ch)) {
			log.Printf("offline update: %s v%s from %s is not newer than installed v%s", ch, rp.Version, sourceIP, installedVersion(ch))
			continue
		}
		if held := readOfflinePackage(PackageDir, ch, board); held != nil && held.Version == rp.Version && held.Size == rp.Size {
			log.Printf("offline update: already holding %s v%s", rp.File, rp.Version)
			continue
		}
		tarball, versionFile := offlineFiles(ch, board)
		names := []string{tarball}
		if rp.Signed {
			names = append(names, tarball+".sig")
		}
		for _, name := range names {
			log.Printf("offline update: fetching %s from %s", name, sourceIP)
			if err := fetchFile(client, base+"/files/"+name, filepath.Join(tmp, name)); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(tmp, versionFile), []byte(rp.Version+"\n"), 0644); err != nil {
			return err
		}
		fetched++
	}
	if fetched == 0 {
		return nil
	}
	_, err = commitOfflinePackages(tmp, board)
	return err
}

func fetchFile(client *http.Client, url, dest string) error {
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return saveStream(io.LimitReader(resp.Body, maxOfflineUpload), dest)
}
