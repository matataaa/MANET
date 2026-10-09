package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeNft records nft calls; listOK says whether table manet_dhcp exists.
func fakeNft(t *testing.T, listOK bool, rules bool) *[]string {
	t.Helper()
	var calls []string
	oldCmd, oldPath := nftCmd, dhcpIsolationNFT
	t.Cleanup(func() { nftCmd, dhcpIsolationNFT = oldCmd, oldPath })
	dhcpIsolationNFT = filepath.Join(t.TempDir(), "dhcp-isolation.nft")
	if rules {
		os.WriteFile(dhcpIsolationNFT, []byte("table bridge manet_dhcp {}\n"), 0644)
	}
	nftCmd = func(_ time.Duration, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if args[0] == "list" && !listOK {
			return "", errors.New("No such file or directory")
		}
		return "", nil
	}
	return &calls
}

func TestEnsureDHCPIsolationLeavesPresentTable(t *testing.T) {
	calls := fakeNft(t, true, true)
	ensureDHCPIsolation()
	if want := []string{"nft list table bridge manet_dhcp"}; !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls %v, want only the check", *calls)
	}
}

func TestEnsureDHCPIsolationReappliesMissingTable(t *testing.T) {
	calls := fakeNft(t, false, true)
	ensureDHCPIsolation()
	want := []string{"nft list table bridge manet_dhcp", "nft -f " + dhcpIsolationNFT}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("calls %v, want %v", *calls, want)
	}
}

func TestEnsureDHCPIsolationWithoutRulesFile(t *testing.T) {
	calls := fakeNft(t, false, false)
	ensureDHCPIsolation()
	if len(*calls) != 1 {
		t.Fatalf("must not run nft -f without the rules file, calls %v", *calls)
	}
}
