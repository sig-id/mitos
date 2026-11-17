package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestCodexWorkerEnvironmentIsolatesHome(t *testing.T) {
	store := t.TempDir()
	user := t.TempDir()
	source := filepath.Join(user, ".codex")
	if err := os.MkdirAll(filepath.Join(source, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"auth.json": `{"token":"x"}`, "config.toml": "[features]\nmulti_agent_v2 = true\n", "AGENTS.md": "spawn agents"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"HOME=" + user, "OPENAI_API_KEY=k"}
	got, err := codexWorkerEnvironment(store, env)
	if err != nil {
		t.Fatal(err)
	}
	home := ""
	for _, entry := range got {
		if value, ok := strings.CutPrefix(entry, "CODEX_HOME="); ok {
			if home != "" {
				t.Fatal("CODEX_HOME set twice", got)
			}
			home = value
		}
	}
	if !strings.HasPrefix(home, filepath.Join(store, "native", "codex")+string(filepath.Separator)) {
		t.Fatalf("CODEX_HOME %q is not store-owned", home)
	}
	if !slices.Contains(got, "OPENAI_API_KEY=k") || !slices.Contains(got, "HOME="+user) {
		t.Fatal("credential environment not preserved", got)
	}
	if target, err := os.Readlink(filepath.Join(home, "auth.json")); err != nil || target != filepath.Join(source, "auth.json") {
		t.Fatalf("auth.json link = %q, %v", target, err)
	}
	entries, _ := os.ReadDir(home)
	if len(entries) != 1 {
		t.Fatalf("only the credential may pass through, found %v", entries)
	}

	// Stable per source so saved native threads resume; a profile's own
	// CODEX_HOME gets a separate home and its own credential.
	again, err := codexWorkerEnvironment(store, append(env, "CODEX_HOME="+source))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(again, "CODEX_HOME="+home) || slices.Contains(again, "CODEX_HOME="+source) {
		t.Fatal("isolated home is not stable", again)
	}
	work := t.TempDir()
	other, err := codexWorkerEnvironment(store, []string{"HOME=" + user, "CODEX_HOME=" + work})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(other, "CODEX_HOME="+home) {
		t.Fatal("profiles share a native home")
	}
	if _, err = codexWorkerEnvironment(store, []string{"CODEX_HOME=relative"}); err == nil {
		t.Fatal("relative CODEX_HOME accepted")
	}
}

// The scheduler's real launch path hands Codex the feature lockdown and the
// isolated home, not the operator's.
func TestAutomaticCodexLaunchIsLockedDown(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "config.toml"), []byte("[features]\nmulti_agent = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", source)
	record := filepath.Join(t.TempDir(), "launch.json")
	e, req := automaticScriptEngine(t, fmt.Sprintf(`import json,os,pathlib,sys
sys.stdin.read()
argv=sys.modules['__main__'].oldargv
pathlib.Path(%q).write_text(json.dumps({'argv':argv,'home':os.environ.get('CODEX_HOME','')}))
`, record))
	if _, err := e.StartAutomaticGoal(AutomaticGoal{Key: "lockdown", ProjectID: req.ProjectID, Goal: "Record the launch"}); err != nil {
		t.Fatal(err)
	}
	var launch struct {
		Argv []string `json:"argv"`
		Home string   `json:"home"`
	}
	until := time.Now().Add(10 * time.Second)
	for {
		data, err := os.ReadFile(record)
		if err == nil && json.Unmarshal(data, &launch) == nil {
			break
		}
		if time.Now().After(until) {
			t.Fatal("codex was never launched")
		}
		time.Sleep(25 * time.Millisecond)
	}
	joined := strings.Join(launch.Argv, " ")
	for _, want := range []string{"app-server -c features.multi_agent=false -c features.multi_agent_v2=false", "shell_environment_policy.set.GOCACHE="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("launch %q lacks %q", joined, want)
		}
	}
	if launch.Home == source || !strings.HasPrefix(launch.Home, filepath.Join(e.Store.Dir(), "native", "codex")) {
		t.Fatalf("worker CODEX_HOME %q is not isolated", launch.Home)
	}
	if _, err := os.Stat(filepath.Join(launch.Home, "config.toml")); !os.IsNotExist(err) {
		t.Fatal("operator config reached the worker home", err)
	}
}
