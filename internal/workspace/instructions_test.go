package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFrozenInstructionsBecomeBaseline(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("new external edits"), 0600)
	os.WriteFile(filepath.Join(root, "source.txt"), []byte("source"), 0600)
	original, err := CaptureResult(context.Background(), root, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := WithInstructions(original, "AGENTS.md", []byte("frozen guidance"))
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Fingerprint == original.Fingerprint || string(original.Files[0].Data) != "new external edits" {
		t.Fatal("snapshot was not copied")
	}
	history, err := Prepare(context.Background(), t.TempDir(), frozen)
	if err != nil {
		t.Fatal(err)
	}
	result, err := CaptureResult(context.Background(), filepath.Join(history.Base, "workspace"), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fingerprint != frozen.Fingerprint {
		t.Fatal("frozen baseline fingerprint differs from materialized guidance")
	}
}

func TestCooperationSnapshotReplacesOnlyCooperation(t *testing.T) {
	s := Snapshot{Files: []File{{Path: "AGENTS.md", Data: []byte("agents"), Bytes: 6}, {Path: "COOPERATION.md", Data: []byte("live"), Bytes: 4}}}
	frozen, err := WithCooperation(s, []byte("frozen"), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen.Files) != 2 || string(frozen.Files[0].Data) != "agents" || string(frozen.Files[1].Data) != "frozen" {
		t.Fatal(frozen)
	}
	absent, err := WithCooperation(s, nil, false)
	if err != nil || len(absent.Files) != 1 || absent.Files[0].Path != "AGENTS.md" {
		t.Fatal(absent, err)
	}
}
