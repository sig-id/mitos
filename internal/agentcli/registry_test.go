package agentcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fakeCLI(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoveryProfilesAndSecretBoundaries(t *testing.T) {
	bin, home, work := t.TempDir(), t.TempDir(), t.TempDir()
	fakeCLI(t, bin, "codex", "printf '%s\\n' \"$OPENAI_API_KEY\"; echo 'codex-cli 1.2.3'\n")
	if err := os.WriteFile(filepath.Join(work, "auth.json"), []byte(`{"secret":"never-read-file-token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	config := Config{Profiles: []Profile{{ID: "work", Name: "Work account", AgentID: "codex", Env: map[string]string{"OPENAI_API_KEY": "WORK_KEY", "CODEX_HOME": "WORK_HOME"}, Unset: []string{"ANTHROPIC_API_KEY"}}}}
	r, err := New(Builtins(), config, []string{"PATH=" + bin, "HOME=" + home, "WORK_HOME=" + work, "OPENAI_API_KEY=personal-secret", "WORK_KEY=work-secret", "ANTHROPIC_API_KEY=unrelated-secret"})
	if err != nil {
		t.Fatal(err)
	}
	r.Refresh(context.Background())
	i, err := r.Inspect("codex", "work", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !i.Installed || i.Version != "1.2.3" || i.Capabilities.Transport != "pty" || i.Capabilities.StructuredEvents {
		t.Fatalf("bad discovery: %+v", i)
	}
	found := false
	for _, resource := range i.Resources {
		if resource.Kind == "auth" && resource.Path == filepath.Join(work, "auth.json") && resource.Present {
			found = true
		}
	}
	if !found {
		t.Fatal("profile's native auth location was not inspected")
	}
	encoded, _ := json.Marshal(i)
	for _, secret := range []string{"personal-secret", "work-secret", "unrelated-secret", "never-read-file-token"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("discovery exposed a secret")
		}
	}
	prompt := "--dangerous $(touch /tmp/must-not-execute) 'quoted'\nsecond line"
	launch, err := r.Prepare(Request{AgentID: "codex", ProfileID: "work", Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(launch.Args, []string{"--", prompt}) {
		t.Fatalf("prompt became executable flags: %q", launch.Args)
	}
	joined := strings.Join(launch.Env, "\n")
	if !strings.Contains(joined, "OPENAI_API_KEY=work-secret") || strings.Contains(joined, "ANTHROPIC_API_KEY=") {
		t.Fatal("profile environment not applied independently")
	}
	encoded, _ = json.Marshal(launch)
	if strings.Contains(string(encoded), "work-secret") {
		t.Fatal("launch JSON exposed environment values")
	}
	native, err := r.Prepare(Request{AgentID: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(native.Env, "\n"), "OPENAI_API_KEY=personal-secret") {
		t.Fatal("profile mutated native environment")
	}
	if _, err := r.Prepare(Request{AgentID: "claude", ProfileID: "work"}); err == nil {
		t.Fatal("cross-agent profile accepted")
	}
	if _, err := r.Prepare(Request{AgentID: "claude"}); err == nil {
		t.Fatal("missing executable accepted")
	}
}

func TestProbeTimeoutAndCustomApplications(t *testing.T) {
	bin := t.TempDir()
	path := fakeCLI(t, bin, "slow", "while :; do :; done\n")
	r, err := New(nil, Config{Applications: []Definition{{ID: "custom", Name: "Local app", VersionArgs: []string{"--version"}, Kind: "application", Executables: []string{path}, Args: []string{"interactive"}, Prompt: "--prompt"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	r.Refresh(ctx)
	if time.Since(start) > time.Second {
		t.Fatal("version probe was not bounded")
	}
	i, err := r.Inspect("custom", "", "")
	if err != nil || i.Probe != "timeout" || !i.Installed {
		t.Fatal(i, err)
	}
	launch, err := r.Prepare(Request{AgentID: "custom", Prompt: "literal $(text)"})
	if err != nil || !reflect.DeepEqual(launch.Args, []string{"interactive", "--prompt", "literal $(text)"}) {
		t.Fatal(launch, err)
	}
}

func TestCatalogValidation(t *testing.T) {
	for _, config := range []Config{
		{Applications: []Definition{{ID: "codex", Name: "Duplicate", Kind: "agent", Executables: []string{"codex"}, Prompt: "none"}}},
		{Profiles: []Profile{{ID: "bad", Name: "Bad", AgentID: "codex", Env: map[string]string{"OPENAI_API_KEY": "literal-secret-value"}}}},
		{Profiles: []Profile{{ID: "bad", Name: "Bad", AgentID: "missing"}}},
	} {
		if _, err := New(Builtins(), config, nil); err == nil {
			t.Fatal("invalid catalog accepted")
		}
	}
	r, err := New(Builtins(), Config{Profiles: []Profile{{ID: "missing", Name: "Missing", AgentID: "codex", Env: map[string]string{"OPENAI_API_KEY": "UNSET_KEY"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Prepare(Request{AgentID: "codex", ProfileID: "missing"}); err == nil || !strings.Contains(err.Error(), "UNSET_KEY") {
		t.Fatal("missing credential reference not diagnosed", err)
	}
}

func TestVersionProbesBelongToAdapterAndProfile(t *testing.T) {
	path := fakeCLI(t, t.TempDir(), "wrapper", `if [ "$FLAVOR" = "work" ]; then echo 2.0.0; else echo 1.0.0; fi`)
	r, err := New(nil, Config{Applications: []Definition{
		{ID: "probed", Name: "Probed", Kind: "agent", Executables: []string{path}, Prompt: "none", VersionArgs: []string{"--version"}},
		{ID: "unprobed", Name: "Unprobed", Kind: "application", Executables: []string{path}, Prompt: "none"},
	}, Profiles: []Profile{{ID: "work", Name: "Work", AgentID: "probed", Env: map[string]string{"FLAVOR": "WORK_FLAVOR"}}}}, []string{"WORK_FLAVOR=work"})
	if err != nil {
		t.Fatal(err)
	}
	r.Refresh(context.Background())
	for _, tc := range []struct{ id, profile, version, status string }{{"probed", "", "1.0.0", "ok"}, {"probed", "work", "2.0.0", "ok"}, {"unprobed", "", "", "not_probed"}} {
		i, err := r.Inspect(tc.id, tc.profile, "")
		if err != nil || i.Version != tc.version || i.Probe != tc.status {
			t.Fatal("probe leaked between adapters or profiles", i, err)
		}
	}
}
