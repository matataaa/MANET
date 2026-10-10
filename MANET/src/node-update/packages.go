package main

// The node's local package store: the last package installed on each
// channel, kept with its signature and version file in the same layout as
// the update server. manet-ctrl serves this directory to the rest of the
// mesh and stages uploaded or peer-fetched packages into it, so a release
// only has to reach the fleet once, and a node without internet can still
// be updated from one that has the package.

import (
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
)

var packageDir = "/var/lib/manet-packages"

// keepPackage links the installed package kept at file (the recovery copy,
// on the same filesystem) into packageDir under its published name, next to
// its signature and versionFile. url is where it came from, for the name
// and the signature. Failure only costs the offline copy, so it is logged,
// not returned.
func keepPackage(url, file, versionFile, version string) {
	name := path.Base(url)
	err := func() error {
		if err := os.MkdirAll(packageDir, 0755); err != nil {
			return err
		}
		dest := filepath.Join(packageDir, name)
		tmp := filepath.Join(packageDir, "."+name+".new")
		os.Remove(tmp)
		if err := os.Link(file, tmp); err != nil {
			if err := copyFile(file, tmp); err != nil {
				return err
			}
		}
		if err := os.Rename(tmp, dest); err != nil {
			os.Remove(tmp)
			return err
		}
		if sig, err := fetchText(url + ".sig"); err == nil {
			if err := writeFileSync(dest+".sig", sig+"\n", 0644); err != nil {
				return err
			}
		} else {
			os.Remove(dest + ".sig")
		}
		// Last: a reader that sees the new version also finds the new
		// package and signature.
		return writeFileSync(filepath.Join(packageDir, versionFile), version+"\n", 0644)
	}()
	if err != nil {
		log.Printf("could not keep %s v%s for offline updates: %v", name, version, err)
		return
	}
	log.Printf("kept %s v%s in %s for offline updates", name, version, packageDir)
}

// copyFile copies src to dest. Offline packages are copied, not moved, out
// of packageDir: armRecovery moves what it is given, and the store has to
// keep its copy to serve it to the rest of the fleet.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dest)
		return err
	}
	return nil
}
