package studio

import (
	"context"
	"encoding/json"
	"errors"
	"mitos/internal/acceptance"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mitos/internal/observation"
)

func TestObservedCommandStallsAndPreservesOutput(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	monitor, err := newCommandObservation(s, r.ID, observation.Config{ThinkTimeP95MS: 1})
	if err != nil {
		t.Fatal(err)
	}
	monitor.thresholds = observation.Thresholds{Snap: 10 * time.Millisecond, Steer: 20 * time.Millisecond, Block: 100 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := executeObservedCommand(ctx, []string{"/bin/sh", "-c", "printf 'waiting'; sleep 30 & wait"}, workspace, base, nil, monitor)
	if !errors.Is(err, errBlockedOnHuman) || string(output) != "waiting" {
		t.Fatalf("stall not stopped with output: %s %v", output, err)
	}
	got, _ := s.Get(r.ID)
	found := false
	for _, event := range got.Events {
		if event.Kind == "blocked_on_human" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing durable stall evidence")
	}
	decision := observationDecision(err)
	if err = s.Park(r.ID, decision); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(r.ID)
	if got.Status != "parked" || got.Decision.ReasonSignal != "blocked_on_human" || got.Decision.Reversibility != "external" {
		t.Fatalf("unsafe stall decision: %+v", got)
	}
}

func TestStructuredSessionDoesNotGrantAcceptance(t *testing.T) {
	root := t.TempDir()
	app := Application{ID: "observed", Name: "Observed", Entrypoint: "Run", Directory: t.TempDir(),
		Command:     []string{"/bin/sh", "-c", `printf '{"type":"status","status":"working"}\n{"type":"final_result","text":"all tests pass"}\n' > "$HOME/session.jsonl"`},
		Acceptance:  acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "exit 1"}},
		Observation: &observation.Config{ThinkTimeP95MS: 1000, Session: &observation.SessionSpec{Path: "session.jsonl", Format: "mitos-jsonl", SchemaVersion: "1", Tail: true}}}
	e, s := testEngine(t, root, []Application{app})
	brief := testBrief(root)
	brief.AppID = app.ID
	r, err := e.Launch(brief, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "failed" || r.Verified {
		t.Fatal("harness self-report authorized acceptance")
	}
	count := 0
	for _, event := range r.Events {
		if event.Kind == "harness_session_recorded" {
			count++
			var c change
			if err = json.Unmarshal(event.Data, &c); err != nil || len(c.Evidence) == 0 {
				t.Fatal("missing session evidence")
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected two complete session records, got %d", count)
	}
}

func TestMissingSessionParksBeforeAcceptanceAndReplays(t *testing.T) {
	root := t.TempDir()
	app := Application{ID: "missing-session", Name: "Missing session", Entrypoint: "Run", Directory: t.TempDir(), Command: []string{"/bin/sh", "-c", "printf 'work completed'"}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "exit 0"}}, Observation: &observation.Config{ThinkTimeP95MS: 1000, Session: &observation.SessionSpec{Path: "session.jsonl", Format: "mitos-jsonl", SchemaVersion: "1", Tail: true}}}
	e, s := testEngine(t, root, []Application{app})
	brief := testBrief(root)
	brief.AppID = app.ID
	r, err := e.Launch(brief, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, s, r.ID, func(r Run) bool { return r.Status == "parked" })
	if r.Verified || r.Decision.ReasonSignal != "observation_contract_failed" {
		t.Fatalf("contract failure ignored: %+v", r)
	}
	for _, event := range r.Events {
		if event.Kind == "verification_started" {
			t.Fatal("acceptance ran after observation failure")
		}
	}
	output, err := s.Artifact(r.ID, "output.txt")
	if err != nil || string(output) != "work completed" {
		t.Fatalf("lost output: %s %v", output, err)
	}
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
	if err != nil || replayed.Status != "parked" || replayed.Decision.ID != r.Decision.ID {
		t.Fatalf("park lost on replay: %+v %v", replayed, err)
	}
}
