package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

// Issue #137 part 3: result captures must not embed derivable dependency and
// cache directories into patches, and whatever is excluded has to be recorded
// in the snapshot so the omission is visible rather than silent.

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCaptureResultExcludesDerivableDirectories(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"src/app.js":                       "console.log('work')",
		"README.md":                        "real output",
		"node_modules/dep/a.js":            "aaaa",
		"node_modules/dep/b.js":            "bbbb",
		"nested/pkg/node_modules/dep/c.js": "cccc",
		"nested/pkg/index.js":              "module",
		"__pycache__/mod.cpython-312.pyc":  "\x00\x01binary",
		".venv/lib/site.py":                "venv",
		"tools/node_modules/.package-lock": "lock",
	})
	snapshot, err := CaptureResult(t.Context(), root, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	captured := map[string]bool{}
	for _, f := range snapshot.Files {
		captured[f.Path] = true
	}
	for _, want := range []string{"src/app.js", "README.md", "nested/pkg/index.js"} {
		if !captured[want] {
			t.Fatalf("real output missing from capture: %s (files: %v)", want, snapshot.Files)
		}
	}
	for _, banned := range []string{"node_modules/dep/a.js", "node_modules/dep/b.js", "nested/pkg/node_modules/dep/c.js", "__pycache__/mod.cpython-312.pyc", ".venv/lib/site.py", "tools/node_modules/.package-lock"} {
		if captured[banned] {
			t.Fatalf("derivable file captured: %s", banned)
		}
	}
	omitted := map[string]OmittedDirectory{}
	for _, dir := range snapshot.OmittedDerivable {
		omitted[dir.Name] = dir
	}
	for _, want := range []string{"node_modules", "nested/pkg/node_modules", "__pycache__", ".venv", "tools/node_modules"} {
		if _, ok := omitted[want]; !ok {
			t.Fatalf("exclusion not recorded in the manifest: %s (recorded: %v)", want, snapshot.OmittedDerivable)
		}
	}
	if omitted["node_modules"].Files != 2 || omitted["node_modules"].Bytes != int64(len("aaaa")+len("bbbb")) {
		t.Fatalf("node_modules footprint wrong: %+v", omitted["node_modules"])
	}
	// The fingerprint reflects only captured work products.
	if snapshot.TotalBytes != int64(len("console.log('work')")+len("real output")+len("module")) {
		t.Fatalf("captured bytes include excluded content: %d", snapshot.TotalBytes)
	}
}

func TestCaptureResultKeepsDirectoriesNamedLikeTools(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"target/design.md": "a design document, not cargo output",
		"main.go":          "package main",
	})
	snapshot, err := CaptureResult(t.Context(), root, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	captured := map[string]bool{}
	for _, f := range snapshot.Files {
		captured[f.Path] = true
	}
	// The exclusion matches directory names anywhere in the tree; this
	// asserts the documented behaviour and its cost.
	if captured["target/design.md"] {
		t.Fatalf("target/ unexpectedly captured")
	}
	if !captured["main.go"] {
		t.Fatal("ordinary file lost")
	}
	if len(snapshot.OmittedDerivable) != 1 || snapshot.OmittedDerivable[0].Name != "target" {
		t.Fatalf("target omission not recorded: %v", snapshot.OmittedDerivable)
	}
}

func TestCaptureResultWithoutDerivableDirectoriesIsUnchanged(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"a.txt":         "one",
		"sub/b.txt":     "two",
		".gitignore":    "node_modules\n",
		".hidden/cache": "kept: unknown caches are still work evidence",
	})
	snapshot, err := CaptureResult(t.Context(), root, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Files) != 4 || snapshot.OmittedDerivable != nil {
		t.Fatalf("capture without derivable directories changed: %+v %v", snapshot.Files, snapshot.OmittedDerivable)
	}
}
