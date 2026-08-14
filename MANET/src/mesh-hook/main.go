package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const hooksDir = "/usr/local/share/manet/hooks"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mesh-hook <event> [KEY=VALUE ...]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Events:")
		fmt.Fprintln(os.Stderr, "  gateway-up         Gateway internet detected")
		fmt.Fprintln(os.Stderr, "  gateway-down       Gateway internet lost")
		fmt.Fprintln(os.Stderr, "  peer-join          New mesh peer appeared")
		fmt.Fprintln(os.Stderr, "  peer-leave         Mesh peer went offline")
		fmt.Fprintln(os.Stderr, "  ip-change          Node IP address changed")
		fmt.Fprintln(os.Stderr, "  ap-client-join     EUD connected to AP")
		fmt.Fprintln(os.Stderr, "  ap-client-leave    EUD disconnected from AP")
		fmt.Fprintln(os.Stderr, "  config-change      mesh.conf was modified")
		fmt.Fprintln(os.Stderr, "  limp-enter         Node entered limp mode")
		fmt.Fprintln(os.Stderr, "  limp-exit          Node exited limp mode")
		os.Exit(1)
	}

	event := os.Args[1]
	env := os.Environ()
	env = append(env, "MESH_EVENT="+event)
	for _, arg := range os.Args[2:] {
		if strings.Contains(arg, "=") {
			env = append(env, "MESH_"+strings.ToUpper(arg))
		}
	}

	dir := filepath.Join(hooksDir, event)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			continue
		}

		// Resolve symlinks to get the real path
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}

		cmd := exec.Command("/bin/bash", real)
		cmd.Dir = filepath.Dir(real)
		cmd.Env = append(env, "HOOK_SCRIPT="+e.Name())
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()
		select {
		case err := <-done:
			if err != nil {
				log.Printf("hook %s/%s failed: %v", event, e.Name(), err)
			}
		case <-time.After(30 * time.Second):
			cmd.Process.Kill()
			log.Printf("hook %s/%s timed out (30s)", event, e.Name())
		}
	}
}
