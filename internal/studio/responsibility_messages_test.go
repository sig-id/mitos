package studio

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mitos/internal/desk"
	"mitos/internal/watch"
)

func TestDirectMessageWakesGraphAndReachesPlanner(t *testing.T) {
	e, g, planner, _ := ownerGraph(t)
	s := e.Store
	const body = "Review the latest results before planning."
	if _, err := e.MessageResponsibility("another-project", g.ID, body); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-project post", err)
	}
	d, err := e.MessageResponsibility(g.ProjectID, g.ID, body)
	if err != nil || d.Route != routeInbox || d.Generation != g.OrchestratorGeneration || d.Trigger != d.FirstSeq || d.Trigger == 0 {
		t.Fatal("missing direct wake", d, err)
	}
	in := s.WatchInbox(responsibility(g))
	r, _ := s.Get(planner)
	if len(in.Pending) != 1 {
		t.Fatal(in)
	}
	m, err := s.deliveryObservation(in.Pending[0], &r)
	if err != nil || !strings.Contains(m.Body, body) {
		t.Fatal("missing message in watch_inbox", m, err)
	}
	after, _ := s.Graph(g.ID)
	cancelOrchestrator(t, e, after)
	if err := e.reconcileGraphOrchestrators(); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Graph(g.ID)
	x := after.Expansions[fmt.Sprintf("event:%d", d.Trigger)]
	if x == nil || x.OrchestratorTask == "" || !strings.Contains(after.Members[x.OrchestratorTask].Spec.Context, body) {
		t.Fatal("message did not reach the next planning round", x)
	}
}

func TestDirectMessageOverflowAndReplay(t *testing.T) {
	e, s := testEngine(t, t.TempDir(), nil)
	p, err := e.Desk.CreateProject("Inbox", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	task, err := e.Desk.CreateWork(p.ID, desk.NewWork{Key: "work", Role: "orchestrator", Title: "Review messages", Intent: "Review each message", Criteria: "Messages reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	o := watch.Owner{Kind: watch.OwnerResponsibility, ID: task.ResponsibilityID}
	for i := 0; i < 257; i++ {
		if _, err := e.MessageResponsibility(p.ID, o.ID, fmt.Sprintf("message %d", i)); err != nil {
			t.Fatal(i, err)
		}
	}
	e.Close()
	dir := s.Dir()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	in := s.WatchInbox(o)
	if len(in.Pending) != ownerInboxHardCapacity || in.Folded.Count != 257-ownerInboxHardCapacity {
		t.Fatal("unbounded inbox or lost fold", len(in.Pending), in.Folded)
	}
	page, err := s.ResponsibilityMessages(p.ID, o.ID, 0)
	if err != nil || len(page) != 50 || page[0].Message != "message 256" || page[0].Route != routeFolded {
		t.Fatal("folded message missing", err)
	}
	older, err := s.ResponsibilityMessages(p.ID, o.ID, page[49].FirstSeq)
	if err != nil || len(older) != 50 || older[0].Message != "message 206" {
		t.Fatal("incorrect history page", err)
	}
	for _, item := range in.Pending {
		if _, err := s.RecordDeliveryOutcome(o, in.Generation, item.DeliveryID, "acted"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EvaluateWatches(time.Now()); err != nil {
		t.Fatal(err)
	}
	in = s.WatchInbox(o)
	if len(in.Pending) != 1 || in.Pending[0].Delivery.Kind != "digest" || in.Pending[0].Absorbed.Count != 257-ownerInboxHardCapacity || in.Folded.Count != 0 {
		t.Fatal("direct-message digest did not flush", in)
	}
}
