package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mitos/internal/decisionmodel"
)

func modelConfig(endpoint string, hooks ...string) *decisionmodel.Config {
	return &decisionmodel.Config{Protocol: "systemone", Endpoint: endpoint, Model: "test-model", Hooks: hooks, Instructions: "Select the eligible action, or escalate if uncertain.", TimeoutMS: 2000, MaxCalls: 8, MinConfidence: .7, MinProbability: .8}
}
func modelReply(w http.ResponseWriter, choice string, confidence float64) {
	alternate := "escalate"
	if choice == "escalate" {
		alternate = "continue"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "test-v1", "answers": map[string]any{"action": map[string]any{"type": "choice", "choice": choice, "confidence": confidence, "probabilities": map[string]float64{choice: .95, alternate: .05}}}, "usage": map[string]int{"input_tokens": 20}})
}

func TestModelBoundaryExecutesOnlyValidatedChoice(t *testing.T) {
	for _, mode := range []string{"continue", "escalate", "uncertain", "malformed", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				switch mode {
				case "malformed":
					fmt.Fprint(w, `{"action":"continue"}`)
				case "uncertain":
					modelReply(w, "continue", .1)
				case "timeout":
					io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(time.Second):
					}
				default:
					modelReply(w, mode, .9)
				}
			}))
			defer srv.Close()
			root := t.TempDir()
			app := executionApp(t, `printf done > result.txt`)
			app.DecisionModel = modelConfig(srv.URL, "before_segment")
			if mode == "timeout" {
				app.DecisionModel.TimeoutMS = 30
			}
			e, s := testEngine(t, root, []Application{app})
			r := waitRecovery(t, s, launchApp(t, e, root, app).ID)
			if calls.Load() != 1 || len(r.ModelDecisions) != 1 {
				t.Fatalf("missing receipt: %+v", r.ModelDecisions)
			}
			if mode == "continue" {
				if r.Status != "done" || r.Execution.Runtime != BatchRuntimeVersion || !r.Verified || len(r.Segments) != 1 || r.ModelDecisions[0].Outcome != "accepted" {
					t.Fatalf("did not execute: %+v", r)
				}
			} else if r.Status != "parked" || len(r.Segments) != 0 || r.Decision == nil || r.ModelDecisions[0].Outcome == "accepted" {
				t.Fatalf("unsafe transition: %+v", r)
			}
			if mode != "continue" {
				if r.Claim != nil && r.Claim.ReleasedAt == nil {
					t.Fatal("stopped workflow retained claims")
				}
				for _, o := range r.Decision.Options {
					if o.ID == "rewind" {
						t.Fatal("rewind bypasses decision boundary")
					}
				}
			}
		})
	}
}

func TestModelRecoveryAndCallBudget(t *testing.T) {
	for _, budget := range []int{8, 1} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct {
					State struct {
						Hook string `json:"hook"`
					}
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				action := "continue"
				if body.State.Hook == "before_recovery" {
					action = "rewind"
				}
				modelReply(w, action, .9)
			}))
			defer srv.Close()
			root := t.TempDir()
			app := supervisedApp(t, root)
			app.DecisionModel = modelConfig(srv.URL, "before_segment", "before_recovery")
			app.DecisionModel.MaxCalls = budget
			e, s := testEngine(t, root, []Application{app})
			r := waitRecovery(t, s, launchApp(t, e, root, app).ID)
			if budget == 8 {
				if r.Status != "done" || len(r.Rewinds) != 1 || len(r.Segments) != 2 || calls.Load() != 3 {
					t.Fatalf("recovery failed: %s %s %+v", r.Status, r.Summary, r.ModelDecisions)
				}
			} else if r.Status != "parked" || len(r.Rewinds) != 0 || calls.Load() != 1 || r.Decision.ReasonSignal != "model_call_limit" {
				t.Fatalf("budget bypassed: %+v", r)
			}
		})
	}
}

func TestCancellationFencesModelAnswer(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; modelReply(w, "continue", .9) }))
	defer srv.Close()
	root := t.TempDir()
	app := executionApp(t, `printf done > result.txt`)
	app.DecisionModel = modelConfig(srv.URL, "before_segment")
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("model call not reached")
	}
	if err := e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	r = waitRecovery(t, s, r.ID)
	if r.Status != "cancelled" || len(r.Segments) != 0 || r.ModelDecisions[0].Outcome != "rejected" {
		t.Fatalf("late answer was applied: %+v", r)
	}
}

func TestModelReceiptReplayNeverReissuesPendingCall(t *testing.T) {
	// Start from a parked real execution with a valid frozen plan, then create the
	// crash window: durable request, but no committed response.
	root := t.TempDir()
	app := supervisedApp(t, root)
	app.Supervision.Strategy = "escalate"
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); modelReply(w, "continue", .9) }))
	defer srv.Close()
	app.DecisionModel = modelConfig(srv.URL, "before_segment")
	e, s := testEngine(t, root, []Application{app})
	r := waitRecovery(t, s, launchApp(t, e, root, app).ID)
	e.Close()
	if err := s.Resolve(r.ID, r.Decision.ID, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(r.ID, "test_resume", "running", "Resuming", ""); err != nil {
		t.Fatal(err)
	}
	d, fresh, err := s.beginModelDecision(r.ID, "before_segment", 2, "new-checkpoint", "", "continue")
	if err != nil || !fresh {
		t.Fatal(err)
	}
	dir := s.Dir()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	restored, err := replay.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.ModelDecisions) != 2 || restored.ModelDecisions[1].Request.ID != d.Request.ID {
		t.Fatal("receipt lost")
	}
	engine := &Engine{Store: replay}
	proceed, err := engine.modelBoundary(context.Background(), r.ID, "before_segment", 2, "new-checkpoint", "", "continue")
	if err != nil || proceed || calls.Load() != 1 {
		t.Fatalf("reissued unknown request: %v %v %d", proceed, err, calls.Load())
	}
	restored, _ = replay.Get(r.ID)
	if restored.Status != "parked" || restored.Decision.ReasonSignal != "decision_model_outcome_unknown" {
		t.Fatalf("unknown outcome not visible: %+v", restored.Decision)
	}
}

func TestModelPersistenceBarriers(t *testing.T) {
	for _, phase := range []string{"request", "response"} {
		t.Run(phase, func(t *testing.T) {
			s := testStore(t)
			r := createRun(t, s)
			failure := errors.New("model receipt disk failure")
			var calls atomic.Int32
			failNextCommit := func() { s.mu.Lock(); s.file = &controlledLog{eventLog: s.file, failure: failure}; s.mu.Unlock() }
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				calls.Add(1)
				if phase == "response" {
					failNextCommit()
				}
				modelReply(w, "continue", .9)
			}))
			defer srv.Close()
			app := executionApp(t, "exit 0")
			app.DecisionModel = modelConfig(srv.URL, "before_segment")
			commit := strings.Repeat("a", 40)
			fingerprint := strings.Repeat("b", 64)
			if err := s.RecordWorkspace(r.ID, "workspace_prepared", "", WorkspaceRecord{Mode: "copy", BaselineCommit: commit, Fingerprint: fingerprint}); err != nil {
				t.Fatal(err)
			}
			if err := s.ConfigureExecution(r.ID, ExecutionPlan{Application: app, InitialCommit: commit, InitialFingerprint: fingerprint, Deadline: time.Now().Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			if phase == "request" {
				failNextCommit()
			}
			engine := &Engine{Store: s}
			proceed, err := engine.modelBoundary(context.Background(), r.ID, "before_segment", 1, commit, "", "continue")
			if proceed || !errors.Is(err, failure) {
				t.Fatalf("crossed persistence barrier: %v %v", proceed, err)
			}
			after, _ := s.Get(r.ID)
			expected := int32(0)
			if phase == "response" {
				expected = 1
			}
			if calls.Load() != expected || len(after.ModelDecisions) != int(expected) {
				t.Fatalf("unexpected request/receipt: %d %+v", calls.Load(), after.ModelDecisions)
			}
			if phase == "response" && after.ModelDecisions[0].Outcome != "pending" {
				t.Fatal("uncommitted answer published")
			}
			if _, err := s.StartSegment(r.ID, commit); err == nil {
				t.Fatal("started without durable model approval")
			}
		})
	}
}
