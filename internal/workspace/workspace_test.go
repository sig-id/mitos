package workspace

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func put(t *testing.T, root, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func gitOK(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	b, err := git(context.Background(), dir, "", nil, args...)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func paths(snapshot Snapshot) []string {
	var paths []string
	for _, f := range snapshot.Files {
		paths = append(paths, f.Path)
	}
	return paths
}

func TestSourceHonorsNestedIgnoreRulesAndPreservesDirtyIndex(t *testing.T) {
	root := t.TempDir()
	gitOK(t, root, "init", "--template=")
	put(t, root, ".gitignore", "*.log\n!keep.log\ncache/\nnested/*.tmp\n!nested/keep.tmp\n")
	for path, content := range map[string]string{"tracked.log": "tracked", "keep.log": "keep", "drop.log": "drop", "cache/result": "cache", "nested/drop.tmp": "drop", "nested/keep.tmp": "keep", "vendor/source.go": "source", "dirty.txt": "before"} {
		put(t, root, path, content)
	}
	gitOK(t, root, "add", "--force", "tracked.log", "dirty.txt")
	gitOK(t, root, "commit", "-m", "initial")
	put(t, root, "dirty.txt", "staged")
	gitOK(t, root, "add", "dirty.txt")
	put(t, root, "dirty.txt", "unstaged")
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	headBefore := gitOK(t, root, "rev-parse", "HEAD")
	snapshot, err := CaptureSource(context.Background(), root, "", DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{".gitignore", "dirty.txt", "keep.log", "nested/keep.tmp", "tracked.log", "vendor/source.go"}
	if !reflect.DeepEqual(paths(snapshot), expected) {
		t.Fatalf("wrong ignored/tracked paths: %v", paths(snapshot))
	}
	if string(snapshot.Files[1].Data) != "unstaged" {
		t.Fatal("working-tree edit lost")
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	if !bytes.Equal(indexBefore, indexAfter) || !bytes.Equal(headBefore, gitOK(t, root, "rev-parse", "HEAD")) {
		t.Fatal("source index or branch changed")
	}
}

func TestPlainDirectoryIgnoreRulesBoundsAndSymlinkIntake(t *testing.T) {
	root := t.TempDir()
	put(t, root, ".gitignore", "ignored/\n*.tmp\n!keep.tmp\n")
	put(t, root, "keep.tmp", "yes")
	put(t, root, "drop.tmp", "no")
	put(t, root, "ignored/data", "no")
	outside := t.TempDir()
	put(t, outside, "private", "secret")
	if err := os.Symlink(filepath.Join(outside, "private"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	put(t, data, "events.jsonl", "excluded")
	snapshot, err := CaptureSource(context.Background(), root, data, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths(snapshot), []string{".gitignore", "keep.tmp"}) {
		t.Fatalf("unexpected intake: %v", paths(snapshot))
	}
	if _, err = os.Stat(filepath.Join(root, ".git")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("plain source was initialized as a repository")
	}
	put(t, root, "largest", strings.Repeat("x", 100))
	_, err = CaptureSource(context.Background(), root, data, Limits{Bytes: 50, Files: 100})
	var ceiling *LimitError
	if !errors.As(err, &ceiling) || len(ceiling.Largest) == 0 || ceiling.Largest[0].Path != "largest" || !strings.Contains(err.Error(), "largest") {
		t.Fatalf("ceiling lacks named contributors: %v", err)
	}
}

func TestHarvestBinaryDeletesModesAndReconstruction(t *testing.T) {
	source := t.TempDir()
	put(t, source, "remove", "old")
	put(t, source, "text", "before\n")
	put(t, source, "script", "#!/bin/sh\nexit 0\n")
	before, err := CaptureSource(context.Background(), source, "", DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	history, err := Prepare(context.Background(), base, before)
	if err != nil {
		t.Fatal(err)
	}
	worker := filepath.Join(base, "workspace")
	if b := gitOK(t, worker, "status", "--porcelain"); len(b) != 0 {
		t.Fatalf("worker starts dirty: %s", b)
	}
	if b := gitOK(t, worker, "remote"); len(b) != 0 {
		t.Fatalf("worker has a remote: %s", b)
	}
	put(t, worker, "text", "after\n")
	put(t, worker, "new\nfile", "linebreak path\n")
	put(t, worker, "binary", string([]byte{0, 1, 2, 255, 0, 3}))
	if err = os.Remove(filepath.Join(worker, "remove")); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(filepath.Join(worker, "script"), 0700); err != nil {
		t.Fatal(err)
	}
	// A changed ignore file must not conceal newly generated deliverables.
	put(t, worker, ".gitignore", "binary\n")
	// Worker commits must not replace the protected baseline used by harvest.
	gitOK(t, worker, "add", "-A")
	gitOK(t, worker, "commit", "-m", "worker changed history")
	harvested, err := history.Harvest(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	if len(harvested.Changes) != 6 || !bytes.Contains(harvested.Patch, []byte("GIT binary patch")) {
		t.Fatalf("incomplete harvest: %+v\n%s", harvested.Changes, harvested.Patch)
	}
	reconstructed := filepath.Join(t.TempDir(), "workspace")
	if err = history.VerificationTree(context.Background(), before, harvested, reconstructed); err != nil {
		t.Fatal(err)
	}
	after, err := CaptureResult(context.Background(), reconstructed, DefaultLimits)
	if err != nil || after.Fingerprint != harvested.Fingerprint {
		t.Fatalf("bad reconstruction: %v", err)
	}
	archive, err := harvested.Snapshot.Archive()
	if err != nil {
		t.Fatal(err)
	}
	compressed, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == "binary" {
			b, _ := io.ReadAll(reader)
			found = bytes.Equal(b, []byte{0, 1, 2, 255, 0, 3})
		}
	}
	if !found {
		t.Fatal("binary result missing from portable archive")
	}
}

func TestGitIgnoresHostHelpersAndUnsafeResultLinks(t *testing.T) {
	source := t.TempDir()
	gitOK(t, source, "init", "--template=")
	put(t, source, "file", "data")
	marker := filepath.Join(t.TempDir(), "called")
	helper := filepath.Join(t.TempDir(), "helper")
	put(t, filepath.Dir(helper), filepath.Base(helper), "#!/bin/sh\nprintf called > '"+marker+"'\n")
	if err := os.Chmod(helper, 0700); err != nil {
		t.Fatal(err)
	}
	gitOK(t, source, "config", "core.fsmonitor", helper)
	hostConfig := filepath.Join(t.TempDir(), "config")
	put(t, filepath.Dir(hostConfig), filepath.Base(hostConfig), "[core]\nfsmonitor = "+helper+"\n[init]\ntemplateDir = /no/such/template\n")
	t.Setenv("GIT_CONFIG_GLOBAL", hostConfig)
	before, err := CaptureSource(context.Background(), source, "", DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	history, err := Prepare(context.Background(), t.TempDir(), before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ambient fsmonitor executed")
	}
	if err = os.Symlink("/etc/passwd", filepath.Join(history.Base, "workspace", "unsafe")); err != nil {
		t.Fatal(err)
	}
	harvest, err := history.Harvest(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	if err = history.VerificationTree(context.Background(), before, harvest, filepath.Join(t.TempDir(), "result")); err == nil {
		t.Fatal("escaping result link admitted to verification")
	}
}

func TestCheckpointLoadAndRestoreAreBoundToCommitAndFingerprint(t *testing.T) {
	source := t.TempDir()
	put(t, source, "file", "initial")
	before, err := CaptureSource(context.Background(), source, "", DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	history, err := Prepare(context.Background(), t.TempDir(), before)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(history.Base, "workspace"), "file", "checkpoint")
	harvest, err := history.Harvest(context.Background(), before)
	if err != nil {
		t.Fatal(err)
	}
	if err = history.Pin(context.Background(), harvest.ResultCommit); err != nil {
		t.Fatal(err)
	}
	if _, err = history.Load(context.Background(), harvest.ResultCommit, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong checkpoint fingerprint accepted")
	}
	destination := filepath.Join(history.Base, "segments", "restored")
	for range 2 {
		if err = history.Restore(context.Background(), harvest.ResultCommit, harvest.Fingerprint, destination); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := os.ReadFile(filepath.Join(destination, "workspace", "file"))
	if err != nil || string(restored) != "checkpoint" {
		t.Fatal("wrong checkpoint restored")
	}
	put(t, filepath.Join(destination, "workspace"), "file", "changed later")
	if err = history.Restore(context.Background(), harvest.ResultCommit, harvest.Fingerprint, destination); err == nil {
		t.Fatal("re-drive overwrote an altered recovery destination")
	}
	preserved, _ := os.ReadFile(filepath.Join(destination, "workspace", "file"))
	if string(preserved) != "changed later" {
		t.Fatal("altered destination was destroyed")
	}
}
