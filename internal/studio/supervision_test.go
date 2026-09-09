package studio

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/autonomy"
	workspacefs "mitos/internal/workspace"
)

func limit(value int) *int { return &value }
func supervisedApp(t *testing.T, root string) Application {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "health"), []byte("healthy"), 0600); err != nil {
		t.Fatal(err)
	}
	app := executionApp(t, `request=$(cat); case "$request" in *'"number":1'*) printf broken > health; printf 'first failure'; exit 7;; *) test "$(cat health)" = healthy || exit 8; case "$request" in *'"failure_memo":""'*) exit 9;; esac; printf recovered > result.txt; printf 'second success';; esac`)
	app.Supervision = &autonomy.Supervision{Strategy: "rewind_to_green", MaxRestarts: limit(2)}
	app.Acceptance = acceptance.Spec{Cmd: []string{"/bin/sh", "-c", `test "$(cat "$MITOS_WORKSPACE/health")" = healthy`}}
	return app
}
func waitRecovery(t *testing.T, s *Store, id string) Run {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Terminal() || r.Status == "parked" {
			return r
		}
		time.Sleep(20 * time.Millisecond)
	}
	r, _ := s.Get(id)
	t.Fatalf("recovery did not finish: status=%s stage=%s summary=%s segments=%d", r.Status, r.Stage, r.Summary, len(r.Segments))
	return r
}

func TestSupervisionRestoresGreenAndCarriesFailureMemo(t *testing.T) {
	root := t.TempDir()
	app := supervisedApp(t, root)
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	r = waitRecovery(t, s, r.ID)
	if r.Status != "done" || !r.Verified || len(r.Segments) != 2 || len(r.Rewinds) != 1 || len(r.Checkpoints) != 3 {
		t.Fatalf("recovery failed: status=%s summary=%s segments=%d rewinds=%d checkpoints=%d", r.Status, r.Summary, len(r.Segments), len(r.Rewinds), len(r.Checkpoints))
	}
	if !r.Checkpoints[0].Green || !r.Checkpoints[1].Invalidated || !r.Checkpoints[2].Green || r.Rewinds[0].Memo == "" || r.Rewinds[0].AppliedAt == nil {
		t.Fatal("incorrect checkpoint recovery fold")
	}
	failed, err := s.Artifact(r.ID, "output.txt")
	if err != nil || string(failed) != "first failure" {
		t.Fatalf("abandoned evidence lost: %s %v", failed, err)
	}
	success, err := s.Artifact(r.ID, "segment-002-output.txt")
	if err != nil || string(success) != "second success" {
		t.Fatalf("retry evidence lost: %s %v", success, err)
	}
	old, err := os.ReadFile(filepath.Join(s.Dir(), "workspaces", r.ID, "workspace", "health"))
	if err != nil || string(old) != "broken" {
		t.Fatal("rewind rewrote the abandoned workspace")
	}
	for _, checkpoint := range r.Checkpoints {
		if checkpoint.Seq <= checkpoint.QuietSeq || checkpoint.SandboxSnapshot != "unsupported" {
			t.Fatal("checkpoint lacks a quiescence prefix")
		}
	}
}
func TestSupervisionExhaustionAndMissingGreenPark(t *testing.T) {
	for _, green := range []bool{true, false} {
		t.Run(map[bool]string{true: "exhausted", false: "no_green"}[green], func(t *testing.T) {
			root := t.TempDir()
			app := supervisedApp(t, root)
			app.Command = []string{"/bin/sh", "-c", `printf broken > health; exit 3`}
			if !green {
				app.Acceptance.Cmd = []string{"/bin/sh", "-c", "exit 1"}
			}
			e, s := testEngine(t, root, []Application{app})
			r := launchApp(t, e, root, app)
			r = waitRecovery(t, s, r.ID)
			reason := "restart_intensity_exhausted"
			attempts := 3
			if !green {
				reason = "no_green_checkpoint"
				attempts = 1
			}
			if r.Status != "parked" || r.Decision == nil || r.Decision.ReasonSignal != reason || len(r.Segments) != attempts {
				t.Fatalf("bad bound: %s %s segments=%d decision=%+v", r.Status, r.Summary, len(r.Segments), r.Decision)
			}
			for _, option := range r.Decision.Options {
				if option.ID == "rewind" {
					t.Fatal("card offered a recovery beyond its bounds")
				}
			}
		})
	}
}
func TestRepeatedPredicateStallEscalatesDespiteBusyDiff(t *testing.T) {
	root := t.TempDir()
	app := supervisedApp(t, root)
	app.Command = []string{"/bin/sh", "-c", `printf broken > health; printf editing > result.txt`}
	app.Supervision.MaxRestarts = limit(8)
	app.Acceptance.Measure = []string{"/bin/sh", "-c", "printf 7"}
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	r = waitRecovery(t, s, r.ID)
	if r.Status != "parked" || r.Decision == nil || r.Decision.ReasonSignal != "predicate_stall_repeated" || len(r.Segments) != 4 {
		for _, event := range r.Events {
			if event.Kind == "initial_predicate_failed" || event.Kind == "predicate_evaluated" {
				t.Log(string(event.Data))
			}
		}
		t.Fatalf("stall was hidden by edits: %s %s segments=%d", r.Status, r.Summary, len(r.Segments))
	}
	count := 0
	for _, rewind := range r.Rewinds {
		if rewind.ReasonSignal == "predicate_stall" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("predicate stall did not get exactly one recovery attempt")
	}
}
func TestPendingRewindRedrivesAfterRestartWithoutRepeatingFailedCommand(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_restore", true: "after_restore_before_effect"}[published], func(t *testing.T) {
			root := t.TempDir()
			app := supervisedApp(t, root)
			app.Supervision.Strategy = "escalate"
			e, s := testEngine(t, root, []Application{app})
			r := launchApp(t, e, root, app)
			r = waitRecovery(t, s, r.ID)
			if r.Status != "parked" {
				t.Fatalf("setup: %s %s", r.Status, r.Summary)
			}
			e.Close()
			request := autonomy.Rewind{CheckpointID: r.Checkpoints[0].ID, Scope: "step", FromSegment: 1, Memo: "Restore health before trying again; the first segment failed.", ReasonSignal: "operator_recovery"}
			rewind, err := s.BeginRewind(r.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := s.BeginRewind(r.ID, request)
			if err != nil || duplicate.ID != rewind.ID {
				t.Fatal("duplicate pending rewind was not idempotent")
			}
			if published {
				base := filepath.Join(s.Dir(), "workspaces", r.ID)
				history := &workspacefs.History{Base: base, Directory: filepath.Join(base, "history.git"), BaselineCommit: r.Execution.InitialCommit}
				c := r.Checkpoints[0]
				if err = history.Restore(context.Background(), c.WorkspaceCommit, c.Fingerprint, segmentBase(base, 2)); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(s.Dir())
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			queued, err := reopened.Get(r.ID)
			if err != nil || queued.PendingRewind == nil || queued.PendingRewind.ID != rewind.ID {
				t.Fatal("pending decision lost on replay")
			}
			restarted, err := NewEngine(reopened, []Application{app}, root)
			if err != nil {
				t.Fatal(err)
			}
			defer restarted.Close()
			result := waitRecovery(t, reopened, r.ID)
			if result.Status != "done" || len(result.Segments) != 2 || len(result.Rewinds) != 1 || result.PendingRewind != nil || result.ReadyRewind != nil {
				t.Fatalf("rewind re-drive failed: %s %s segments=%d", result.Status, result.Summary, len(result.Segments))
			}
			restored := 0
			for _, event := range result.Events {
				if event.Kind == "checkpoint_restored" {
					restored++
				}
			}
			if restored != 1 || !result.Execution.Deadline.Equal(r.Execution.Deadline) {
				t.Fatal("recovery duplicated an effect or refreshed the time budget")
			}
			report := ReportInterrupts(reopened.List())
			if report.HumanTouches != 1 {
				t.Fatalf("operator rewind missing from interruption report: %+v", report)
			}
			if !strings.Contains(result.Rewinds[0].Memo, "first segment") {
				t.Fatal("failure memo lost on replay")
			}
		})
	}
}

func TestOperatorRewindValidatesMemoStalenessAndNonGreenOverride(t *testing.T) {
	root := t.TempDir()
	app := supervisedApp(t, root)
	app.Supervision.Strategy = "escalate"
	app.Command = []string{"/bin/sh", "-c", `request=$(cat); case "$request" in *'"number":1'*) printf broken > health; exit 1;; *) test "$(cat health)" = broken || exit 8; printf healthy > health; printf repaired > result.txt;; esac`}
	app.Acceptance.Measure = []string{"/bin/sh", "-c", `if test "$(cat "$MITOS_WORKSPACE/health")" = healthy; then printf 1; else printf 0; fi`}
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	r = waitRecovery(t, s, r.ID)
	if r.Status != "parked" || len(r.Checkpoints) != 2 {
		t.Fatalf("setup failed: %s %s", r.Status, r.Summary)
	}
	request := autonomy.Rewind{CheckpointID: r.Checkpoints[1].ID, Scope: "step", FromSegment: 1, ReasonSignal: "operator_recovery"}
	if _, err := e.Rewind(r.ID, request); err == nil {
		t.Fatal("memo-free rewind accepted")
	}
	request.Memo = "The health file is broken; explicitly restore this failed checkpoint and repair that file."
	if _, err := e.Rewind(r.ID, request); err == nil {
		t.Fatal("non-green checkpoint accepted without an override")
	}
	request.AllowNonGreen = true
	request.FromSegment = 0
	if _, err := e.Rewind(r.ID, request); err == nil {
		t.Fatal("stale segment request accepted")
	}
	request.FromSegment = 1
	if _, err := e.Rewind(r.ID, request); err != nil {
		t.Fatal(err)
	}
	r = waitRecovery(t, s, r.ID)
	if r.Status != "done" || r.Acceptance == nil || len(r.Acceptance.Delta) != 1 || r.Acceptance.Delta[0] != 1 {
		t.Fatalf("measurement did not use restored input: %s %s acceptance=%+v", r.Status, r.Summary, r.Acceptance)
	}
	if r.Segments[1].InputCommit != r.Checkpoints[1].WorkspaceCommit || !r.Rewinds[0].AllowNonGreen {
		t.Fatal("explicit non-green recovery was not recorded")
	}
	if _, err := e.Rewind(r.ID, request); err == nil {
		t.Fatal("terminal run reopened")
	}
}
