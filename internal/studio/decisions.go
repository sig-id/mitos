package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"mitos/internal/autonomy"
	"mitos/internal/decision"
)

// Historical local logs did not stamp tenants. They remain readable only through
// the local operator boundary; this is not a migration into a hosted tenant.
func runTenant(r *Run) decision.Tenant {
	if r.Tenant.Valid() {
		return r.Tenant
	}
	return decision.LocalTenant
}

func (s *Store) Park(id string, draft *HumanDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() || r.Status == "stopping" || draft == nil {
		return ErrConflict
	}
	// A same-user command has no enforceable confinement grant. Recovery, unknown
	// modal state, and oracle failures therefore require explicit judgment.
	assessment := decision.Classify(decision.Act{Unknown: true}, decision.Grants{})
	if draft.Reversibility != assessment.Reversibility {
		return errors.New("card reversibility does not match the substrate assessment")
	}
	b, err := json.Marshal(draft)
	if err != nil {
		return err
	}
	var d HumanDecision
	if err = json.Unmarshal(b, &d); err != nil {
		return err
	}
	d.RunID = id
	d.Tenant = runTenant(r)
	d.CausedBy = s.seq + 1
	d.RequestedAt = time.Now().UTC()
	d.Choice = ""
	d.ResolvedAt = nil
	d.Resolution = nil
	d.AppliedAt = nil
	d.ResultRunID = ""
	d.ApplicationError = ""
	if d.Kind == "" {
		d.Kind = "choose"
	}
	if d.Kind != "choose" {
		return errors.New("this runtime only executes choice cards")
	}
	if d.Context.Attempted == "" {
		d.Context.Attempted = r.Brief.Title
	}
	d.Context.EvidenceRefs = []string{}
	for _, artifact := range r.Artifacts[max(0, len(r.Artifacts)-6):] {
		d.Context.EvidenceRefs = append(d.Context.EvidenceRefs, artifact.Name)
	}
	d.BlastRadius = decision.BlastRadius{Paths: []string{r.Brief.Workspace}, Destinations: []string{}}
	d.CostOfWrong.WastedWallMS = int64(r.Brief.TimeoutMinutes) * 60000
	for _, option := range d.Options {
		if option.ID != "retry" && option.ID != "cancel" && option.ID != "rewind" {
			return errors.New("this runtime cannot execute the decision option")
		}
	}
	key, _ := json.Marshal(struct {
		Run, Signal, Question string
		Options               []Option
	}{id, d.ReasonSignal, d.Question, d.Options})
	hash := sha256.Sum256(key)
	d.DedupeKey = hex.EncodeToString(hash[:])
	if err = d.Validate(false); err != nil {
		return err
	}
	if r.Decision != nil && r.Decision.Choice == "" {
		if r.Decision.DedupeKey == d.DedupeKey {
			return nil
		}
		return ErrConflict
	}
	policyEvidence, _ := json.Marshal(map[string]any{"act": decision.Act{Unknown: true}, "workspace_confined": false, "assessment": assessment})
	request, err := s.prepareEvent(id, "decision", "request_approval", change{Evidence: policyEvidence, DecisionRequest: &d, Status: "parked", Stage: "Preparing your decision", Summary: "Request an explicit choice for " + d.ReasonSignal + "."}, s.seq+1, s.hash)
	if err != nil {
		return err
	}
	// The effect explicitly supplies the final status and card. It can be
	// derived from the pre-request state, then published with the request so
	// readers never see a parked run before its decision is available.
	requested, err := s.prepareEvent(id, "effect", "approval_requested", change{Status: "parked", Stage: "Waiting for you", Summary: d.Context.BlockedBy, Decision: &d}, request.Seq+1, request.Hash)
	if err != nil {
		return err
	}
	return s.persist(request, requested)
}

func (s *Store) Resolve(id, decisionID, choice string) error {
	return s.ResolveChoice(id, decision.Resolution{DecisionID: decisionID, Choice: choice, Via: "console"}, decision.LocalOperator(), nil)
}
func (s *Store) ResolveChoice(id string, resolution decision.Resolution, principal decision.Principal, rewind *autonomy.Rewind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	reject := func(reason string, result error) error {
		b, _ := json.Marshal(map[string]string{"decision_id": resolution.DecisionID, "reason": reason, "via": resolution.Via})
		if err := s.commit(id, "effect", "decision_rejected", change{Evidence: b}); err != nil {
			return err
		}
		return result
	}
	if !principal.Authenticated || principal.ID == "" || principal.Tenant != runTenant(r) {
		return reject("resolver is unauthenticated or outside the run tenant", ErrConflict)
	}
	if r.Decision == nil || r.Decision.ID != resolution.DecisionID {
		return reject("stale decision", ErrConflict)
	}
	if resolution.Via != "console" && resolution.Via != "cli" && resolution.Via != "api" {
		return reject("unsupported resolution transport", ErrConflict)
	}
	if len(resolution.Value) > 0 || len(resolution.Note) > 10000 {
		return reject("unsupported value or oversized note", ErrConflict)
	}
	d := r.Decision
	if d.Choice != "" {
		if d.Choice == resolution.Choice {
			return reject("duplicate resolution; first valid choice retained", nil)
		}
		return reject("conflicting resolution; first valid choice retained", ErrConflict)
	}
	if r.Status != "parked" || !d.HasOption(resolution.Choice) {
		return reject("choice is unavailable", ErrConflict)
	}
	if resolution.Choice == "rewind" {
		if rewind == nil {
			return reject("rewind requires a validated recovery request", ErrConflict)
		}
		if err := rewind.Validate(); err != nil {
			return reject("invalid recovery request", err)
		}
		if r.Execution == nil || !time.Now().Before(r.Execution.Deadline) {
			return reject("original recovery deadline expired", ErrConflict)
		}
		if _, err := s.beginRewind(id, *rewind, false); err != nil {
			return reject("checkpoint recovery is no longer available", err)
		}
	}
	resolution.Resolver = principal.ID
	resolution.ResolvedAt = time.Now().UTC()
	status := "cancelled"
	stage := "Applying your choice"
	if resolution.Choice == "rewind" || resolution.Choice == "retry" {
		status = "parked"
	}
	return s.commit(id, "effect", "approval_resolved", change{Resolution: &resolution, ResolutionRewind: rewind, Choice: resolution.Choice, Status: status, Stage: stage, Summary: "Decision recorded: " + resolution.Choice + ". The previous evidence is preserved."})
}
func (s *Store) DecisionApplied(id, decisionID, resultID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Decision == nil || r.Decision.ID != decisionID || r.Decision.Resolution == nil {
		return ErrConflict
	}
	if r.Decision.AppliedAt != nil {
		return nil
	}
	change := change{DecisionApplied: true, ResultRunID: resultID}
	if r.Status == "cancelled" || r.Decision.Choice == "retry" {
		change.Status = "cancelled"
		change.Stage = "Closed"
		change.Summary = "Your choice was applied. The previous attempt is preserved."
	}
	return s.commit(id, "effect", "decision_applied", change)
}

func (e *Engine) ResolveAs(id, decisionID, choice, note, via string, principal decision.Principal) (string, error) {
	e.actions.Lock()
	defer e.actions.Unlock()
	return e.resolveAs(id, decisionID, choice, note, via, principal, nil)
}
func (e *Engine) resolveAs(id, decisionID, choice, note, via string, principal decision.Principal, request *autonomy.Rewind) (string, error) {
	r, err := e.Store.Get(id)
	if err != nil {
		return "", err
	}
	if choice == "rewind" && r.Claim != nil && r.Claim.ReleasedAt == nil {
		e.mu.Lock()
		_, active := e.cancel[id]
		e.mu.Unlock()
		if !active {
			return "", errors.New("inspect and release the previous execution's claims before recovery")
		}
	}
	if choice == "rewind" && request == nil && r.Decision != nil && r.Decision.ID == decisionID && r.Decision.Choice == "" {
		signal := r.Decision.ReasonSignal
		memo := fmt.Sprintf("The operator requested checkpoint recovery after %s. Correct the recorded failure using the preceding segment's output and predicate evidence, while preserving the original brief.", signal)
		request = &autonomy.Rewind{Scope: "step", FromSegment: len(r.Segments), Memo: memo, ReasonSignal: signal}
		// A crash may interrupt an already recorded restoration. Resume that
		// exact request after inspection, preserving its memo and restart count.
		if r.PendingRewind != nil {
			copy := *r.PendingRewind
			request = &copy
		} else if r.ReadyRewind != nil {
			copy := *r.ReadyRewind
			request = &copy
		}
	}
	if err = e.Store.ResolveChoice(id, decision.Resolution{DecisionID: decisionID, Choice: choice, Note: note, Via: via}, principal, request); err != nil {
		return "", err
	}
	r, err = e.Store.Get(id)
	if err != nil {
		return "", err
	}
	// A historical choice has no resolver envelope. Preserve its existing result
	// without inventing provenance or replaying an old action.
	if r.Decision != nil && r.Decision.Resolution == nil && r.Decision.Choice == choice {
		if choice == "retry" {
			for _, child := range e.Store.List() {
				if child.ParentID == id {
					return child.ID, nil
				}
			}
		}
		return id, nil
	}
	return e.applyResolution(r)
}
func (e *Engine) applyResolution(r Run) (string, error) {
	d := r.Decision
	if d == nil || d.Resolution == nil {
		return "", ErrConflict
	}
	if d.AppliedAt != nil {
		if d.ResultRunID != "" {
			return d.ResultRunID, nil
		}
		return r.ID, nil
	}
	resultID := r.ID
	if r.Status == "cancelled" && d.Resolution.Choice != "cancel" {
		if err := e.Store.failDecisionApplication(r.ID, "The operator stopped this work before the accepted choice could run.", true); err != nil {
			return "", err
		}
		return "", ErrConflict
	}
	switch d.Resolution.Choice {
	case "cancel":
	case "retry":
		child, err := e.retry(r.ID)
		if err != nil {
			return "", err
		}
		resultID = child.ID
	case "rewind":
		if r.DecisionRewind == nil {
			return "", errors.New("recorded rewind request is missing")
		}
		// Recheck state and restart intensity at execution, not just at delivery.
		if r.Execution == nil || !time.Now().Before(r.Execution.Deadline) {
			err := errors.New("the accepted recovery's original deadline expired")
			if recordErr := e.Store.failDecisionApplication(r.ID, err.Error(), true); recordErr != nil {
				return "", recordErr
			}
			next := recoveryDecision()
			next.ReasonSignal = "decision_deadline_expired"
			next.Context.BlockedBy = "The original deadline expired before the accepted recovery could execute. Start a new attempt with a fresh budget, or close this work."
			if recordErr := e.Store.Park(r.ID, next); recordErr != nil {
				return "", recordErr
			}
			return "", err
		}
		if _, err := e.Store.BeginRewind(r.ID, *r.DecisionRewind); err != nil {
			return "", err
		}
	default:
		return "", errors.New("unsupported recorded decision action")
	}
	if err := e.Store.DecisionApplied(r.ID, d.ID, resultID); err != nil {
		return "", err
	}
	return resultID, nil
}
func (e *Engine) reconcileDecisions() {
	e.actions.Lock()
	defer e.actions.Unlock()
	for _, r := range e.Store.List() {
		if r.Sample || r.Decision == nil || r.Decision.Resolution == nil || r.Decision.AppliedAt != nil {
			continue
		}
		if _, err := e.applyResolution(r); err != nil {
			_ = e.Store.failDecisionApplication(r.ID, err.Error(), false)
		}
	}
}

func (s *Store) recoverDecisionRequests() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.runs {
		if r.PendingDecision != nil {
			d := r.PendingDecision
			if err := d.Validate(false); err != nil {
				return err
			}
			if err := s.commit(id, "effect", "approval_requested", change{Status: "parked", Stage: "Waiting for you", Summary: d.Context.BlockedBy, Decision: d}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) failDecisionApplication(id, message string, final bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Decision == nil || r.Decision.Resolution == nil || r.Decision.AppliedAt != nil {
		return nil
	}
	if r.Decision.ApplicationError == message && !final {
		return nil
	}
	c := change{DecisionError: message, DecisionApplied: final, Stage: "Your choice needs attention", Summary: message}
	return s.commit(id, "effect", "decision_application_blocked", c)
}
