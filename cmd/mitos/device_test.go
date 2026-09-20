package main

import "testing"

func TestDeviceConfigurationValidation(t *testing.T) {
	for _, args := range [][]string{
		{"serve", "--device-addr", ":8443"},
		{"serve", "--device-cert", "missing.pem"},
		{"serve", "--device-stt-command", "transcribe"},
		{"device", "pair", "--url", "http://remote.example"},
		{"device", "list", "--url", "https://localhost"},
		{"device", "revoke", "invalid/id"},
	} {
		if err := run(args); err == nil {
			t.Fatal("invalid configuration accepted", args)
		}
	}
}
