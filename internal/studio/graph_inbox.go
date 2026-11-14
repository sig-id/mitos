package studio

import (
	"encoding/json"
	"fmt"

	"mitos/internal/messaging"
	"mitos/internal/workpool"
)

// A zero cursor means future messages: freeze the journal position when the
// specification is committed. Retries retain it and the admission witness.
func (s *Store) normalizeGraphInbox(g *WorkGraph, deps []workpool.Dependency) error {
	for i := range deps {
		if deps[i].Inbox == nil {
			continue
		}
		in := *deps[i].Inbox
		deps[i].Inbox = &in
		if in.Project != g.ProjectID {
			return graphError("inbox source must belong to the graph project")
		}
		if !messaging.ValidID(in.Project) || !messaging.ValidID(in.Mailbox) || in.Sender != "" && !messaging.ValidID(in.Sender) {
			return graphError("invalid inbox address")
		}
		if in.AfterSequence == 0 {
			in.AfterSequence = s.seq
		}
		if in.AfterSequence > s.seq {
			return graphError("inbox cursor cannot be ahead of the journal")
		}
	}
	return nil
}

// Caller holds the store lock. Rejected/expired deliveries are durable inbox
// records too, but can never enable work. Earliest admitted match wins.
func (s *Store) graphInboxState(dep workpool.Dependency) workpool.State {
	in := dep.Inbox
	state := workpool.State{ID: dep.From, Status: "pending"}
	for _, record := range s.messagesIn {
		m := record.Message
		if (record.Receipt.Status != "accepted" && record.Receipt.Status != "applied") || record.Sequence <= in.AfterSequence || m.Project != in.Project || m.Mailbox != in.Mailbox || m.Kind != in.Kind || m.Type != in.Type || m.Generation != in.Generation || in.Sender != "" && m.From != in.Sender || in.Correlation != "" && m.Correlation != in.Correlation {
			continue
		}
		if state.Inbox != nil && record.Sequence >= state.Seq {
			continue
		}
		body, _ := json.Marshal(m)
		state.Status, state.Seq, state.Hash = "inbox_received", record.Sequence, m.Fingerprint()
		state.Inbox = &workpool.InboxWitness{Key: m.Key(), Message: body}
	}
	return state
}

func (s *Store) boundInboxInput(w workpool.Witness) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.messagesIn[w.Inbox.Key]
	if !ok || record.Sequence != w.Seq || record.Message.Fingerprint() != w.Hash || (record.Receipt.Status != "accepted" && record.Receipt.Status != "applied") {
		return nil, fmt.Errorf("inbox witness no longer identifies an admitted message")
	}
	body, _ := json.Marshal(record.Message)
	if string(body) != string(w.Inbox.Message) {
		return nil, fmt.Errorf("inbox witness content changed")
	}
	return body, nil
}

func graphInboxAddress(g *WorkGraph, seq int64) map[string]any {
	return map[string]any{"project": g.ProjectID, "mailbox": g.ID, "generation": g.OrchestratorGeneration, "after_sequence": seq}
}

// Caller holds the store lock. Advance unsatisfied graph-mailbox selectors with
// their admission authority. Queued attempts have frozen specifications; an
// already received message also remains eligible while other join sources wait.
func (s *Store) advanceGraphOrchestratorGeneration(g *WorkGraph) {
	previous := g.OrchestratorGeneration
	g.OrchestratorGeneration++
	for _, m := range g.Members {
		if m.Spec.Kind != "application" || m.CurrentRunID != "" || len(m.Attempts) != 0 || m.Accepted != nil || m.CancelRequested || m.Replacement != "" {
			continue
		}
		changed := false
		for _, dep := range m.Spec.Brief.Scheduling.Deps {
			in := dep.Inbox
			if in == nil || in.Project != g.ProjectID || in.Mailbox != g.ID || in.Kind != "event" || in.Type != "workflow.signal" || int64(in.Generation) != previous || s.graphInboxState(dep).Inbox != nil {
				continue
			}
			if !changed {
				m.History = append(m.History, graphClone(m.Spec))
				m.Revision++
				changed = true
			}
			in.Generation = int(g.OrchestratorGeneration)
		}
	}
}
