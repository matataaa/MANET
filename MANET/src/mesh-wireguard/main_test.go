package main

import "testing"

func TestValidateConfig(t *testing.T) {
	ok := Config{Interface: "wg0", Address: "10.9.0.1/24", DNS: "1.1.1.1",
		Peers: []WGPeer{{PublicKey: "abc=", Endpoint: "1.2.3.4:51820", AllowedIPs: "0.0.0.0/0"}}}
	if err := validateConfig(ok); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	bad := map[string]Config{
		"PostUp via DNS":       {DNS: "1.1.1.1\nPostUp = id"},
		"PostUp via peer":      {Peers: []WGPeer{{AllowedIPs: "0.0.0.0/0\r\nPostUp = id"}}},
		"path in interface":    {Interface: "../../tmp/x"},
		"too long interface":   {Interface: "abcdefghijklmnop"},
		"control char in addr": {Address: "10.9.0.1/24\x00"},
	}
	for name, c := range bad {
		if validateConfig(c) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
