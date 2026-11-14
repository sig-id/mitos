package studio

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestGraphWorkStatePersistsAndCancelsWithoutRestarting(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	// Administrative closure remains possible after the execution deadline.
	e.Store.graphs[g.ID].Limits.Deadline = time.Now().Add(-time.Minute)
	edit := GraphWorkStateEdit{Key: "archive", Revision: g.Revision, State: "archived"}
	archived, err := e.Store.SetGraphWorkState(g.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if archived.WorkStateValue() != "archived" || archived.Status != "cancelled" {
		t.Fatalf("archive: %+v", archived)
	}
	for _, m := range archived.Members {
		if !m.CancelRequested {
			t.Fatal("archive left dispatchable work")
		}
	}
	replay, err := e.Store.SetGraphWorkState(g.ID, edit)
	if err != nil || replay.Revision != archived.Revision {
		t.Fatal("retry changed state", err)
	}
	edit.State = "closed"
	if _, err = e.Store.SetGraphWorkState(g.ID, edit); !errors.Is(err, ErrConflict) {
		t.Fatal("reused key accepted", err)
	}
	edit.Key = "stale"
	if _, err = e.Store.SetGraphWorkState(g.ID, edit); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision accepted", err)
	}
	reopened, err := e.Store.SetGraphWorkState(g.ID, GraphWorkStateEdit{Key: "reopen", Revision: archived.Revision, State: "open"})
	if err != nil || reopened.Status != "cancelled" || reopened.WorkStateValue() != "open" {
		t.Fatal("reopen restarted execution", err)
	}
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	if len(e.Store.List()) != 0 {
		t.Fatal("reopening dispatched an attempt")
	}
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	saved, err := OpenStore(e.Store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	restored, err := saved.Graph(g.ID)
	if err != nil || !reflect.DeepEqual(reopened, restored) {
		t.Fatal("state not replayed", err)
	}
}

func TestGraphWorkStateFilteringBeforePagination(t *testing.T) {
	e, req := graphEngine(t, false)
	for i, state := range []string{"archived", "open", "closed", "open"} {
		req.Key = state + string(rune('a'+i))
		g := createGraphTest(t, e, req)
		if state != "open" {
			if _, err := e.Store.SetGraphWorkState(g.ID, GraphWorkStateEdit{Key: req.Key, Revision: g.Revision, State: state}); err != nil {
				t.Fatal(err)
			}
		}
	}
	first, next := e.Store.GraphSummaries(req.ProjectID, "", 1, "open")
	if len(first) != 1 || next == "" || first[0].WorkState != "open" {
		t.Fatal("first open page", first, next)
	}
	second, next := e.Store.GraphSummaries(req.ProjectID, next, 1, "open")
	if len(second) != 1 || next != "" || second[0].ID == first[0].ID {
		t.Fatal("second open page", second, next)
	}
	wrong, _ := e.Store.GraphSummaries("other-project", "", 100, "archived")
	if len(wrong) != 0 {
		t.Fatal("cross-project archive")
	}
	all, _ := e.Store.GraphSummaries(req.ProjectID, "", 100)
	if len(all) != 4 {
		t.Fatal("legacy discovery lost work")
	}
	for _, status := range []string{"accepted", "cancelled"} {
		if (WorkGraph{Status: status}).WorkStateValue() != "closed" {
			t.Fatal("legacy completed work remains open")
		}
	}
}

func TestGraphWorkStatePersistenceFailurePublishesNothing(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	e.Store.file = &controlledLog{eventLog: e.Store.file, failure: errors.New("sync failed")}
	if _, err := e.Store.SetGraphWorkState(g.ID, GraphWorkStateEdit{Key: "archive", Revision: g.Revision, State: "archived"}); err == nil {
		t.Fatal("ignored persistence failure")
	}
	after, _ := e.Store.Graph(g.ID)
	if !reflect.DeepEqual(g, after) {
		t.Fatal("unpersisted state published")
	}
}
