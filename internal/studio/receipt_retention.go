package studio

import (
	"errors"
	"time"
)

var ErrRequestKeyExpired = errors.New("request_key_expired: original request key has expired and cannot admit new work")

// ExpireStructuredKeys retains rejection tombstones indefinitely. Callers can
// discard receipt bodies elsewhere without ever turning an old retry into new
// work. Only settled logical Tasks whose latest terminal attempt completed
// before the cutoff qualify; retries and dormant history retain their root key.
func (s *Store) ExpireStructuredKeys(before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := []string{}
	for _, r := range s.runs {
		if r.ParentID != "" || r.Brief.Structured == nil {
			continue
		}
		terminal := r
		if task := s.tasks[r.TaskID]; task != nil {
			terminal = s.runs[task.CurrentRunID]
		}
		if terminal == nil || !terminal.Terminal() || terminal.RestartRetry || s.taskHasActionHoldLocked(r.TaskID) {
			continue
		}
		completed := terminal.FinishedAt
		if completed.IsZero() {
			completed = terminal.UpdatedAt
		}
		if completed.Before(before) && !s.expiredRequestKeys[scopedRequestKey(runTenant(r), r.Brief.Structured.Key)] {
			keys = append(keys, scopedRequestKey(runTenant(r), r.Brief.Structured.Key))
		}
	}
	if len(keys) == 0 {
		return 0, nil
	}
	if err := s.commit("", "effect", "request_keys_expired", change{ExpiredRequestKeys: keys}); err != nil {
		return 0, err
	}
	return len(keys), nil
}

func (s *Store) requestKeyExpired(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.expiredRequestKeys[legacyOrScopedKey(key)]
}

func (s *Store) AuthorityEpoch() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.authorityEpoch == "" {
		return "0"
	}
	return s.authorityEpoch
}
