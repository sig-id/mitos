package studio

import (
	"fmt"
	"strings"
	"time"
)

// The process has stopped, but starting a replacement cannot supply the missing
// answer. Preserve that distinction from an execution failure in the journal.
type agentPrerequisiteError struct{ summary string }

func (e *agentPrerequisiteError) Error() string { return e.summary }

func (s *Store) pendingAgentPrerequisite(runID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.graphPendingRequests(runID) {
		// Async clarification permits independent work while the agent is
		// running. If that attempt is interrupted, retain its unanswered input
		// just like a blocking request instead of replacing it with a timeout.
		if p.Decision.Choice == "" {
			return &agentPrerequisiteError{summary: fmt.Sprintf("Stopped while waiting for an answer to %q. No answer or permission was inferred. Saved work is preserved. Review approval settings or the missing input, then resume this workflow; automatic replacement would repeat the same wait.", p.Decision.Question)}
		}
	}
	return nil
}

func (s *Store) blockAgentPrerequisite(id, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	if r.Status == "stopping" {
		// Workflow expiry can request cancellation while the provider is still
		// harvesting. Retain the unanswered prerequisite in that race, while
		// giving explicit operator cancellation precedence.
		if r.GraphAttempt == nil {
			return ErrConflict
		}
		g := s.graphs[r.GraphAttempt.GraphID]
		if g == nil || g.Status != "active" || time.Now().Before(g.Limits.Deadline) {
			return ErrConflict
		}
		m := g.Members[r.TaskID]
		if m == nil || m.CancelRequested || m.Replacement != "" {
			return ErrConflict
		}
	}
	return s.commit(id, "effect", "agent_prerequisite_blocked", change{Status: "failed", Stage: "Answer required before resuming", Summary: summary, BlockedReason: "native_request_unanswered"})
}

func (s *Store) graphPrerequisiteBlocked(g *WorkGraph) bool {
	for _, m := range g.Members {
		if m.CancelRequested || m.Replacement != "" || m.Accepted != nil {
			continue
		}
		if r := s.runs[m.CurrentRunID]; r != nil && r.BlockedReason != "" {
			return true
		}
	}
	return false
}

func (s *Store) GraphPrerequisiteBlocked(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.graphs[id]
	return g != nil && s.graphPrerequisiteBlocked(g)
}

func agentCacheEnvironment(env []string, cache string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "GOCACHE=") {
			result = append(result, entry)
		}
	}
	return append(result, "GOCACHE="+cache)
}
