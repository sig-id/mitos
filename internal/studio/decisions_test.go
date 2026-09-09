package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mitos/internal/decision"
)

func TestDecisionBindingAndResolutionAuthority(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	draft := recoveryDecision()
	if err := s.Park(r.ID, draft); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(r.ID)
	d := r.Decision
	if d.RunID != r.ID || d.Tenant != r.Tenant || d.CausedBy <= 0 || d.Context.Attempted != r.Brief.Title || d.Default != nil || d.TTLMS != nil {
		t.Fatalf("unbound card: %+v", d)
	}
	found := false
	for _, e := range r.Events {
		if e.Seq == d.CausedBy && e.Type == "decision" && e.Kind == "request_approval" {
			found = true
		}
	}
	if !found {
		t.Fatal("card did not reference its durable request decision")
	}
	for _, principal := range []decision.Principal{{ID: "anonymous", Tenant: r.Tenant}, {ID: "other", Authenticated: true, Tenant: decision.Tenant{Org: "other", Project: "studio"}}} {
		if err := s.ResolveChoice(r.ID, decision.Resolution{DecisionID: d.ID, Choice: "retry", Via: "api"}, principal, nil); !errors.Is(err, ErrConflict) {
			t.Fatal("unauthorized resolution accepted")
		}
	}
	after, _ := s.Get(r.ID)
	if after.Status != "parked" || after.Decision.Resolution != nil {
		t.Fatal("rejected input changed execution")
	}
	if err := s.ResolveChoice(r.ID, decision.Resolution{DecisionID: d.ID, Choice: "cancel", Via: "cli", Resolver: "forged", ResolvedAt: time.Unix(1, 0), Note: "Keep the evidence"}, decision.LocalOperator(), nil); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Get(r.ID)
	resolution := after.Decision.Resolution
	if resolution.Resolver != "local-operator" || resolution.ResolvedAt.Unix() == 1 || resolution.Via != "cli" || resolution.Note != "Keep the evidence" {
		t.Fatalf("incorrect provenance: %+v", resolution)
	}
	finish := after.FinishedAt
	if err := s.ResolveChoice(r.ID, *resolution, decision.LocalOperator(), nil); err != nil {
		t.Fatal(err)
	}
	final, _ := s.Get(r.ID)
	if final.FinishedAt != finish || final.Events[len(final.Events)-1].Kind != "decision_rejected" {
		t.Fatal("duplicate was applied or not audited")
	}
}
func TestParkRejectsDefaultAndUnsupportedChoiceBeforeWriting(t *testing.T) {
	for _, mutate := range []func(*HumanDecision){func(d *HumanDecision) { choice := "retry"; ttl := int64(1); d.Default = &choice; d.TTLMS = &ttl }, func(d *HumanDecision) { d.Options[0].ID = "publish" }, func(d *HumanDecision) { d.Reversibility = "internal" }, func(d *HumanDecision) { d.Options[0].Consequence = "" }} {
		s := testStore(t)
		r := createRun(t, s)
		d := recoveryDecision()
		mutate(d)
		if s.Park(r.ID, d) == nil {
			t.Fatal("invalid request accepted")
		}
		after, _ := s.Get(r.ID)
		if len(after.Events) != len(r.Events) {
			t.Fatal("invalid request wrote a partial decision")
		}
	}
}
func TestAcceptedRetrySurvivesBothSidesOfChildCreation(t *testing.T) {
	for _, childCreated := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-child", true: "after-child"}[childCreated], func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			r, err := s.Create(testBrief(root), "Inventory", "", false)
			if err != nil {
				t.Fatal(err)
			}
			d := recoveryDecision()
			if err = s.Park(r.ID, d); err != nil {
				t.Fatal(err)
			}
			if err = s.ResolveChoice(r.ID, decision.Resolution{DecisionID: d.ID, Choice: "retry", Via: "api"}, decision.LocalOperator(), nil); err != nil {
				t.Fatal(err)
			}
			childID := ""
			if childCreated {
				child, err := s.Create(r.Brief, r.AppName, r.ID, false)
				if err != nil {
					t.Fatal(err)
				}
				childID = child.ID
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			apps, _ := LoadApps("")
			e, err := NewEngine(s, apps, root)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			parent := waitRun(t, s, r.ID, func(r Run) bool { return r.Decision.AppliedAt != nil })
			children := 0
			for _, candidate := range s.List() {
				if candidate.ParentID == r.ID {
					children++
					if childID != "" && candidate.ID != childID {
						t.Fatal("recorded retry created a second child")
					}
					if parent.Decision.ResultRunID != candidate.ID {
						t.Fatal("resolution result is not bound to child")
					}
				}
			}
			if children != 1 {
				t.Fatalf("retry has %d children", children)
			}
			result, err := e.ResolveAs(r.ID, d.ID, "retry", "", "api", decision.LocalOperator())
			if err != nil || result != parent.Decision.ResultRunID {
				t.Fatalf("duplicate response changed: %s %v", result, err)
			}
		})
	}
}
func TestNewAndLegacyCardPayloadsReplayTogether(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	old := json.RawMessage(`{"status":"parked","decision":{"decision_id":"legacy","question":"Restart?","context":"old context","cost_of_wrong":"old consequence","reversibility":"external","options":[{"id":"retry","label":"Retry","consequence":"Repeat"},{"id":"cancel","label":"Close","consequence":"Stop"}]}}`)
	var c change
	if err := json.Unmarshal(old, &c); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err := s.commit(r.ID, "effect", "approval_requested", c)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Resolve(r.ID, "legacy", "cancel"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Get(r.ID)
	if after.Decision.Context.BlockedBy != "old context" || after.Decision.Resolution == nil {
		t.Fatal("legacy decision lost")
	}
}

func TestRequestCrashReconstructsCardBeforeAnyExecution(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	r, err := s.Create(testBrief(root), "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Park(r.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.Get(r.ID)
	s.Close()
	logPath := filepath.Join(dir, "events.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	prefix := bytes.Join(lines[:len(lines)-2], nil)
	if err = os.WriteFile(logPath, prefix, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, _ := s.Get(r.ID)
	if before.PendingDecision == nil || before.Status != "parked" {
		t.Fatal("request did not durably stop execution")
	}
	apps, _ := LoadApps("")
	engine, err := NewEngine(s, apps, root)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	after, _ := s.Get(r.ID)
	if after.PendingDecision != nil || after.Decision == nil || after.Decision.ID != saved.Decision.ID || after.Decision.CausedBy != saved.Decision.CausedBy || after.Status != "parked" {
		t.Fatal("pending card was not reconstructed")
	}
	count := 0
	for _, event := range after.Events {
		if event.Kind == "approval_requested" {
			count++
		}
		if event.Kind == "capability_started" {
			t.Fatal("pending decision executed work")
		}
	}
	if count != 1 {
		t.Fatalf("card delivered %d times", count)
	}
}

func TestOperatorStopSupersedesUnappliedRetry(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	r, err := s.Create(testBrief(root), "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	d := recoveryDecision()
	if err = s.Park(r.ID, d); err != nil {
		t.Fatal(err)
	}
	if err = s.Resolve(r.ID, d.ID, "retry"); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s}
	// Apply the operator stop before the dispatch reconciliation pass.
	if err = e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	e.reconcileDecisions()
	after, _ := s.Get(r.ID)
	if after.Status != "cancelled" || after.Decision.AppliedAt == nil || after.Decision.ApplicationError == "" {
		t.Fatal("pending retry ignored the operator stop")
	}
	for _, candidate := range s.List() {
		if candidate.ParentID == r.ID {
			t.Fatal("cancelled retry created a child")
		}
	}
}

func TestLegacyResolvedChoiceReturnsWithoutInventingAuthority(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	d := recoveryDecision()
	if err := s.Park(r.ID, d); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err := s.commit(r.ID, "effect", "decision_resolved", change{Choice: "retry", Status: "cancelled"})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s}
	result, err := e.Resolve(r.ID, d.ID, "retry")
	if err != nil || result != r.ID {
		t.Fatalf("legacy result lost: %s %v", result, err)
	}
	after, _ := s.Get(r.ID)
	if after.Decision.Resolution != nil || len(s.List()) != 1 {
		t.Fatal("historical duplicate invented a resolver or a new attempt")
	}
}
