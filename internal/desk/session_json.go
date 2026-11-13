package desk

import "encoding/json"

// Session is read from historical journals and snapshots, never accepted as an
// API mutation payload. Migrate its stored lineage without exposing old fields.
func (s *Session) UnmarshalJSON(raw []byte) error {
	type stored Session
	var value struct {
		*stored
		PreviousCoordinator string `json:"supervisor_id"`
	}
	value.stored = (*stored)(s)
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if s.CoordinatorID == "" {
		s.CoordinatorID = value.PreviousCoordinator
	}
	return nil
}
