package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #137 part 3, end to end: a workspace that ran a package install
// produces a patch free of the dependency tree, a change list free of it, and
// a harvest manifest that records the omission. This is the reproduction
// path from the issue (npm install then harvest), with a synthetic
// dependency tree standing in for the multi-hundred-megabyte real one.
func TestHarvestOmitsDerivableDependencyTree(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	// The project's own ignore rules declare the dependency tree derivable.
	writeTree(t, source, map[string]string{
		"package.json":                       `{"name":"app","dependencies":{"left-pad":"1.0.0"}}`,
		".gitignore":                         "node_modules/\n",
		"src/main.js":                        "console.log('work')",
		"src/helper.js":                      "export const x = 1",
		"node_modules/left-pad/package.json": `{"name":"left-pad"}`,
		"node_modules/left-pad/index.js":     strings.Repeat("// dependency payload\n", 512),
		"node_modules/.package-lock.json":    `{"lock":true}`,
	})
	before, err := CaptureSource(context.Background(), source, base, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	history, err := Prepare(context.Background(), base, before)
	if err != nil {
		t.Fatal(err)
	}
	// The worker both edits source and installs dependencies.
	work := filepath.Join(base, "workspace")
	writeTree(t, work, map[string]string{
		"src/main.js":                        "console.log('shipped')",
		"src/new.js":                         "export const y = 2",
		"node_modules/left-pad/package.json": `{"name":"left-pad"}`,
		"node_modules/left-pad/index.js":     strings.Repeat("// dependency payload\n", 1024),
		"node_modules/.package-lock.json":    `{"lock":true}`,
		"node_modules/extra-dep/index.js":    "extra",
	})
	harvest, err := history.Harvest(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(harvest.Patch, []byte("node_modules")) {
		t.Fatalf("patch embeds the dependency tree (%d bytes):\n%.400s", len(harvest.Patch), harvest.Patch)
	}
	for _, change := range harvest.Changes {
		if strings.HasPrefix(change.Path, "node_modules/") {
			t.Fatalf("change list embeds the dependency tree: %+v", change)
		}
	}
	changed := map[string]bool{}
	for _, change := range harvest.Changes {
		changed[change.Path] = true
	}
	for _, want := range []string{"src/main.js", "src/new.js"} {
		if !changed[want] {
			t.Fatalf("real work missing from the change list: %s (%v)", want, harvest.Changes)
		}
	}
	manifest, err := harvest.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		OmittedDerivable []OmittedDirectory `json:"omitted_derivable"`
	}
	if err = json.Unmarshal(manifest, &record); err != nil {
		t.Fatal(err)
	}
	if len(record.OmittedDerivable) != 1 || record.OmittedDerivable[0].Name != "node_modules" {
		t.Fatalf("omission not recorded in the manifest: %s", manifest)
	}
	omitted := record.OmittedDerivable[0]
	if omitted.Files != 4 { // left-pad/package.json, index.js, .package-lock.json, extra-dep/index.js
		t.Fatalf("omitted file count wrong: %+v", omitted)
	}
	if omitted.Bytes == 0 {
		t.Fatalf("omitted byte count not recorded: %+v", omitted)
	}
	// The verification tree replays the patch onto the pristine baseline and
	// must still match the harvested manifest exactly.
	verifyDir := filepath.Join(base, "verify")
	if err = os.MkdirAll(verifyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = history.VerificationTree(context.Background(), before, harvest, verifyDir); err != nil {
		t.Fatalf("verification tree diverged from the harvest manifest: %v", err)
	}
}
