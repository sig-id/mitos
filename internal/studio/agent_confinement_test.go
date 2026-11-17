package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/compute"
)

// Confinement is scoped to the agent provider: applications and structured
// work keep the plain local posture, and a withheld network refuses agent
// admission instead of launching a runtime that can never reach its model.
func TestConfinementIsScopedAndFailsClosedWithoutTransport(t *testing.T) {
	if _, err := os.Stat("/nonexistent-bwrap"); err == nil {
		t.Skip("unreachable")
	}
	bin := t.TempDir()
	fake := filepath.Join(bin, "bwrap")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MITOS_AGENT_CONFINEMENT", "bwrap")
	t.Setenv("MITOS_AGENT_NETWORK", "off")
	e, _ := graphEngine(t, false)
	if e.provider.Capabilities().WorkspaceConfined {
		t.Fatal("shared provider replaced by the confining one")
	}
	if !e.agentProvider.Capabilities().WorkspaceConfined || !e.confinement.Enabled() {
		t.Fatal("agent provider not confined")
	}
	if !confinementBlocksTransport(e.confinement) {
		t.Fatal("net-off confinement reported as transport-safe")
	}
	if confinementBlocksTransport(compute.Confinement{Tool: compute.ConfinementBwrap, NetMode: compute.NetUnrestricted}) {
		t.Fatal("unrestricted egress reported as blocked")
	}
	if confinementBlocksTransport(compute.Confinement{}) {
		t.Fatal("unconfined engine reported as blocked")
	}
}

// Authentication is staged into the sandbox home and the environment is
// rewritten to it; the host home is never referenced afterwards.
func TestStageConfinementHomeStagesAuthentication(t *testing.T) {
	host := t.TempDir()
	config := filepath.Join(host, ".codex")
	if err := os.MkdirAll(filepath.Join(config, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "nested", "config.toml"), []byte("key = 1"), 0600); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	env := []string{"PATH=/usr/bin", "HOME=" + host, "OTHER=1"}
	out, err := stageConfinementHome(base, "codex", env)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	staged := filepath.Join(home, ".codex")
	body, err := os.ReadFile(filepath.Join(staged, "nested", "config.toml"))
	if err != nil || string(body) != "key = 1" {
		t.Fatalf("config not staged: %q %v", body, err)
	}
	if info, err := os.Stat(filepath.Join(staged, "auth.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("staged credentials not 0600: %+v %v", info, err)
	}
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "HOME="+home) || !strings.Contains(joined, "CODEX_HOME="+staged) || strings.Contains(joined, "HOME="+host) {
		t.Fatalf("environment not rewritten to the sandbox home: %v", out)
	}
	// A runtime without a registered config directory keeps its environment.
	same, err := stageConfinementHome(base, "pi", env)
	if err != nil || strings.Join(same, "\n") != strings.Join(env, "\n") {
		t.Fatalf("unrelated runtime environment rewritten: %v %v", same, err)
	}
	// Special files are refused rather than partially staged.
	secret := filepath.Join(host, ".claude")
	if err = os.MkdirAll(secret, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(secret, "token"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(secret, "token"), filepath.Join(secret, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err = stageConfinementHome(t.TempDir(), "claude", []string{"HOME=" + host}); err == nil || !strings.Contains(err.Error(), "refusing to stage") {
		t.Fatalf("symlink staged: %v", err)
	}
}
