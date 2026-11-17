package studio

import (
	"errors"

	"mitos/internal/decision"
)

type taskContextVersion struct {
	Value uint64 `json:"value"`
	Seq   int64  `json:"seq"`
}

var ErrInputsNotConsumed = errors.New("inputs_not_consumed")
var ErrSuperseded = errors.New("superseded_context")
var ErrVersionConflict = errors.New("version_conflict")

type VersionAdvance struct {
	Name     string `json:"name"`
	Expected uint64 `json:"expected"`
	Value    uint64 `json:"value"`
}

func validConsumedVersions(values map[string]uint64) bool {
	if len(values) > 32 {
		return false
	}
	for key := range values {
		if !validGraphKey(key) {
			return false
		}
	}
	return true
}
func (s *Store) versionsCurrentLocked(tenant decision.Tenant, values map[string]uint64) bool {
	for key, value := range values {
		if s.tenantVersions[scopedRequestKey(tenant, key)] != value {
			return false
		}
	}
	return true
}
func (s *Store) validateVersionAdvanceLocked(tenant decision.Tenant, changes []VersionAdvance) error {
	if len(changes) > 32 {
		return ErrConflict
	}
	seen := map[string]bool{}
	for _, v := range changes {
		if !validGraphKey(v.Name) || seen[v.Name] || v.Value <= v.Expected {
			return ErrConflict
		}
		seen[v.Name] = true
		if s.tenantVersions[scopedRequestKey(tenant, v.Name)] != v.Expected {
			return ErrVersionConflict
		}
	}
	return nil
}
func (s *Store) serializationAvailableLocked(r *Run, active []string) bool {
	if r.Brief.Structured == nil || r.Brief.Structured.SerializationKey == "" {
		return true
	}
	for _, id := range active {
		other := s.runs[id]
		if other != nil && other.ID != r.ID && (other.Status == "running" || other.Status == "stopping") && other.Brief.Structured != nil && runTenant(other) == runTenant(r) && other.Brief.Structured.SerializationKey == r.Brief.Structured.SerializationKey {
			return false
		}
	}
	return true
}
func (s *Store) taskHasActionHoldLocked(task string) bool {
	for _, id := range s.actionHolds {
		if s.externalOperations[id].TaskID == task {
			return true
		}
	}
	return false
}
