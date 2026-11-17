package studio

import "time"

func (e *Engine) reconcileWorkerLeases() error {
	s := e.Store
	s.mu.Lock()
	for id, l := range s.workerLeases {
		if l.Closed {
			continue
		}
		r := s.runs[id]
		p := s.workers[l.ID]
		reg := s.workerRegistrations[l.ID]
		valid := r != nil && r.Status == "running" && !p.Revoked && p.Epoch == s.authorityEpoch && time.Now().Before(p.ExpiresAt) && reg.Incarnation == l.Incarnation && time.Now().Before(l.ExpiresAt) && time.Now().Before(l.BudgetEndsAt)
		if valid {
			continue
		}
		l.Closed = true
		c := change{Worker: &workerChange{Lease: &l}}
		if r != nil && !r.Terminal() && r.Status != "stopping" {
			pending := true
			c.Status = "failed"
			c.Stage = "Worker binding ended"
			c.Summary = "The prior worker grant is fenced; a bounded replacement retains the Task."
			c.StructuredRetryPending = &pending
		}
		if err := s.commit(id, "effect", "worker_binding_ended", c); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	pending := []Run{}
	for _, r := range s.runs {
		if r.RestartRetry && r.Brief.Structured != nil {
			pending = append(pending, boundedRun(r))
		}
	}
	s.mu.Unlock()
	for _, r := range pending {
		if err := e.recoverStructured(r); err != nil {
			return err
		}
	}
	return nil
}
