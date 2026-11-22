package studio

import (
	"encoding/json"
	"slices"
	"time"
	"unicode/utf8"
)

func (s *Store) resultIntegrity(r *Run) error {
	if r.HandoffProtocol != "" && r.HandoffProtocol != HandoffVersion {
		return handoffError("unsupported_handoff_version", 0)
	}
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
	return s.submitJudgedResult(run, in, nil)
}

// submitJudgedResult records one immutable typed result. A non-nil judgment is
// a settled decision permitting a judgment-gated rule for exactly this
// digest; nil keeps the conservative default semantics.
func (s *Store) submitJudgedResult(run string, in ResultSubmission, judgment *KernelJudgment) (ResultReceipt, error) {
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
	if judgment != nil && (judgment.Digest != digest || judgment.State != "applied") {
		return ResultReceipt{}, ErrConflict
	}
	// A pending judgment holds this attempt's submission: an identical
	// resubmission stays held, anything else conflicts.
	if judgment == nil && r.HeldJudgment != nil {
		if r.HeldJudgment.Digest == digest {
			return ResultReceipt{}, &heldJudgment{judgment: *r.HeldJudgment}
		}
		return ResultReceipt{}, ErrConflict
	}
	t := s.tasks[r.TaskID]
	permit := judgment
	if permit == nil {
		// A settled judgment decides one digest once: applied permits the
		// gated intent on retry, refused keeps the refusal without re-asking.
		for i := len(r.KernelJudgments) - 1; i >= 0; i-- {
			if r.KernelJudgments[i].Digest != digest {
				continue
			}
			if r.KernelJudgments[i].State == "applied" {
				permit = &r.KernelJudgments[i]
			} else if r.KernelJudgments[i].State == "refused" {
				k := r.KernelJudgments[i]
				return ResultReceipt{}, judgmentRefusal(k.Code, k.Detail)
			}
			break
		}
	}
	if permit != nil && permit.Resolver == "decision_application" && permit.ResolverRevision != nil && *permit.ResolverRevision != s.agentPolicy(r.ProjectID).Revision {
		return ResultReceipt{}, ErrConflict
	}
	if permit == nil && (r.Status != "running" || t == nil || t.CurrentRunID != r.ID) {
		return ResultReceipt{}, ErrConflict
	}
	if t == nil {
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
	}
	if err := s.verifySubmissionEvidence(r, in, refs); err != nil {
		return ResultReceipt{}, err
	}
	if err := s.validateProposals(r, in.Proposals, permit != nil); err != nil {
		return ResultReceipt{}, err
	}
	receipt, err := s.buildResultReceipt(r, in, time.Now().UTC())
	if err != nil {
		return ResultReceipt{}, err
	}
	c := change{Result: &receipt}
	if judgment != nil {
		// Journal the decision receipt in the same record as the outcome.
		c.Judgment = judgment
	}
	if err := s.commit(run, "effect", "result_submitted", c); err != nil {
		return ResultReceipt{}, err
	}
	return graphClone(receipt), nil
}

// SubmittedResult returns the run's immutable result when it was submitted for
// the task's current revision (issue #127): the work itself is finished, so
// only the wrap-up can have been cut short.
func (s *Store) SubmittedResult(run string) (ResultReceipt, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[run]
	if r == nil || r.Result == nil {
		return ResultReceipt{}, false
	}
	t := s.tasks[r.TaskID]
	if t == nil || r.Result.Revision != t.Revision {
		return ResultReceipt{}, false
	}
	return *r.Result, true
}

// verifySubmissionEvidence checks artifact references, checks, limitations and
// request references against the attempt's current verified evidence.
func (s *Store) verifySubmissionEvidence(r *Run, in ResultSubmission, refs map[string]bool) error {
	for _, ref := range in.Artifacts {
		found := false
		for _, a := range r.Artifacts {
			if artifactID(r.ID, a) == ref.ArtifactID {
				if a.Role != "" && a.Role != ref.Role {
					return ErrConflict
				}
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
	for _, check := range in.Checks {
		if len(check.Name) == 0 || len(check.Name) > 256 || !slices.Contains([]string{"passed", "failed", "not_run"}, check.Outcome) || len(check.Evidence) > 32 {
			return handoffError("invalid_check", 0)
		}
		for _, id := range check.Evidence {
			if !refs[id] {
				return handoffError("uncited_check", 0)
			}
		}
	}
	for _, text := range in.Limitations {
		if len(text) > 2048 {
			return handoffError("invalid_limitation", 0)
		}
	}
	for _, id := range in.UnresolvedRequests {
		rec, ok := s.cooperation[r.ProjectID+"/"+id]
		if !ok || rec.Message.RunID != r.ID || !rec.Message.Request() {
			return handoffError("invalid_request_reference", 0)
		}
	}
	return nil
}

// buildResultReceipt mints the receipt for a submission recorded now. Caller
// holds the store lock and has validated the submission.
func (s *Store) buildResultReceipt(r *Run, in ResultSubmission, at time.Time) (ResultReceipt, error) {
	t := s.tasks[r.TaskID]
	if t == nil {
		return ResultReceipt{}, ErrConflict
	}
	receipt := ResultReceipt{ID: newID("result"), ProjectID: r.ProjectID, TaskID: r.TaskID, RunID: r.ID, Revision: t.Revision, Seq: s.seq + 1, At: at, Digest: graphDigest(in), Submission: in}
	for range in.Proposals {
		receipt.ProposalIDs = append(receipt.ProposalIDs, newID("proposal"))
	}
	return receipt, nil
}

// validateProposals checks proposal shape, ownership and caps at submission.
// Admission of the proposed work stays with the orchestrator and normal graph
// validation; this only refuses what could never be admitted. Deterministic
// refusals fail closed; the nature ceiling on blocking proposals is a
// judgment-shaped rule, so unless a settled judgment permits it (permitted),
// it raises a decision instead of a hard refusal. Caller holds the store lock.
func (s *Store) validateProposals(r *Run, proposals []ResultProposal, permitted bool) error {
	if len(proposals) == 0 {
		return nil
	}
	if r.GraphAttempt == nil {
		return handoffError("proposals_unsupported", 0)
	}
	g := s.graphs[r.GraphAttempt.GraphID]
	if g == nil || g.Status != "active" || g.Policy.Mode == "defined" {
		return handoffError("proposals_unsupported", 0)
	}
	m := g.Members[r.TaskID]
	if m == nil || m.OrchestratorFor != "" || m.CurrentRunID != r.ID {
		return handoffError("proposals_unsupported", 0)
	}
	if m.Proposed+len(proposals) > HandoffProposalsPerTask {
		return handoffError("proposal_limit_exceeded", int64(HandoffProposalsPerTask-m.Proposed))
	}
	ids := map[string]bool{}
	tasks, blocking := 0, false
	for i, p := range proposals {
		raw, _ := json.Marshal(p)
		if !validGraphKey(p.ID) || ids[p.ID] || len(p.Rationale) == 0 || len(p.Rationale) > 4000 || !utf8.ValidString(p.Rationale) || len(p.Tasks) == 0 || len(p.Tasks) > HandoffTasksPerProposal || len(p.Artifacts) > 32 || len(raw) > handoffProposalBytes {
			return handoffError("invalid_proposal", int64(i))
		}
		ids[p.ID] = true
		blocking = blocking || p.Blocking
		tasks += len(p.Tasks)
		local := map[string]bool{}
		for _, spec := range p.Tasks {
			if !validGraphKey(spec.ID) || local[spec.ID] || spec.Kind != "application" || spec.Brief.AppID == "" || spec.Brief.Title == "" || spec.Criteria == "" {
				return handoffError("invalid_proposal", int64(i))
			}
			local[spec.ID] = true
		}
		for _, spec := range p.Tasks {
			for _, dep := range spec.Brief.Scheduling.Deps {
				if dep.Inbox != nil || !local[dep.From] && g.Members[dep.From] == nil {
					return handoffError("invalid_proposal", int64(i))
				}
			}
		}
		for _, id := range p.Artifacts {
			found := false
			for _, a := range r.Artifacts {
				if artifactID(r.ID, a) == id {
					found = true
					break
				}
			}
			if !found {
				return handoffError("artifact_missing", int64(i))
			}
		}
	}
	if blocking && m.Yields >= HandoffYieldsPerTask {
		return handoffError("yield_limit_exceeded", int64(HandoffYieldsPerTask))
	}
	if len(g.Members)+tasks > g.Limits.Tasks {
		return handoffError("proposal_over_budget", int64(g.Limits.Tasks-len(g.Members)))
	}
	if !permitted && blocking && m.Spec.Traits != nil && len(natureCeilings[m.Spec.Traits.Nature]) > 0 {
		return &judgmentRequired{
			code:   "proposal_blocking_not_permitted",
			detail: m.Spec.Traits.Nature + " tasks may propose follow-up work but cannot yield on it",
		}
	}
	return nil
}

func (s *Store) readResult(consumer string, in ResultRead) (ResultPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, err := s.handoffFullProducer(consumer, in.RunID)
	if err != nil {
		return ResultPage{}, err
	}
	if r.Result == nil {
		if held := r.HeldJudgment; held != nil && held.State == "pending" {
			// The submission is held, not lost: report the gate and its reason.
			return ResultPage{ID: "held:" + held.ID, RunID: r.ID, TaskID: r.TaskID, State: "held", Summary: "Held for a judgment on " + held.Code + ": " + held.Detail, Entries: []ResultEntry{}}, nil
		}
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
	for i, p := range sub.Proposals {
		entry := ProposalEntry{Detail: p, Outcome: "pending"}
		if i < len(receipt.ProposalIDs) {
			entry.ID = receipt.ProposalIDs[i]
			if r.GraphAttempt != nil {
				if g := s.graphs[r.GraphAttempt.GraphID]; g != nil {
					if rec := g.Proposals[entry.ID]; rec != nil && rec.Outcome != "" {
						entry.Outcome = rec.Outcome
					}
				}
			}
		}
		entries = append(entries, ResultEntry{Kind: "proposal", Proposal: &entry})
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
			} else if m != nil && m.Yield != nil && m.Yield.RunID == r.ID {
				state = "yielded"
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
	return r.HandoffProtocol != ""
}

// validateProposalExecution applies the task trait contract to proposed specs
// at submission, so a worker learns that its proposal could never be routed
// before the orchestrator tries to admit it.
func (e *Engine) validateProposalExecution(run string, proposals []ResultProposal) error {
	if len(proposals) == 0 {
		return nil
	}
	r, err := e.Store.Get(run)
	if err != nil || r.GraphAttempt == nil {
		return handoffError("proposals_unsupported", 0)
	}
	g, err := e.Store.Graph(r.GraphAttempt.GraphID)
	if err != nil {
		return handoffError("proposals_unsupported", 0)
	}
	p := graphExecution(&g)
	for i, proposal := range proposals {
		for _, spec := range proposal.Tasks {
			app, ok := e.App(spec.Brief.AppID)
			if !ok {
				return &HandoffFailure{Code: "invalid_proposal", Position: int64(i), Detail: "unknown application " + spec.Brief.AppID}
			}
			if app.Agent == nil {
				if spec.Traits != nil || spec.Harness != "" {
					return &HandoffFailure{Code: "invalid_proposal", Position: int64(i), Detail: "traits and harness apply to native agent tasks"}
				}
				continue
			}
			if isOrchestratorKind(app.Agent.Definition.Kind) {
				return &HandoffFailure{Code: "invalid_proposal", Position: int64(i), Detail: "workers cannot propose orchestrators"}
			}
			traits, err := resolveTraits(app.Agent.Definition, spec.Traits)
			if err != nil {
				return &HandoffFailure{Code: "invalid_proposal_traits", Position: int64(i), Detail: err.Error()}
			}
			if _, _, err = selectHarness(p, app.Agent.Definition, traits.Nature, spec.Harness, graphEnforcement(&g)); err != nil {
				return &HandoffFailure{Code: "proposal_unroutable", Position: int64(i), Detail: err.Error()}
			}
		}
	}
	return nil
}
