package main

import (
	"strings"
	"testing"
)

func TestSSHCommand(t *testing.T) {
	cmd, err := sshCommand("radio", "10.30.2.90", "pw with spaces", false, "bash -l -c 'echo hi'")
	if err != nil {
		t.Fatal(err)
	}
	args := cmd.Args
	if args[0] != "ssh" || args[len(args)-2] != "radio@10.30.2.90" || args[len(args)-1] != "bash -l -c 'echo hi'" {
		t.Fatalf("args = %q", args)
	}
	for _, a := range args {
		if strings.Contains(a, "pw with spaces") {
			t.Fatal("password on the command line")
		}
	}
	env := strings.Join(cmd.Env, "\n")
	for _, want := range []string{"SSH_ASKPASS_REQUIRE=force", "SSH_ASKPASS=/", askpassPasswordEnv + "=pw with spaces"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}

	nopw, _ := sshCommand("radio", "eud4", "", true, "")
	if strings.Contains(strings.Join(nopw.Env, "\n"), "SSH_ASKPASS") || nopw.Args[len(nopw.Args)-1] != "radio@eud4" {
		t.Errorf("no-password command: args %q", nopw.Args)
	}

	for _, bad := range [][2]string{
		{"-oProxyCommand=touch /tmp/x", "10.30.2.90"},
		{"radio", "-oProxyCommand=x"},
		{"radio", "10.30.2.90 -v"},
		{"", "10.30.2.90"},
	} {
		if _, err := sshCommand(bad[0], bad[1], "", false, ""); err == nil {
			t.Errorf("accepted user %q target %q", bad[0], bad[1])
		}
	}
}
