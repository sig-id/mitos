package observation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocalActivityObservesWritesAndNestedDirectories(t *testing.T) {
	root := t.TempDir()
	watcher, err := NewLocalActivity(root)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	initial, err := watcher.Sample(0)
	if err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(root, "nested"), 0700)
	created, err := watcher.Sample(0)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "nested", "result"), []byte("first"), 0600)
	written, err := watcher.Sample(0)
	if err != nil {
		t.Fatal(err)
	}
	// Rewriting the same bytes is still a write, unlike a fingerprint poll.
	os.WriteFile(filepath.Join(root, "nested", "result"), []byte("first"), 0600)
	repeated, err := watcher.Sample(0)
	if err != nil {
		t.Fatal(err)
	}
	if created.WorkspaceWrites <= initial.WorkspaceWrites || written.WorkspaceWrites <= created.WorkspaceWrites || repeated.WorkspaceWrites <= written.WorkspaceWrites {
		t.Fatalf("workspace writes lost: %+v %+v %+v %+v", initial, created, written, repeated)
	}
	watcher.SetPID(os.Getpid())
	own, err := watcher.Sample(7)
	if err != nil || own.TreeSize < 1 || len(own.Execs) < 1 || own.OutputOffset != 7 {
		t.Fatalf("process observation failed: %+v %v", own, err)
	}
}

func TestLocalActivityFollowsRenamesAndDropsMovedOutDirectories(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "old")
	renamed := filepath.Join(root, "renamed")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	watcher, err := NewLocalActivity(root)
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.Close()
	if err = os.Rename(old, renamed); err != nil {
		t.Fatal(err)
	}
	before, err := watcher.Sample(0)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(renamed, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = watcher.Sample(0); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(renamed, "child", "file"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	written, err := watcher.Sample(0)
	if err != nil || written.WorkspaceWrites <= before.WorkspaceWrites {
		t.Fatalf("rename lost observation: %+v %v", written, err)
	}
	outside := filepath.Join(t.TempDir(), "moved")
	if err = os.Rename(renamed, outside); err != nil {
		t.Fatal(err)
	}
	moved, err := watcher.Sample(0)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(outside, "child", "file"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := watcher.Sample(0)
	if err != nil || after.WorkspaceWrites != moved.WorkspaceWrites {
		t.Fatalf("outside writes counted: %+v %+v %v", moved, after, err)
	}
}
