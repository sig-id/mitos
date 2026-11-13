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
