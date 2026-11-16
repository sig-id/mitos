package studio

import (
	"encoding/json"
	"errors"
	"mitos/internal/cooperation"
	"sort"
	"time"
)

type CooperationRecord struct {
	Seq       int64               `json:"seq"`
	State     string              `json:"state,omitempty"`
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
	if task.Revision != m.Revision || m.RunID != "" && task.CurrentRunID != m.RunID {
		return CooperationRecord{}, ErrConflict
	}
	for _, id := range m.Artifacts {
		found := false
		for _, producer := range s.runs {
			if producer.ProjectID != project {
				continue
			}
			for _, artifact := range producer.Artifacts {
				if artifactID(producer.ID, artifact) != id || !s.handoffArtifactAllowed(m.RunID, producer, artifact) {
					continue
				}
				f, err := s.verifiedArtifact(producer, artifact)
				if err != nil {
					return CooperationRecord{}, err
				}
				f.Close()
				found = true
			}
		}
		if !found {
			return CooperationRecord{}, errors.New("artifact reference unavailable to origin")
		}
	}
	for _, resolver := range m.PermittedResolvers {
		target := s.runs[resolver.RunID]
		targetTask := s.tasks[resolver.TaskID]
		if target == nil || targetTask == nil || target.ProjectID != project || target.TaskID != resolver.TaskID || targetTask.Revision != resolver.Revision || targetTask.CurrentRunID != resolver.RunID || target.Status != "running" {
			return CooperationRecord{}, ErrConflict
		}
		origin := s.runs[m.RunID]
		if origin == nil || target.ID != origin.ID && (origin.GraphAttempt == nil || target.GraphAttempt == nil || origin.GraphAttempt.GraphID != target.GraphAttempt.GraphID) {
			return CooperationRecord{}, errors.New("cross-task resolver requires the same explicit graph scope")
		}
	}
	for _, blocked := range m.BlockedActions {
		if blocked.TaskID != m.TaskID || blocked.RunID != m.RunID || blocked.Revision != m.Revision {
			return CooperationRecord{}, errors.New("only the originating attempt's acceptance may be blocked")
		}
	}
	if m.Kind == "approval" {
		found := false
		for _, run := range s.runs {
			if run.ProjectID == project && run.Decision != nil && run.Decision.ID == m.DecisionID {
				found = true
				break
			}
		}
		if !found {
			return CooperationRecord{}, errors.New("canonical decision unavailable")
		}
	}
	if m.InReplyTo != "" {
		original, ok := s.cooperation[project+"/"+m.InReplyTo]
		if !ok || !original.Message.Request() || s.cooperationState(original, now) != "pending" {
			return CooperationRecord{}, ErrConflict
		}
		allowed := false
		if m.Kind == "cancel" {
			allowed = original.Message.TaskID == m.TaskID && original.Message.RunID == m.RunID && original.Message.SessionID == m.SessionID && original.Message.Revision == m.Revision
		} else {
			for _, resolver := range original.Message.PermittedResolvers {
				if resolver.TaskID == m.TaskID && resolver.RunID == m.RunID && resolver.Revision == m.Revision {
					allowed = true
				}
			}
		}
		if !allowed {
			return CooperationRecord{}, errors.New("resolver is not permitted")
		}
	}
	record := CooperationRecord{ProjectID: project, Message: m, Digest: digest, At: now, Seq: s.seq + 1}
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
			copy := graphClone(r)
			copy.State = s.cooperationState(r, time.Now().UTC())
			out = append(out, copy)
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

// Request state is projected from immutable request/settlement events and exact
// current attempt identity. No request is automatically adopted by a retry.
// Historical versions remain inspectable but cannot participate in v2 settlement.
func (s *Store) cooperationState(record CooperationRecord, now time.Time) string {
	m := record.Message
	if !m.Request() {
		return "recorded"
	}
	if m.Version != cooperation.Version {
		return "unsupported_version"
	}
	for _, reply := range s.cooperation {
		if reply.ProjectID == record.ProjectID && reply.Message.InReplyTo == m.ID {
			if reply.Message.Kind == "answer" {
				return "answered"
			}
			if reply.Message.Kind == "cancel" {
				return "cancelled"
			}
		}
	}
	if m.ExpiresAt != nil && !now.Before(*m.ExpiresAt) {
		return "expired"
	}
	t := s.tasks[m.TaskID]
	r := s.runs[m.RunID]
	if t == nil || t.Revision != m.Revision || r == nil || t.CurrentRunID != m.RunID || r.Status != "running" {
		return "superseded"
	}
	if r.GraphAttempt != nil {
		g := s.graphs[r.GraphAttempt.GraphID]
		if g == nil {
			return "superseded"
		}
		member := g.Members[r.TaskID]
		if member == nil || member.CancelRequested || member.Replacement != "" {
			return "superseded"
		}
	}
	return "pending"
}
func (s *Store) cooperationBlocks(run string) bool {
	for _, record := range s.cooperation {
		for _, blocked := range record.Message.BlockedActions {
			if blocked.RunID == run && s.cooperationState(record, time.Now().UTC()) != "answered" {
				return true
			}
		}
	}
	return false
}

type CooperationPage struct {
	Messages []CooperationRecord `json:"messages"`
	Next     *int64              `json:"next_cursor"`
	Version  string              `json:"version"`
}

func (s *Store) CooperationPage(project, run string, cursor int64, limit int) (CooperationPage, error) {
	if cursor < 0 || limit < 1 || limit > 16 {
		return CooperationPage{}, handoffError("invalid_page", cursor)
	}
	records := s.Cooperation(project)
	sort.Slice(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })
	out := CooperationPage{Messages: []CooperationRecord{}, Version: cooperation.Version}
	next := cursor
	for _, record := range records {
		if record.Seq <= cursor {
			continue
		}
		if run != "" {
			visible := record.Message.RunID == run
			for _, resolver := range record.Message.PermittedResolvers {
				if resolver.RunID == run {
					visible = true
				}
			}
			if !visible {
				next = record.Seq
				continue
			}
		}
		out.Messages = append(out.Messages, record)
		body, _ := json.Marshal(out)
		if len(out.Messages) > limit || len(body) > 48<<10 {
			out.Messages = out.Messages[:len(out.Messages)-1]
			out.Next = &next
			break
		}
		next = record.Seq
	}
	return out, nil
}

// Cooperation notices are informational and deliberately separate from native
// decision cards. They expose no approve/resolve operation to the notification UI.
type CooperationNotice struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	GraphID   string `json:"graph_id"`
	TaskID    string `json:"task_id"`
	RunID     string `json:"run_id"`
	Body      string `json:"body"`
	Blocking  bool   `json:"blocking"`
}

func (s *Store) CooperationNotices() []CooperationNotice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []CooperationNotice{}
	now := time.Now().UTC()
	records := []CooperationRecord{}
	for _, record := range s.cooperation {
		if s.cooperationState(record, now) == "pending" {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Seq > records[j].Seq })
	for _, record := range records {
		if len(out) == 16 {
			break
		}
		run := s.runs[record.Message.RunID]
		if run == nil || run.GraphAttempt == nil {
			continue
		}
		body := []rune(record.Message.Body)
		if len(body) > 500 {
			body = append(body[:500], []rune("…")...)
		}
		out = append(out, CooperationNotice{ID: record.ProjectID + "/" + record.Message.ID, ProjectID: record.ProjectID, GraphID: run.GraphAttempt.GraphID, TaskID: run.TaskID, RunID: run.ID, Body: string(body), Blocking: len(record.Message.BlockedActions) > 0})
	}
	return out
}
