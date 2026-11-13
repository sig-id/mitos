package studio

import (
	"encoding/json"
	"errors"
	"mitos/internal/cooperation"
	"sort"
	"time"
)

type CooperationRecord struct {
	ProjectID string              `json:"project_id"`
	Message   cooperation.Message `json:"message"`
	Digest    string              `json:"digest"`
	At        time.Time           `json:"at"`
}

func (e *Engine) SaveCooperation(project, hash, text string) (cooperation.Document, error) {
	p, err := e.Desk.Project(project)
	if err != nil {
		return cooperation.Document{}, err
	}
	e.setupMu.Lock()
	defer e.setupMu.Unlock()
	return cooperation.Save(p.Directory, hash, text)
}
func (e *Engine) SubmitCooperation(project string, m cooperation.Message) (CooperationRecord, error) {
	if _, err := e.Desk.Project(project); err != nil {
		return CooperationRecord{}, err
	}
	s := e.Store
	digest := graphDigest(m)
	key := project + "/" + m.ID
	s.mu.RLock()
	old, exists := s.cooperation[key]
	s.mu.RUnlock()
	if exists {
		if old.Digest != digest {
			return CooperationRecord{}, ErrConflict
		}
		return graphClone(old), nil
	}
	if m.SessionID != "" {
		session, err := e.Desk.Session(m.SessionID)
		if err != nil || session.ProjectID != project || session.TaskID != m.TaskID || session.Status != "running" {
			return CooperationRecord{}, ErrConflict
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.cooperation[key]; ok {
		if old.Digest != digest {
			return CooperationRecord{}, ErrConflict
		}
		return graphClone(old), nil
	}
	if len(s.cooperation) >= 16384 {
		return CooperationRecord{}, errors.New("cooperation journal message limit reached")
	}
	now := time.Now().UTC()
	if err := m.Validate(now); err != nil {
		return CooperationRecord{}, err
	}
	if m.RunID != "" {
		run := s.runs[m.RunID]
		if run == nil || run.ProjectID != project || run.TaskID != m.TaskID || run.Status != "running" {
			return CooperationRecord{}, ErrConflict
		}
	}
	task := s.tasks[m.TaskID]
	if task == nil || task.ProjectID != project {
		return CooperationRecord{}, ErrConflict
	}
	// Revision is the durable task revision, never an agent-invented generation.
	if task.Revision != m.Revision {
		return CooperationRecord{}, ErrConflict
	}
	if m.InReplyTo != "" {
		original, ok := s.cooperation[project+"/"+m.InReplyTo]
		if !ok || !original.Message.Request() || original.Message.TaskID != m.TaskID || original.Message.Revision != m.Revision || original.Message.ExpiresAt != nil && !now.Before(*original.Message.ExpiresAt) {
			return CooperationRecord{}, errors.New("request is unavailable or stale")
		}
		for _, old := range s.cooperation {
			if old.ProjectID == project && old.Message.InReplyTo == m.InReplyTo {
				return CooperationRecord{}, ErrConflict
			}
		}
	}
	record := CooperationRecord{ProjectID: project, Message: m, Digest: digest, At: now}
	if err := s.commit("", "effect", "cooperation_message_recorded", change{Cooperation: &record}); err != nil {
		return CooperationRecord{}, err
	}
	return record, nil
}
func (s *Store) Cooperation(project string) []CooperationRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []CooperationRecord{}
	for _, r := range s.cooperation {
		if r.ProjectID == project {
			out = append(out, graphClone(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Keep the newest complete records within both limits. A single oversized record
// becomes a marked excerpt; its complete evidence remains in the durable journal.
func cooperationEvidence(messages []CooperationRecord) []byte {
	if len(messages) > 16 {
		messages = messages[len(messages)-16:]
	}
	for {
		encoded, _ := json.Marshal(messages)
		if len(encoded) <= 32<<10 {
			return encoded
		}
		if len(messages) > 1 {
			messages = messages[1:]
			continue
		}
		latest := graphClone(messages[0])
		body := []rune(latest.Message.Body)
		if len(body) > 1024 {
			body = body[:1024]
		}
		latest.Message.Body = string(body) + "\n[Evidence excerpt: full message and artifact references remain in the cooperation journal.]"
		latest.Message.Artifacts = nil
		encoded, _ = json.Marshal([]CooperationRecord{latest})
		return encoded
	}
}
