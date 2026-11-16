package studio

import (
	"encoding/json"
	"slices"
	"time"
	"unicode/utf8"
)

func (s *Store) resultIntegrity(r *Run) error {
	if r.Result == nil {
		return handoffError("result_missing", 0)
	}
	for _, ref := range r.Result.Submission.Artifacts {
		found := false
		for _, a := range r.Artifacts {
			if artifactID(r.ID, a) == ref.ArtifactID {
				f, err := s.verifiedArtifact(r, a)
				if err != nil {
					return err
				}
				f.Close()
				found = true
				break
			}
		}
		if !found {
			return handoffError("artifact_missing", 0)
		}
	}
	return nil
}

func (s *Store) submitResult(run string, in ResultSubmission) (ResultReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[run]
	if r == nil {
		return ResultReceipt{}, ErrNotFound
	}
	digest := graphDigest(in)
	if r.Result != nil {
		if r.Result.Digest == digest {
			return graphClone(*r.Result), nil
		}
		return ResultReceipt{}, ErrConflict
	}
	t := s.tasks[r.TaskID]
	if r.Status != "running" || t == nil || t.CurrentRunID != r.ID {
		return ResultReceipt{}, ErrConflict
	}
	if in.Version != HandoffVersion || len(in.Key) == 0 || len(in.Key) > 100 || !utf8.ValidString(in.Summary) || len(in.Summary) == 0 || len(in.Summary) > 2048 || len(in.Artifacts) > 4096 || len(in.Checks) > 64 || len(in.Limitations) > 32 || len(in.UnresolvedRequests) > 32 {
		return ResultReceipt{}, handoffError("invalid_result", 0)
	}
	refs := map[string]bool{}
	for _, ref := range in.Artifacts {
		if !validArtifactRole(ref.Role) || refs[ref.ArtifactID] {
			return ResultReceipt{}, handoffError("invalid_artifact_reference", 0)
		}
		refs[ref.ArtifactID] = true
		found := false
		for _, a := range r.Artifacts {
			if artifactID(run, a) == ref.ArtifactID {
				if a.Role != "" && a.Role != ref.Role {
					return ResultReceipt{}, ErrConflict
				}
				f, err := s.verifiedArtifact(r, a)
				if err != nil {
					return ResultReceipt{}, err
				}
				f.Close()
				found = true
				break
			}
		}
		if !found {
			return ResultReceipt{}, handoffError("artifact_missing", 0)
		}
	}
	for _, check := range in.Checks {
		if len(check.Name) == 0 || len(check.Name) > 256 || !slices.Contains([]string{"passed", "failed", "not_run"}, check.Outcome) || len(check.Evidence) > 32 {
			return ResultReceipt{}, handoffError("invalid_check", 0)
		}
		for _, id := range check.Evidence {
			if !refs[id] {
				return ResultReceipt{}, handoffError("uncited_check", 0)
			}
		}
	}
	for _, text := range in.Limitations {
		if len(text) > 2048 {
			return ResultReceipt{}, handoffError("invalid_limitation", 0)
		}
	}
	for _, id := range in.UnresolvedRequests {
		rec, ok := s.cooperation[r.ProjectID+"/"+id]
		if !ok || rec.Message.RunID != r.ID || !rec.Message.Request() {
			return ResultReceipt{}, handoffError("invalid_request_reference", 0)
		}
	}
	receipt := ResultReceipt{ID: newID("result"), ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: run, Revision: t.Revision, Seq: s.seq + 1, At: time.Now().UTC(), Digest: digest, Submission: in}
	if err := s.commit(run, "effect", "result_submitted", change{Result: &receipt}); err != nil {
		return ResultReceipt{}, err
	}
	return graphClone(receipt), nil
}

func (s *Store) readResult(consumer string, in ResultRead) (ResultPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, err := s.handoffFullProducer(consumer, in.RunID)
	if err != nil {
		return ResultPage{}, err
	}
	if r.Result == nil {
		return ResultPage{}, handoffError("result_missing", 0)
	}
	receipt := r.Result
	sub := receipt.Submission
	entries := []ResultEntry{}
	for _, a := range sub.Artifacts {
		copy := a
		entries = append(entries, ResultEntry{Kind: "artifact", Artifact: &copy})
	}
	for _, c := range sub.Checks {
		copy := c
		entries = append(entries, ResultEntry{Kind: "check", Check: &copy})
	}
	for _, text := range sub.Limitations {
		entries = append(entries, ResultEntry{Kind: "limitation", Text: text})
	}
	for _, text := range sub.UnresolvedRequests {
		entries = append(entries, ResultEntry{Kind: "request", Text: text})
	}
	if in.Cursor < 0 || in.Cursor > len(entries) || in.Limit < 1 || in.Limit > 16 {
		return ResultPage{}, handoffError("invalid_page", int64(in.Cursor))
	}
	state := "incomplete"
	if r.Status == "done" {
		state = "candidate"
	}
	if r.GraphAttempt != nil {
		if g := s.graphs[r.GraphAttempt.GraphID]; g != nil {
			if m := g.Members[r.TaskID]; m != nil && m.Accepted != nil && m.Accepted.RunID == r.ID {
				state = "accepted"
			}
		}
	}
	page := ResultPage{ID: receipt.ID, RunID: r.ID, TaskID: r.TaskID, Revision: receipt.Revision, Seq: receipt.Seq, At: receipt.At, Summary: sub.Summary, State: state, Entries: []ResultEntry{}}
	end := in.Cursor
	for end < len(entries) && end-in.Cursor < in.Limit {
		page.Entries = append(page.Entries, entries[end])
		raw, _ := json.Marshal(page)
		if len(raw) > 48<<10 {
			page.Entries = page.Entries[:len(page.Entries)-1]
			break
		}
		end++
	}
	if end < len(entries) {
		page.Next = &end
	}
	return page, nil
}

func requiresHandoff(r *Run) bool {
	return r.GraphAttempt != nil && r.GraphAttempt.Executor.Application.Agent != nil && !isOrchestratorKind(r.GraphAttempt.Executor.Application.Agent.Definition.Kind)
}
