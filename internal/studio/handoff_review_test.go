package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mitos/agents"
	"mitos/internal/cooperation"
)

func TestCooperationHistoryUsesJournalSequenceOnReplay(t *testing.T) {
	s := testStore(t)
	sequences := []int64{}
	// Write the original wire shape: no sequence member in the payload.
	for _, id := range []string{"old-question", "old-answer"} {
		message := cooperation.Message{Version: "mitos.cooperation/v1", ID: id, TaskID: "task", RunID: "origin", Revision: 1, Kind: "question", Body: "Historical evidence"}
		if id == "old-answer" {
			message.Kind = "answer"
			message.InReplyTo = "old-question"
			message.RunID = "resolver"
		}
		data, err := json.Marshal(map[string]any{"cooperation": map[string]any{"project_id": "project", "message": message, "digest": graphDigest(message), "at": time.Now().UTC()}})
		if err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		event := Event{Seq: s.seq + 1, Previous: s.hash, Type: "effect", Kind: "cooperation_message_recorded", At: time.Now().UTC(), Data: data}
		event.Hash = eventHash(event)
		err = s.persist(event)
		s.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		sequences = append(sequences, event.Seq)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, "events.jsonl")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	first, err := reopened.CooperationPage("project", "origin", 0, 1)
	if err != nil || len(first.Messages) != 1 || first.Messages[0].Seq != sequences[0] || first.Next == nil || *first.Next != sequences[0] {
		t.Fatalf("first page: %+v %v", first, err)
	}
	if first.Messages[0].State != "unsupported_version" {
		t.Fatal("historical request became live", first.Messages[0])
	}
	next, err := reopened.CooperationPage("project", "origin", *first.Next, 1)
	if err != nil || len(next.Messages) != 1 || next.Messages[0].Seq != sequences[1] || next.Messages[0].Message.ID != "old-answer" || next.Next != nil {
		t.Fatalf("next page: %+v %v", next, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("replay rewrote history", err)
	}
}

func nativeHandoffGraph(t *testing.T) (*Engine, WorkGraph, string) {
	t.Helper()
	e, req := graphEngine(t, false)
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	var generalist agents.Definition
	for _, d := range library {
		if d.ID == "mitos.generalist" {
			generalist = d
		}
	}
	e.Apps = append(e.Apps, Application{ID: "native-fixture", Name: "Native worker", Agent: &AgentBinding{Definition: generalist}})
	req.Policy.Automatic = true
	req.Policy.Mode = "defined"
	req.Policy.Applications = append(req.Policy.Applications, "native-fixture")
	worker := graphWorker("worker", "goal")
	worker.Brief.AppID = "native-fixture"
	worker.Traits = traits("small", "low")
	worker.Acceptance = "operator"
	req.Tasks = append(req.Tasks, worker)
	g := createGraphTest(t, e, req)
	for id, m := range g.Members {
		if m.Spec.Kind == "application" {
			return e, g, id
		}
	}
	t.Fatal("worker missing")
	return nil, WorkGraph{}, ""
}

func TestHandoffAcceptancePreservesPreContractCandidates(t *testing.T) {
	for _, protocol := range []string{"", HandoffVersion} {
		t.Run(map[bool]string{true: "before-contract", false: "typed-contract"}[protocol == ""], func(t *testing.T) {
			e, g, id := nativeHandoffGraph(t)
			m := g.Members[id]
			candidate := Run{ID: "completed-native", TaskID: id, ProjectID: g.ProjectID, Brief: m.Spec.Brief, Status: "done", HandoffProtocol: protocol, GraphAttempt: &GraphAttempt{GraphID: g.ID, Executor: *m.Executor, Specification: m.Revision}, CreatedAt: time.Now().UTC()}
			next := graphClone(g)
			member := next.Members[id]
			member.CurrentRunID = candidate.ID
			member.Attempts = []string{candidate.ID}
			member.State = "done"
			gc := graphChange{Graph: next, Runs: []Run{candidate}}
			e.Store.mu.Lock()
			e.Store.projectGraph(&gc, &g)
			err := e.Store.commit("", "effect", "fixture_completed_candidate", change{Graph: &gc})
			e.Store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(e.Store.dir)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			e.Store = reopened
			saved, err := reopened.Get(candidate.ID)
			if err != nil || saved.HandoffProtocol != protocol {
				t.Fatal(saved.HandoffProtocol, err)
			}
			current, _ := reopened.Graph(g.ID)
			p := proposal(current, "review", GraphOperation{Kind: "accept", Task: id, Evidence: "Saved report", Rationale: "Reviewed pre-existing evidence"})
			p.Reads = map[string]GraphRead{id: {Specification: current.Members[id].Revision, Fact: current.Members[id].FactRevision}}
			accepted, _, err := e.AmendGraph(p)
			if protocol == "" {
				if err != nil || accepted.Members[id].Accepted == nil {
					t.Fatal("historical candidate could not be reviewed", err)
				}
			} else {
				var failure *HandoffFailure
				if !errors.As(err, &failure) || failure.Code != "result_missing" {
					t.Fatal("new candidate bypassed mandatory receipt", err)
				}
			}
		})
	}
}

func TestHandoffContractIsFrozenAtAttemptCreation(t *testing.T) {
	e, g, id := nativeHandoffGraph(t)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	current, _ := e.Store.Graph(g.ID)
	runID := current.Members[id].CurrentRunID
	run, err := e.Store.Get(runID)
	if err != nil || run.HandoffProtocol != HandoffVersion {
		t.Fatal("new native attempt not bound", run.HandoffProtocol, err)
	}
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(e.Store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	run, err = reopened.Get(runID)
	if err != nil || !requiresHandoff(&run) {
		t.Fatal("contract lost on replay", run, err)
	}
	if err = reopened.Transition(runID, "started", "running", "Running", "Native attempt"); err != nil {
		t.Fatal(err)
	}
	var failure *HandoffFailure
	if err = reopened.Complete(runID, "prose without a receipt", true); !errors.As(err, &failure) || failure.Code != "result_missing" {
		t.Fatal("new completion bypassed receipt", err)
	}
}
