package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

// The web terminal can SSH to another node with a password. That used to
// go through sshpass, which no image installs (only a hand-set-up node had
// it) and which puts the password on its command line, readable in ps.
// OpenSSH (8.4+) can instead ask an SSH_ASKPASS program, forced with
// SSH_ASKPASS_REQUIRE=force even when a terminal is attached. That program
// is this binary: started with askpassPasswordEnv in its environment, it
// prints the password and exits (runAsAskpass, first thing in main). The
// password only ever travels in the environment of ssh and its askpass
// child, which only root can read.
const askpassPasswordEnv = "MANET_SSH_ASKPASS_PASSWORD"

// runAsAskpass reports whether this process was started as ssh's askpass
// helper, in which case it has already answered the prompt.
func runAsAskpass() bool {
	pw, ok := os.LookupEnv(askpassPasswordEnv)
	if !ok {
		return false
	}
	fmt.Println(pw)
	return true
}

// sshUserRE keeps user from being read as an ssh option ("-oProxyCommand=").
var sshUserRE = regexp.MustCompile(`^[a-zA-Z0-9._][a-zA-Z0-9._-]*$`)

// sshCommand builds ssh to user@target running remote (empty: a login
// shell), authenticating with password through askpass when one is given.
func sshCommand(user, target, password string, tty bool, remote string) (*exec.Cmd, error) {
	if !validateTargetRE.MatchString(target) || target[0] == '-' {
		return nil, fmt.Errorf("invalid target")
	}
	if !sshUserRE.MatchString(user) {
		return nil, fmt.Errorf("invalid user")
	}
	args := []string{"-o", "StrictHostKeyChecking=no", "-o", "ConnectTimeout=5"}
	if tty {
		args = append(args, "-tt")
	}
	args = append(args, user+"@"+target)
	if remote != "" {
		args = append(args, remote)
	}
	cmd := exec.Command("ssh", args...)
	cmd.Env = os.Environ()
	if password != "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cmd.Env = append(cmd.Env, "SSH_ASKPASS="+exe, "SSH_ASKPASS_REQUIRE=force", askpassPasswordEnv+"="+password)
	}
	return cmd, nil
}
