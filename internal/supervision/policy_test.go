package supervision

import (
	"encoding/json"
	"fmt"
	"mitos/internal/agentpolicy"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceBoundaryResolvesSymlinkAncestors(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	p := agentpolicy.Default()
	p.Supervision.WorkspaceOnly = true
	for _, path := range []string{filepath.Join(root, "escape", "new.txt"), filepath.Join(outside, "new.txt")} {
		raw := json.RawMessage(fmt.Sprintf(`{"method":"item/completed","params":{"item":{"type":"fileChange","changes":[{"path":%q}]}}}`, path))
		if Check(p, root, raw) == "" {
			t.Fatal("outside path permitted", path)
		}
	}
	raw := json.RawMessage(fmt.Sprintf(`{"method":"item/started","params":{"item":{"type":"commandExecution","cwd":%q,"command":"ls"}}}`, root))
	if got := Check(p, root, raw); got != "" {
		t.Fatal(got)
	}
	p.Supervision.Mode = "off"
	if got := Check(p, root, json.RawMessage(`{}`)); got != "" {
		t.Fatal(got)
	}
}

func TestClaudeFileWorkspaceBoundaries(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	p := agentpolicy.Default()
	p.Supervision.WorkspaceOnly = true
	for _, tool := range []string{"Write", "Edit"} {
		for _, tc := range []struct {
			name, cwd, path string
			allowed         bool
		}{
			{"absolute inside", root, filepath.Join(root, "new.txt"), true},
			{"relative inside", root, "new.txt", true},
			{"relative to reported cwd", filepath.Join(root, "sub"), "../new.txt", true},
			{"absolute outside", root, filepath.Join(outside, "new.txt"), false},
			{"relative outside", root, "../new.txt", false},
			{"symlink ancestor", root, "escape/new.txt", false},
			{"outside cwd", outside, "new.txt", false},
			{"missing cwd", "", "new.txt", false},
			{"missing target", root, "", false},
		} {
			t.Run(tool+"/"+tc.name, func(t *testing.T) {
				raw, _ := json.Marshal(map[string]any{"session_id": "s", "tool_use_id": "t", "hook_event_name": "PreToolUse", "tool_name": tool, "cwd": tc.cwd, "tool_input": map[string]string{"file_path": tc.path}})
				if reason := Check(p, root, raw); (reason == "") != tc.allowed {
					t.Fatalf("allowed=%v: %s", tc.allowed, reason)
				}
			})
		}
	}
}
