package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	workspacefs "mitos/internal/workspace"
)

func (s *Store) recordNativeThread(id, thread string) error {
	if strings.TrimSpace(thread) == "" || len(thread) > 256 {
		return graphError("invalid native conversation identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.NativeThreadID != "" && r.NativeThreadID != thread {
		return ErrConflict
	}
	return s.commit(id, "effect", "agent_conversation_saved", change{NativeThreadID: thread})
}

// Read only journal-bound, immutable history; never reuse the mutable old
// working tree. The original baseline keeps patches cumulative across retries.
func (e *Engine) agentContinuation(ctx context.Context, r Run) (Run, workspacefs.Snapshot, workspacefs.Snapshot, error) {
	prior, err := e.Store.Get(r.GraphAttempt.ContinueFrom)
	if err != nil {
		return prior, workspacefs.Snapshot{}, workspacefs.Snapshot{}, err
	}
	if !prior.Terminal() || prior.TaskID != r.TaskID || prior.ProjectID != r.ProjectID || prior.GraphAttempt == nil || prior.GraphAttempt.GraphID != r.GraphAttempt.GraphID || prior.GraphAttempt.Executor.Digest != r.GraphAttempt.Executor.Digest || prior.Claim != nil && prior.Claim.ReleasedAt == nil {
		return prior, workspacefs.Snapshot{}, workspacefs.Snapshot{}, ErrConflict
	}
	started := prior.NativeThreadID != ""
	for _, event := range prior.Events {
		if event.Kind == "agent_started" {
			started = true
		}
	}
	// Process startup is not proof that the conversation opened. A failed
	// initialize/thread-resume handshake can still harvest the restored tree.
	// Recover its conversation from the validated lineage while retaining that
	// latest harvest; only fall back to the ancestor's tree if none was saved.
	if prior.NativeThreadID == "" && prior.GraphAttempt.ContinueFrom != "" {
		ancestor, before, current, err := e.agentContinuation(ctx, prior)
		if err != nil || prior.Workspace == nil || prior.Workspace.ResultCommit == "" {
			return ancestor, before, current, err
		}
		prior.NativeThreadID = ancestor.NativeThreadID
	}
	if prior.Workspace == nil {
		// A pre-launch failure has no workspace or conversation to restore.
		if prior.NativeThreadID != "" {
			return prior, workspacefs.Snapshot{}, workspacefs.Snapshot{}, graphError("saved conversation has no workspace")
		}
		return prior, workspacefs.Snapshot{}, workspacefs.Snapshot{}, nil
	}
	base := filepath.Join(e.Store.Dir(), "workspaces", prior.ID)
	history := &workspacefs.History{Base: base, Directory: filepath.Join(base, "history.git"), BaselineCommit: prior.Workspace.BaselineCommit}
	fingerprint := ""
	for _, event := range prior.Events {
		var c change
		if json.Unmarshal(event.Data, &c) == nil && c.Workspace != nil && c.Workspace.ResultCommit == "" {
			fingerprint = c.Workspace.Fingerprint
		}
	}
	before, err := history.Load(ctx, history.BaselineCommit, fingerprint)
	if err != nil {
		return prior, before, workspacefs.Snapshot{}, fmt.Errorf("restore agent baseline: %w", err)
	}
	current := before
	if prior.Workspace.ResultCommit != "" {
		current, err = history.Load(ctx, prior.Workspace.ResultCommit, prior.Workspace.Fingerprint)
	} else {
		if started {
			err = graphError("agent workspace was not harvested; inspect the interrupted execution before continuing")
		}
	}
	return prior, before, current, err
}
