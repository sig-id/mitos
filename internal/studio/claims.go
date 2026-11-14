package studio

import (
	"encoding/json"
	"errors"
	"time"

	"mitos/internal/fleet"
	"mitos/internal/workpool"
)

var errOwnershipViolation = errors.New("ownership_violation")

func (s *Store) AcquireClaims(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireClaims(id, nil)
}
func (s *Store) acquireClaims(id string, witnesses []workpool.Witness) (bool, error) {
	r := s.runs[id]
	if r == nil {
		return false, ErrNotFound
	}
	if task := s.tasks[r.TaskID]; task != nil && task.GraphID != "" && !s.graphCanDispatch(task) {
		return false, nil
	}
	if s.lifecycleTaskHeld(s.tasks[r.TaskID]) {
		return false, nil
	}
	if waiting, _ := s.serviceCycleReadiness(s.tasks[r.TaskID]); len(waiting) > 0 {
		return false, nil
	}
	if r.Status != "queued" || r.Sample {
		return false, ErrConflict
	}
	if r.Claim != nil && r.Claim.ReleasedAt == nil {
		return false, errors.New("previous execution claims require inspection before reuse")
	}
	ownership := r.Brief.Ownership
	held := []fleet.Claim{}
	for _, other := range s.runs {
		if other.ID != id && other.Claim != nil && other.Claim.ReleasedAt == nil {
			held = append(held, *other.Claim)
		}
	}
	conflicts := fleet.Conflicts(r.Brief.Workspace, ownership, held)
	if len(conflicts) > 0 {
		b, _ := json.Marshal(conflicts)
		previous, _ := json.Marshal(r.ClaimConflicts)
		if string(b) != string(previous) {
			if err := s.commit(id, "effect", "dispatch_conflict", change{ClaimConflicts: conflicts, Summary: "Waiting for conflicting ownership or resource claims to be released."}); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	kind := "task_scheduled"
	c := change{Status: "running", Stage: "Preparing execution", Summary: "The task was scheduled within its pool's capacity."}
	if len(ownership.Paths) > 0 || len(ownership.Leases) > 0 {
		claim := fleet.Claim{ID: newID("claim"), RunID: id, Workspace: r.Brief.Workspace, Ownership: ownership, AcquiredAt: time.Now().UTC()}
		kind = "claims_acquired"
		c.Claim = &claim
		c.Summary = "Ownership and resource claims were acquired before dispatch."
	}
	c.Task, c.TaskEffect = s.taskChange(id, kind, c, time.Now().UTC(), s.seq+1)
	if c.Task != nil {
		c.Task.DependencyEvidence = witnesses
		policy := s.configuredPools()[c.Task.Scheduling.Pool]
		c.Task.AdmissionPool = &policy
	}
	if r.GraphAttempt != nil {
		bound := graphClone(*r.GraphAttempt)
		now := time.Now().UTC()
		bound.AdmittedAt = &now
		if g := s.graphs[bound.GraphID]; g != nil {
			bound.GraphRevision = g.Revision
			bound.PolicyRevision = g.PolicyRevision
			bound.Generation = g.OrchestratorGeneration
		}
		bound.Witnesses = append([]workpool.Witness(nil), witnesses...)
		c.GraphAttempt = &bound
	}
	if err := s.commit(id, "effect", kind, c); err != nil {
		return false, err
	}
	return true, nil
}
func (s *Store) ReleaseClaims(id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Claim == nil || r.Claim.ReleasedAt != nil {
		return nil
	}
	claim := *r.Claim
	at := time.Now().UTC()
	claim.ReleasedAt = &at
	claim.ReleaseReason = reason
	return s.commit(id, "effect", "claims_released", change{Claim: &claim})
}

// ReleaseInspectedClaims is an operator operation, never an automatic expiry.
// A crashed local provider cannot prove whether escaped/orphaned processes stopped.
func (e *Engine) ReleaseInspectedClaims(id string) error {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	_, active := e.cancel[id]
	if active {
		return errors.New("execution is still active; stop it before releasing claims")
	}
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	r := e.Store.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Status != "parked" && !r.Terminal() {
		return ErrConflict
	}
	if r.Claim == nil || r.Claim.ReleasedAt != nil {
		return nil
	}
	claim := *r.Claim
	at := time.Now().UTC()
	claim.ReleasedAt = &at
	claim.ReleaseReason = "operator_confirmed_stopped"
	evidence, err := json.Marshal(map[string]string{"resolver": "local-operator"})
	if err != nil {
		return err
	}
	return e.Store.commit(id, "decision", "claims_release_confirmed", change{Claim: &claim, Evidence: evidence, Summary: "The local operator confirmed that the previous execution was inspected and stopped."})
}
func ownershipDecision() *HumanDecision {
	d := recoveryDecision()
	d.ReasonSignal = "ownership_violation"
	d.InterruptClass = "judgment"
	d.Question = "Correct the work that exceeded its declared paths?"
	d.Context.BlockedBy = "The harvested change includes paths outside this run's declared ownership. The patch is preserved and acceptance is withheld. Correct the application or start work with the intended scope."
	return d
}
