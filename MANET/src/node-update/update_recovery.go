package main

// An update unpacks straight onto /. A power cut part-way through leaves a
// mix of old and new files, and the node would need the network (which those
// files may be what brings up) to download the release again. So the
// verified package is kept on disk with a pending marker until the unpack has
// reached the disk, and an early-boot unit finishes an interrupted one from
// that copy, offline.
//
// The recovery script and unit are written here, not shipped in the
// tarball: the unpack being protected would otherwise rewrite them, and a
// cut at that moment could leave the recovery path itself broken. They use
// only sh, tar and sync, not this binary, which the cut may have hit too.

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

var (
	updateStateDir = "/var/lib/manet-update"
	systemdDir     = "/etc/systemd/system"
)

const recoverUnitName = "manet-update-recover.service"

// channelVersionFile maps an update channel to the file recording its
// installed version.
var channelVersionFile = map[string]string{
	"software": releaseVersionFile,
	"overlay":  overlayVersionFile,
}

const recoverScript = `#!/bin/sh
# Written by node-update. Finishes a software/overlay update that a power
# cut interrupted, from the verified package kept in this directory. Exit 0
# when one was finished: the unit then reboots once (SuccessAction), so every
# service starts on complete files.
d=%[1]s
done_any=
for c in software overlay; do
    [ -f "$d/$c.pending" ] || continue
    case $c in
        software) vf=%[2]s ;;
        overlay) vf=%[3]s ;;
    esac
    v=$(cat "$d/$c.pending")
    if tar -zxf "$d/$c.tar.gz" --no-overwrite-dir -C / && sync; then
        printf '%%s\n' "$v" > "$vf.new" && sync && mv "$vf.new" "$vf"
        rm -f "$d/$c.pending" "$d/$c.tar.gz"
        sync
        echo "finished the interrupted $c update to $v"
        done_any=1
    else
        # Do not retry on every boot: a package that cannot unpack now will
        # not unpack next time either.
        mv "$d/$c.pending" "$d/$c.failed"
        sync
        echo "could not finish the interrupted $c update to $v; left $d/$c.failed" >&2
    fi
done
[ -n "$done_any" ]
`

const recoverUnit = `[Unit]
Description=Finish an interrupted MANET update from the local package
DefaultDependencies=no
After=local-fs.target
RequiresMountsFor=/var/lib /usr/local /etc /boot/firmware
Before=sysinit.target shutdown.target
Conflicts=shutdown.target
ConditionPathExistsGlob=%[1]s/*.pending
# Reboot once after finishing an update; this early, systemctl cannot reach
# logind to do it from the script.
SuccessAction=reboot

[Service]
Type=oneshot
ExecStart=/bin/sh %[1]s/recover.sh
TimeoutStartSec=10min

[Install]
WantedBy=sysinit.target
`

// armRecovery moves the verified package into the state directory, installs
// the boot-time recovery unit and writes the channel's pending marker, all
// synced to disk. It returns the package's new path, to unpack from.
func armRecovery(channel, pkg, version string) (string, error) {
	if err := os.MkdirAll(updateStateDir, 0700); err != nil {
		return "", err
	}
	if err := writeFileSync(filepath.Join(updateStateDir, "recover.sh"),
		fmt.Sprintf(recoverScript, updateStateDir, releaseVersionFile, overlayVersionFile), 0700); err != nil {
		return "", err
	}
	wants := filepath.Join(systemdDir, "sysinit.target.wants")
	if err := os.MkdirAll(wants, 0755); err != nil {
		return "", err
	}
	if err := writeFileSync(filepath.Join(systemdDir, recoverUnitName),
		fmt.Sprintf(recoverUnit, updateStateDir), 0644); err != nil {
		return "", err
	}
	link := filepath.Join(wants, recoverUnitName)
	if _, err := os.Lstat(link); os.IsNotExist(err) {
		if err := os.Symlink("../"+recoverUnitName, link); err != nil {
			return "", err
		}
	}
	if err := syncDir(wants); err != nil {
		return "", err
	}

	kept := filepath.Join(updateStateDir, channel+".tar.gz")
	if err := os.Rename(pkg, kept); err != nil {
		return "", err
	}
	if err := syncFile(kept); err != nil {
		return "", err
	}
	if err := writeFileSync(filepath.Join(updateStateDir, channel+".pending"), version+"\n", 0600); err != nil {
		return "", err
	}
	return kept, nil
}

// finishRecovery runs once the unpack has succeeded: flush it to disk, record
// the version, and only then drop the marker and the kept package.
func finishRecovery(channel, version string) error {
	syscall.Sync()
	if err := writeFileSync(channelVersionFile[channel], version+"\n", 0644); err != nil {
		return err
	}
	os.Remove(filepath.Join(updateStateDir, channel+".pending"))
	os.Remove(filepath.Join(updateStateDir, channel+".tar.gz"))
	return syncDir(updateStateDir)
}

// writeFileSync replaces path atomically and durably: write a sibling, fsync
// it, rename it over path, fsync the directory.
func writeFileSync(path, data string, mode os.FileMode) error {
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
