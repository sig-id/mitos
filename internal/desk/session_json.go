package desk

import "encoding/json"

// Session is read from historical journals and snapshots, never accepted as an
// API mutation payload. Migrate its stored lineage without exposing old fields.
func (s *Session) UnmarshalJSON(raw []byte) error {
	type stored Session
	var value struct {
		*stored
		PreviousOrchestrator string `json:"supervisor_id"`
		LegacyOrchestrator   string `json:"coordinator_id"`
	}
	value.stored = (*stored)(s)
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if s.Role == "coordinator" {
		s.Role = "orchestrator"
	}
	if s.OrchestratorID == "" {
		s.OrchestratorID = value.LegacyOrchestrator
	}
	if s.OrchestratorID == "" {
		s.OrchestratorID = value.PreviousOrchestrator
	}
	return nil
}

func (r *Responsibility) UnmarshalJSON(raw []byte) error {
	type stored Responsibility
	var value struct {
		*stored
		LegacyOrchestrator string `json:"coordinator_session_id"`
	}
	value.stored = (*stored)(r)
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if r.OrchestratorSessionID == "" {
		r.OrchestratorSessionID = value.LegacyOrchestrator
	}
	return nil
}
