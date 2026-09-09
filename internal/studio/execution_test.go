package studio

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/compute"
	workspacefs "mitos/internal/workspace"
)

func executionApp(t *testing.T, command string) Application {
	t.Helper()
	return Application{ID: "change-app", Name: "Change application", Entrypoint: "Run", Directory: t.TempDir(), Command: []string{"/bin/sh", "-c", command}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "test -s \"$MITOS_WORKSPACE/result.txt\" && printf accepted"}}}
}
func launchApp(t *testing.T, e *Engine, root string, app Application) Run {
	t.Helper()
	brief := testBrief(root)
	brief.AppID = app.ID
	r, err := e.Launch(brief, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func readChanges(t *testing.T, s *Store, r Run) workspacefs.Harvest {
	t.Helper()
	data, err := s.Artifact(r.ID, "changes.json")
	if err != nil {
		t.Fatal(err)
	}
	var result workspacefs.Harvest
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestExecutionHarvestsUsablePatchAndVerifiesReconstructedWorkspace(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf result > result.txt; printf '%s' "$PWD" > execution-location.txt`)
	app.Acceptance.Cmd = []string{"/bin/sh", "-c", `test "$(cat "$MITOS_WORKSPACE/execution-location.txt")" != "$MITOS_WORKSPACE" && test -s "$MITOS_WORKSPACE/result.txt" && printf accepted`}
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "done" || !r.Verified || r.Workspace == nil || r.Acceptance == nil || r.Acceptance.Verdict != "pass" || r.Acceptance.WorkspaceCommit != r.Workspace.ResultCommit {
		t.Fatalf("unverified change: %+v", r)
	}
	changes := readChanges(t, s, r)
	if len(changes.Changes) != 2 || changes.BaselineCommit == changes.ResultCommit {
		t.Fatalf("incomplete changed paths: %+v", changes)
	}
	patch, err := s.Artifact(r.ID, "changes.patch")
	if err != nil || !strings.Contains(string(patch), "result.txt") {
		t.Fatalf("missing usable patch: %s %v", patch, err)
	}
	if _, err = os.Stat(filepath.Join(root, "result.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("source changed")
	}
}

func TestExecutionPreservesFailedAndCancelledChanges(t *testing.T) {
	for _, stop := range []string{"failed", "cancelled"} {
		t.Run(stop, func(t *testing.T) {
			root := t.TempDir()
			command := `printf partial > result.txt; printf partial-output; exit 7`
			if stop == "cancelled" {
				command = `printf partial > result.txt; printf partial-output; sleep 30 & wait`
			}
			app := executionApp(t, command)
			e, s := testEngine(t, root, []Application{app})
			r := launchApp(t, e, root, app)
			if stop == "cancelled" {
				path := filepath.Join(s.Dir(), "workspaces", r.ID, "workspace", "result.txt")
				deadline := time.Now().Add(5 * time.Second)
				for {
					if _, err := os.Stat(path); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("command did not write result")
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err := e.Cancel(r.ID); err != nil {
					t.Fatal(err)
				}
				if err := e.Cancel(r.ID); err != nil {
					t.Fatal("duplicate cancel was not idempotent", err)
				}
			}
			r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
			if r.Status != stop || r.Verified {
				t.Fatalf("wrong stopped state: %+v", r)
			}
			output, err := s.Artifact(r.ID, "output.txt")
			if err != nil || string(output) != "partial-output" {
				t.Fatalf("partial output lost: %s %v", output, err)
			}
			changes := readChanges(t, s, r)
			if len(changes.Changes) != 1 || changes.Changes[0].Path != "result.txt" {
				t.Fatalf("partial change lost: %+v", changes)
			}
			if _, err = s.Artifact(r.ID, "workspace.tar.gz"); err != nil {
				t.Fatal("partial archive missing", err)
			}
			if _, err = s.Artifact(r.ID, "acceptance.txt"); !errors.Is(err, ErrNotFound) {
				t.Fatal("acceptance ran after stop")
			}
		})
	}
}

func TestExecutionHarvestsAfterDeadline(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf partial > result.txt; printf output; sleep 30 & wait`)
	s := testStore(t)
	r, err := s.Create(Brief{Title: "Timed-out work", Workspace: root, AppID: app.ID, TimeoutMinutes: 1}, app.Name, "", false)
	if err != nil {
		t.Fatal(err)
	}
	// Use the same execution lifecycle with a short deadline, without a dispatcher
	// racing the manually driven run.
	engineCtx, stop := context.WithCancel(context.Background())
	defer stop()
	e := &Engine{provider: &compute.Local{}, Store: s, Apps: []Application{app}, Root: root, ctx: engineCtx}
	ctx, cancel := context.WithTimeout(engineCtx, 2*time.Second)
	defer cancel()
	e.execute(ctx, r)
	finished, _ := s.Get(r.ID)
	if finished.Status != "failed" || !strings.Contains(finished.Summary, "time limit") {
		t.Fatalf("deadline failed: %+v", finished)
	}
	if changes := readChanges(t, s, finished); len(changes.Changes) != 1 {
		t.Fatal("deadline discarded partial change")
	}
}

func TestOracleEditsAreDurableJudgmentDecisions(t *testing.T) {
	for _, command := range []string{`printf changed > tests/oracle.txt; printf result > result.txt`, `rm tests/oracle.txt; printf result > result.txt`, `printf 'tests/\n' > .gitignore; printf changed > tests/oracle.txt; printf result > result.txt`} {
		t.Run(command, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "tests"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "tests", "oracle.txt"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			app := executionApp(t, command)
			app.Acceptance.OraclePaths = []string{"tests"}
			e, s := testEngine(t, root, []Application{app})
			r := launchApp(t, e, root, app)
			r = waitRun(t, s, r.ID, func(r Run) bool { return r.Status == "parked" || r.Terminal() })
			if r.Status != "parked" || r.Verified || r.Decision == nil || r.Decision.ReasonSignal != "oracle_tampered" || r.Decision.InterruptClass != "judgment" {
				t.Fatalf("oracle edit accepted: %+v", r)
			}
			if _, err := s.Artifact(r.ID, "acceptance.txt"); !errors.Is(err, ErrNotFound) {
				t.Fatal("oracle edit reached acceptance")
			}
			changes := readChanges(t, s, r)
			if len(changes.Changes) < 2 {
				t.Fatal("oracle evidence missing")
			}
			recorded := false
			for _, event := range r.Events {
				if event.Kind == "oracle_tampered" {
					recorded = true
				}
			}
			if !recorded {
				t.Fatal("oracle edit was not on the log")
			}
		})
	}
}

func TestAcceptanceUsesPristineInstalledFilesAndMeasuresFailedPredicate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "score"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	app := executionApp(t, `printf 3 > score; printf result > result.txt`)
	verifier := filepath.Join(app.Directory, "verify.sh")
	if err := os.WriteFile(verifier, []byte("#!/bin/sh\nprintf rejected\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app.Acceptance = acceptance.Spec{Cmd: []string{verifier}, Measure: []string{"/bin/sh", "-c", `cat "$MITOS_WORKSPACE/score"`}}
	// Mutating the installed copy must not change the verifier saved before run.
	app.Command = []string{"/bin/sh", "-c", app.Command[2] + `; printf '#!/bin/sh\nexit 0\n' > "$1"`, "worker", verifier}
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "failed" || r.Verified || r.Acceptance == nil || r.Acceptance.Verdict != "fail" || len(r.Acceptance.Delta) != 1 || r.Acceptance.Delta[0] != 2 {
		t.Fatalf("wrong predicate evidence: %+v", r)
	}
	output, err := s.Artifact(r.ID, "acceptance.txt")
	if err != nil || string(output) != "rejected" {
		t.Fatalf("installed verifier was not frozen: %s %v", output, err)
	}
}

func TestAcceptanceEvidenceCannotTargetAnotherCommitAndSurvivesReplay(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf result > result.txt`)
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "done" {
		t.Fatalf("setup failed: %+v", r)
	}
	pending, err := s.Create(testBrief(root), "Pending evidence", "", true)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(pending.ID)
	if err = s.RecordAcceptance(pending.ID, acceptance.Verdict{Verdict: "pass", WorkspaceCommit: r.Workspace.ResultCommit, EvidenceRef: "acceptance.txt"}); err == nil {
		t.Fatal("acceptance for an unharvested run was accepted")
	}
	if err = s.RecordWorkspace(pending.ID, "workspace_prepared", "Prepared", *r.Workspace); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAcceptance(pending.ID, *r.Acceptance); err == nil {
		t.Fatal("acceptance with missing evidence was accepted")
	}
	record := *r.Workspace
	record.BaselineCommit = strings.Repeat("0", 40)
	if err = s.RecordWorkspace(pending.ID, "workspace_harvested", "Changed baseline", record); err == nil {
		t.Fatal("harvest changed the baseline")
	}
	after, _ := s.Get(pending.ID)
	if len(after.Events) != len(before.Events)+1 {
		t.Fatal("rejected verdicts changed the log")
	}
	// Stop the engine before reopening the canonical log; folded verification
	// state and digested artifacts must not require a live runner.
	e.Close()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, err := reopened.Get(r.ID)
	if err != nil || replayed.Acceptance == nil || replayed.Workspace == nil || replayed.Acceptance.WorkspaceCommit != replayed.Workspace.ResultCommit || !replayed.Verified {
		t.Fatalf("predicate lost on replay: %+v %v", replayed, err)
	}
	if _, err = reopened.Artifact(r.ID, "changes.patch"); err != nil {
		t.Fatal(err)
	}
}
