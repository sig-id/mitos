package autonomy

import "time"

// Admission is supplied by trusted provider/harness/verifier contract checks.
// Measured run duration and an agent's narrative cannot satisfy these inputs.
type Admission struct {
	StructuredObservation bool `json:"structured_observation"`
	ObservationContract   bool `json:"observation_contract"`
	MachinePredicate      bool `json:"machine_predicate"`
	OracleIsolated        bool `json:"oracle_isolated"`
	WorkspaceConfined     bool `json:"workspace_confined"`
}
type HorizonGrant struct {
	AssessedAt      time.Time `json:"assessed_at"`
	UnattendedUntil time.Time `json:"unattended_until"`
	DurationMS      int64     `json:"duration_ms"`
	Admission       Admission `json:"admission"`
	Reasons         []string  `json:"reasons"`
}

func GrantHorizon(at, requested, deadline time.Time, a Admission) HorizonGrant {
	grant := HorizonGrant{AssessedAt: at, UnattendedUntil: at, Admission: a, Reasons: []string{}}
	for _, condition := range []struct {
		ok     bool
		reason string
	}{{a.StructuredObservation, "structured_observation_missing"}, {a.ObservationContract, "observation_contract_unproven"}, {a.MachinePredicate, "machine_predicate_missing"}, {a.OracleIsolated, "oracle_isolation_unproven"}, {a.WorkspaceConfined, "workspace_confinement_unproven"}} {
		if !condition.ok {
			grant.Reasons = append(grant.Reasons, condition.reason)
		}
	}
	if at.IsZero() || !requested.After(at) || !deadline.After(at) {
		grant.Reasons = append(grant.Reasons, "horizon_or_deadline_expired")
	}
	if len(grant.Reasons) > 0 {
		return grant
	}
	if requested.After(deadline) {
		requested = deadline
	}
	grant.UnattendedUntil = requested
	grant.DurationMS = requested.Sub(at).Milliseconds()
	return grant
}
