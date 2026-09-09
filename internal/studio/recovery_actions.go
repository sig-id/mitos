package studio

import (
	"errors"
	"time"

	"mitos/internal/autonomy"
	"mitos/internal/decision"
)

func (e *Engine) Rewind(id string, request autonomy.Rewind) (autonomy.Rewind, error) {
	return e.RewindAs(id, request, "api", decision.LocalOperator())
}
func (e *Engine) RewindAs(id string, request autonomy.Rewind, via string, principal decision.Principal) (autonomy.Rewind, error) {
	e.actions.Lock()
	defer e.actions.Unlock()
	r, err := e.Store.Get(id)
	if err != nil {
		return autonomy.Rewind{}, err
	}
	if !principal.Authenticated || principal.ID == "" || principal.Tenant != runTenant(&r) {
		decisionID := ""
		if r.Decision != nil {
			decisionID = r.Decision.ID
		}
		return autonomy.Rewind{}, e.Store.ResolveChoice(id, decision.Resolution{DecisionID: decisionID, Choice: "rewind", Via: via}, principal, &request)
	}
	if (r.PendingRewind != nil || r.ReadyRewind != nil) && !(r.Status == "parked" && r.Decision != nil && r.Decision.Choice == "") {
		if r.Claim != nil && r.Claim.ReleasedAt == nil {
			return autonomy.Rewind{}, errors.New("inspect and release the previous execution's claims before recovery")
		}
		if r.Decision != nil && r.Decision.Resolution != nil {
			if err = e.Store.ResolveChoice(id, decision.Resolution{DecisionID: r.Decision.ID, Choice: "rewind", Via: via}, principal, &request); err != nil {
				return autonomy.Rewind{}, err
			}
		}
		return e.Store.BeginRewind(id, request)
	}
	if r.Status != "parked" || r.Execution == nil {
		return autonomy.Rewind{}, ErrConflict
	}
	if !time.Now().Before(r.Execution.Deadline) {
		return autonomy.Rewind{}, errors.New("the original execution deadline has expired; start a new attempt")
	}
	offered := false
	if r.Decision != nil && r.Decision.Choice == "" {
		for _, option := range r.Decision.Options {
			if option.ID == "rewind" {
				offered = true
			}
		}
	}
	if !offered {
		return autonomy.Rewind{}, errors.New("checkpoint recovery is not available for this decision")
	}
	if _, err = e.resolveAs(id, r.Decision.ID, "rewind", "", via, principal, &request); err != nil {
		return autonomy.Rewind{}, err
	}
	current, err := e.Store.Get(id)
	if err != nil {
		return autonomy.Rewind{}, err
	}
	if len(current.Rewinds) == 0 {
		return autonomy.Rewind{}, ErrConflict
	}
	return current.Rewinds[len(current.Rewinds)-1], nil
}
