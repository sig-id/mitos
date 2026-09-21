package studio

import (
	"context"
	"encoding/json"
	"errors"
	"mitos/internal/acceptance"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func testBrief(root string) Brief {
	return Brief{Title: "Inspect the workspace", Workspace: root, AppID: "workspace-inventory", TimeoutMinutes: 1}
}
func createRun(t *testing.T, s *Store) Run {
	t.Helper()
	r, err := s.Create(testBrief("."), "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReplayRepairsOnlyIncompleteTail(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	if err = s.SaveArtifact(r.ID, "report.txt", []byte("a real result")); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(r.ID, "Checked", true); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(r.ID)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"seq":4`)
	_ = f.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "done" || !after.Verified || len(after.Events) != len(before.Events) {
		t.Fatalf("incorrect replay: %+v", after)
	}
	b, err := s.Artifact(r.ID, "report.txt")
	if err != nil || string(b) != "a real result" {
		t.Fatalf("artifact: %s %v", b, err)
	}
	if _, err = s.Create(testBrief("."), "Inventory", "", false); err != nil {
		t.Fatal(err)
	}
}
func TestCorruptCompletedRecordIsRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	createRun(t, s)
	_ = s.Close()
	path := filepath.Join(dir, "events.jsonl")
	b, _ := os.ReadFile(path)
	b = []byte(strings.Replace(string(b), "Inspect the workspace", "Changed the workspace", 1))
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenStore(dir); err == nil {
		reopened.Close()
		t.Fatal("corruption accepted")
	}
}
func TestStoreHasOneProcessWriter(t *testing.T) {
	s := testStore(t)
	if second, err := OpenStore(s.Dir()); err == nil {
		second.Close()
		t.Fatal("second writer admitted")
	}
}
func TestConcurrentWritesAndImmutableReads(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.RecordDecision(r.ID, "advance", "Next step"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _ := s.Get(r.ID)
	if len(got.Events) != 21 {
		t.Fatalf("lost events: %d", len(got.Events))
	}
	got.Brief.Title = "mutation"
	got.Events[0].Data[0] = 'x'
	fresh, _ := s.Get(r.ID)
	if fresh.Brief.Title == "mutation" || fresh.Events[0].Data[0] == 'x' {
		t.Fatal("read changed canonical data")
	}
	for i, event := range fresh.Events {
		if event.Seq != int64(i+1) || event.Hash != eventHash(event) {
			t.Fatal("invalid ordering or hash")
		}
	}
}
func TestArtifactIntegrityAndTerminalGuard(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	if err := s.SaveArtifact(r.ID, "../escape", []byte("bad")); err == nil {
		t.Fatal("path accepted")
	}
	if err := s.SaveArtifact(r.ID, "report.txt", []byte("valid")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Dir(), "artifacts", r.ID, "report.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Artifact(r.ID, "report.txt"); err == nil {
		t.Fatal("tampering not detected")
	}
	if err := s.Complete(r.ID, "Finished", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(r.ID, "run_started", "running", "Working", "Again"); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal state reopened: %v", err)
	}
}
func TestDecisionResolutionIsIdempotentAndRejectsStaleChoices(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	d := recoveryDecision()
	if err := s.Park(r.ID, d); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(r.ID, "stale", "cancel"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale card accepted")
	}
	if err := s.Resolve(r.ID, d.ID, "invalid"); err == nil {
		t.Fatal("invalid option accepted")
	}
	if err := s.Resolve(r.ID, d.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(r.ID)
	if err := s.Resolve(r.ID, d.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(r.ID)
	if len(after.Events) != len(before.Events)+1 || after.Events[len(after.Events)-1].Kind != "decision_rejected" {
		t.Fatal("duplicate resolution must be audited without reapplying it")
	}
	if err := s.Resolve(r.ID, d.ID, "retry"); !errors.Is(err, ErrConflict) {
		t.Fatal("conflicting choice accepted")
	}
}

func waitRun(t *testing.T, s *Store, id string, predicate func(Run) bool) Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(r) {
			return r
		}
		time.Sleep(15 * time.Millisecond)
	}
	r, _ := s.Get(id)
	t.Fatalf("run never reached expected state: %+v", r)
	return r
}
func testEngine(t *testing.T, root string, apps []Application) (*Engine, *Store) {
	t.Helper()
	s := testStore(t)
	e, err := NewEngine(s, apps, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e, s
}

func TestInventoryExecutesAndPreservesSource(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "private.txt"), []byte("outside"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	apps, _ := LoadApps("")
	e, s := testEngine(t, root, apps)
	r, err := e.Launch(testBrief(root), "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "done" || !r.Verified || len(r.Artifacts) != 2 {
		t.Fatalf("inventory failed: %+v", r)
	}
	b, err := s.Artifact(r.ID, "inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err = json.Unmarshal(b, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Files) != 1 || inventory.Files[0].Path != "hello.txt" {
		t.Fatalf("unexpected inventory: %s", b)
	}
	if original, _ := os.ReadFile(filepath.Join(root, "hello.txt")); string(original) != "hello" {
		t.Fatal("source changed")
	}
	if _, err = e.Launch(testBrief(outside), ""); err == nil {
		t.Fatal("workspace outside root accepted")
	}
	if _, err = e.Launch(testBrief(filepath.Join(root, "linked")), ""); err == nil {
		t.Fatal("symlink escaped workspace root")
	}
}

func TestOpenedProjectWorkspaceRunsAndSurvivesRestart(t *testing.T) {
	s := testStore(t)
	root, projectDir, outside := t.TempDir(), t.TempDir(), t.TempDir()
	apps, err := LoadApps("")
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(s, apps, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { e.Close() }()
	if _, err := e.Launch(testBrief(projectDir), ""); err == nil {
		t.Fatal("unconfirmed directory accepted")
	}
	if _, err := e.Desk.CreateProject("Selected workspace", projectDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "project.txt"), []byte("project content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(projectDir, "outside")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Workspace(filepath.Join(projectDir, "outside")); err == nil {
		t.Fatal("symlink escaped confirmed workspace")
	}
	e.Close()
	e, err = NewEngine(s, apps, root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Launch(testBrief(projectDir), "")
	if err != nil {
		t.Fatal("confirmed workspace lost on restart", err)
	}
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "done" || r.Brief.Workspace != projectDir {
		t.Fatal("task did not run in selected workspace", r.Status, r.Brief.Workspace)
	}
	if _, err := e.Workspace(outside); err == nil {
		t.Fatal("unconfirmed sibling became authorized")
	}
}
func TestAcceptanceFailureNeverMarksWorkVerified(t *testing.T) {
	root := t.TempDir()
	app := Application{ID: "test-app", Name: "Test", Entrypoint: "Run", Directory: t.TempDir(), Command: []string{"/bin/sh", "-c", "printf 'done'"}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "printf 'rejected'; exit 1"}}}
	e, s := testEngine(t, root, []Application{app})
	b := testBrief(root)
	b.AppID = app.ID
	r, err := e.Launch(b, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "failed" || r.Verified {
		t.Fatalf("false verification: %+v", r)
	}
	for _, name := range []string{"output.txt", "acceptance.txt", "changes.patch", "changes.json", "workspace.tar.gz"} {
		if _, err := s.Artifact(r.ID, name); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
}
func TestCommandAcceptanceAndCancellation(t *testing.T) {
	root := t.TempDir()
	app := Application{ID: "test-app", Name: "Test", Entrypoint: "Run", Directory: t.TempDir(), Command: []string{"/bin/sh", "-c", "printf 'result' > result.txt; printf 'complete'"}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "test \"$PWD\" != \"$MITOS_WORKSPACE\" && test -s \"$MITOS_WORKSPACE/result.txt\" && printf 'pass'"}}}
	e, s := testEngine(t, root, []Application{app})
	b := testBrief(root)
	b.AppID = app.ID
	r, err := e.Launch(b, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "done" || !r.Verified {
		t.Fatalf("command failed: %+v", r)
	}
	child, err := e.Retry(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := e.Retry(r.ID)
	if err != nil || duplicate.ID != child.ID {
		t.Fatal("retry was not idempotent")
	}
	if child.TaskID != r.TaskID {
		t.Fatal("retry lost task identity")
	}
	if err = e.Cancel(child.ID); err != nil {
		t.Fatal(err)
	}
	after := waitRun(t, s, child.ID, func(r Run) bool { return r.Terminal() })
	if after.Status != "cancelled" {
		t.Fatal("cancel did not stop work")
	}
}
func TestCommandTimeoutKillsProcessGroup(t *testing.T) {
	base := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := executeCommand(ctx, []string{"/bin/sh", "-c", "sleep 30 & wait"}, base, base, nil)
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("process group did not stop: %v", err)
	}
}
func TestRestartParksInterruptedWork(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	if err := s.Transition(r.ID, "run_started", "running", "Working", "Executing"); err != nil {
		t.Fatal(err)
	}
	apps, _ := LoadApps("")
	e, err := NewEngine(s, apps, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	after, _ := s.Get(r.ID)
	if after.Status != "parked" || after.Decision == nil || after.Decision.Reversibility != "external" {
		t.Fatalf("unsafe restart: %+v", after)
	}
}
func TestSnapshotSkipsItsOwnDataAndRejectsOversize(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	dest := filepath.Join(data, "copy")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyWorkspace(context.Background(), root, dest, data); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recursively copied the data directory")
	}
	f, err := os.Create(filepath.Join(root, "large"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(65 << 20)
	_ = f.Close()
	if err = copyWorkspace(context.Background(), root, t.TempDir(), data); err == nil {
		t.Fatal("oversized workspace accepted")
	}
}
