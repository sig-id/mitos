package studio

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Issue #137 part 2: a work item whose own deadline has passed must fail
// amendments with a distinct, actionable error instead of the generic
// conflict message that sends operators chasing phantom concurrent
// modifications, and the expiry must be journalled once instead of silently
// stalling the graph.

func TestAmendGraphExpiredDeadlineIsDistinctError(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Limits.Deadline = time.Now().UTC().Add(400 * time.Millisecond)
	g := createGraphTest(t, e, req)
	time.Sleep(500 * time.Millisecond)
	spec := graphWorker("inspect", g.RootTask)
	p := proposal(g, "late", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "Arrived after the deadline"})
	_, _, err := e.AmendGraph(p)
	if !errors.Is(err, ErrWorkExpired) || !strings.Contains(err.Error(), "resume") {
		t.Fatalf("expired work reported the wrong cause: %v", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Fatalf("expiry must not be reported as a conflict: %v", err)
	}
	// Idempotent replay of the same proposal reports the same cause.
	if _, _, err = e.AmendGraph(p); !errors.Is(err, ErrWorkExpired) {
		t.Fatalf("replayed amendment changed cause: %v", err)
	}
}

func TestAmendGraphConflictStaysDistinctFromExpiry(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	spec := graphWorker("inspect", g.RootTask)
	p := proposal(g, "stale", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "Stale revision"})
	p.Revision = g.Revision + 3
	if _, _, err := e.AmendGraph(p); !errors.Is(err, ErrConflict) || errors.Is(err, ErrWorkExpired) {
		t.Fatalf("stale revision misclassified: %v", err)
	}
}

func TestReconcileGraphsAnnouncesExpiredWorkOnce(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Limits.Deadline = time.Now().UTC().Add(600 * time.Millisecond)
	g := createGraphTest(t, e, req)
	spec := graphWorker("inspect", g.RootTask)
	amended, _ := amendGraphTest(t, e, g, "work", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "One pending worker"})
	time.Sleep(700 * time.Millisecond)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	expired, err := e.Store.Graph(amended.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.ExpiredNotice.IsZero() {
		t.Fatal("deadline expiry was not recorded on the work item")
	}
	// The announcement is durable: replaying the journal sees the effect
	// exactly once, with the notice on the graph change.
	count := 0
	var noticed bool
	if err = e.Store.journal.Replay(func(ev Event) error {
		if ev.Kind == "work_deadline_passed" {
			count++
			var c change
			if json.Unmarshal(ev.Data, &c) == nil && c.Graph != nil && !c.Graph.Graph.ExpiredNotice.IsZero() {
				noticed = true
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !noticed {
		t.Fatalf("expiry announcement not journalled once with the notice: count=%d noticed=%v", count, noticed)
	}
	// Reconciling again must not announce twice.
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	count = 0
	if err = e.Store.journal.Replay(func(ev Event) error {
		if ev.Kind == "work_deadline_passed" {
			count++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expiry announced more than once: %d", count)
	}
	// Amendments against the announced graph still name the real cause.
	if _, _, err = e.AmendGraph(proposal(expired, "after", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "After announcement"})); !errors.Is(err, ErrWorkExpired) {
		t.Fatalf("post-announcement amendment lost the expiry cause: %v", err)
	}
}
