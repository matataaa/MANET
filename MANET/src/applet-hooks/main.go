package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: applet-hooks <pre-install|post-remove> <applet-name>")
		os.Exit(1)
	}
	action := os.Args[1]
	appletName := os.Args[2]

	dir := os.Getenv("APPLET_DIR")
	if dir == "" {
		dir, _ = os.Getwd()
	}

	switch action {
	case "pre-install":
		if err := checkMeshUnique(appletName); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		runScript(dir, "pre-install.sh")
	case "post-remove":
		runScript(dir, "post-remove.sh")
	default:
		fmt.Fprintf(os.Stderr, "unknown action: %s\n", action)
		os.Exit(1)
	}
}

func checkMeshUnique(appletName string) error {
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Get("https://127.0.0.1/api/data")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}

	var data struct {
		Nodes []struct {
			Hostname string `json:"hostname"`
			IP       string `json:"ip"`
			IsSelf   bool   `json:"is_self"`
			Applets  []struct {
				Name string `json:"name"`
			} `json:"applets"`
		} `json:"nodes"`
	}
	if json.Unmarshal(body, &data) != nil {
		return nil
	}

	for _, n := range data.Nodes {
		if n.IsSelf {
			continue
		}
		for _, a := range n.Applets {
			if a.Name == appletName {
				host := n.Hostname
				if host == "" {
					host = n.IP
				}
				return fmt.Errorf("%s is already installed on node: %s. Only one instance allowed across the mesh.", appletName, host)
			}
		}
	}
	return nil
}

func runScript(dir, name string) {
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return
	}
	os.Chmod(path, 0755)
	cmd := exec.Command("/bin/bash", path)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "APPLET_DIR="+dir)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s failed: %v\n", name, err)
		os.Exit(1)
	}
}
