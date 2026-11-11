package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"mitos/internal/desk"
	"mitos/internal/messaging"
)

type messageChange struct {
	Outbound *messaging.Outbound `json:"outbound,omitempty"`
	Inbound  *messaging.Inbound  `json:"inbound,omitempty"`
}

func (s *Store) applyMessage(e Event, c *messageChange) error {
	if e.RunID != "" || e.Type != "effect" {
		return errors.New("invalid messaging journal event")
	}
	if s.messagesOut == nil {
		s.messagesOut = map[string]messaging.Outbound{}
	}
	if s.messagesIn == nil {
		s.messagesIn = map[string]messaging.Inbound{}
	}
	if c.Outbound != nil {
		s.messagesOut[c.Outbound.Message.ID] = *c.Outbound
	}
	if c.Inbound != nil {
		s.messagesIn[c.Inbound.Message.Key()] = *c.Inbound
	}
	return nil
}
func copyMessage[T any](v T) T {
	b, _ := json.Marshal(v)
	var cp T
	_ = json.Unmarshal(b, &cp)
	return cp
}
func (s *Store) QueueMessage(m messaging.Envelope) (messaging.Outbound, error) {
	if err := m.Validate(); err != nil {
		return messaging.Outbound{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.From != s.home.ID || m.Kind == "receipt" {
		return messaging.Outbound{}, errors.New("invalid outbound origin or kind")
	}
	if old, ok := s.messagesOut[m.ID]; ok {
		if old.Message.Fingerprint() != m.Fingerprint() {
			return copyMessage(old), messaging.ErrConflict
		}
		return copyMessage(old), nil
	}
	if len(s.messagesOut) >= messaging.MaxRecords {
		return messaging.Outbound{}, messaging.ErrCapacity
	}
	if !m.Deadline.After(time.Now()) || m.Deadline.After(time.Now().Add(7*24*time.Hour)) {
		return messaging.Outbound{}, errors.New("deadline must be in the next seven days")
	}
	v := messaging.Outbound{Message: m, Status: "pending", NextAttempt: time.Now().UTC()}
	err := s.commit("", "effect", "message_queued", change{Message: &messageChange{Outbound: &v}})
	return copyMessage(v), err
}
func (s *Store) OutboundMessage(id string) (messaging.Outbound, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.messagesOut[id]
	if !ok {
		return v, ErrNotFound
	}
	return copyMessage(v), nil
}
func (s *Store) PendingMessages(now time.Time, limit int) []messaging.Outbound {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []messaging.Outbound{}
	if limit <= 0 {
		return out
	}
	type candidate struct {
		id  string
		due time.Time
	}
	var candidates []candidate
	for _, v := range s.messagesOut {
		if v.Status == "pending" && v.Message.Deadline.After(now) && !v.NextAttempt.After(now) {
			candidates = append(candidates, candidate{id: v.Message.ID, due: v.NextAttempt})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].due.Equal(candidates[j].due) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].due.Before(candidates[j].due)
	})
	// Only the selected batch owns payload copies; the backlog contains metadata.
	for _, selected := range candidates[:min(limit, len(candidates))] {
		out = append(out, copyMessage(s.messagesOut[selected.id]))
	}
	return out
}

// ExpireMessages never waits for a network slot or a future retry time. A bounded
// batch limits journal writes, and terminal records leave subsequent selections.
func (s *Store) ExpireMessages(now time.Time, limit int) error {
	if limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	type candidate struct {
		id       string
		deadline time.Time
	}
	var candidates []candidate
	for id, v := range s.messagesOut {
		if v.Status == "pending" && !v.Message.Deadline.After(now) {
			candidates = append(candidates, candidate{id: id, deadline: v.Message.Deadline})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].deadline.Equal(candidates[j].deadline) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].deadline.Before(candidates[j].deadline)
	})
	for _, selected := range candidates[:min(limit, len(candidates))] {
		v := s.messagesOut[selected.id]
		v.Status = "expired"
		if v.Attempts > 0 {
			v.Status = "unknown"
		}
		if err := s.commit("", "effect", "message_expired", change{Message: &messageChange{Outbound: &v}}); err != nil {
			return err
		}
	}
	return nil
}

// Reserve the attempt before networking. A crash must not reset retry accounting.
func (s *Store) AttemptMessage(id string, now time.Time, max int) (messaging.Outbound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.messagesOut[id]
	if !ok {
		return v, ErrNotFound
	}
	if v.Status != "pending" || v.NextAttempt.After(now) {
		return copyMessage(v), nil
	}
	if !v.Message.Deadline.After(now) || v.Attempts >= max {
		v.Status = "unknown"
		if v.Attempts == 0 {
			v.Status = "expired"
		}
	} else {
		v.Attempts++
		v.NextAttempt = now.Add(time.Second * time.Duration(1<<min(v.Attempts, 6)))
	}
	err := s.commit("", "effect", "message_attempted", change{Message: &messageChange{Outbound: &v}})
	return copyMessage(v), err
}
func (s *Store) AcceptReceipt(m messaging.Envelope) error {
	var r messaging.Receipt
	if err := messaging.Decode(m.Payload, &r); err != nil {
		return err
	}
	if r.Status != "accepted" && r.Status != "applied" && r.Status != "rejected" && r.Status != "expired" {
		return errors.New("invalid application receipt status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.messagesOut[r.ID]
	if !ok {
		return ErrNotFound
	}
	orig := v.Message
	if m.Kind != "receipt" || m.Type != "application" || m.To != s.home.ID || m.From != orig.To || m.Project != orig.Project || m.Mailbox != orig.Mailbox || m.Correlation != orig.ID || r.Digest != orig.Fingerprint() {
		return messaging.ErrConflict
	}
	if v.Receipt != nil {
		if *v.Receipt != r {
			return messaging.ErrConflict
		}
		return nil
	}
	v.Status = r.Status
	v.Receipt = &r
	return s.commit("", "effect", "message_receipted", change{Message: &messageChange{Outbound: &v}})
}

// AcceptMessage is called only after signature and exact peer-grant verification.
// The domain effect and receipt share one fsynced journal record.
func (s *Store) AcceptMessage(m messaging.Envelope) (messaging.Receipt, error) {
	if err := m.Validate(); err != nil {
		return messaging.Receipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.To != s.home.ID || m.Kind == "receipt" {
		return messaging.Receipt{}, errors.New("wrong destination or message class")
	}
	if old, ok := s.messagesIn[m.Key()]; ok {
		if old.Message.Fingerprint() != m.Fingerprint() {
			return messaging.Receipt{}, messaging.ErrConflict
		}
		return old.Receipt, nil
	}
	if len(s.messagesIn) >= messaging.MaxRecords {
		return messaging.Receipt{}, messaging.ErrCapacity
	}
	receipt := messaging.Receipt{ID: m.ID, Digest: m.Fingerprint(), Status: "accepted"}
	c := change{}
	kind := "message_received"
	if !m.Deadline.After(time.Now()) {
		receipt.Status = "expired"
		receipt.Detail = "message deadline elapsed before admission"
	} else if (m.Kind == "command" || m.Kind == "event") && m.Type == "service.event" && m.Project == "studio" && m.Mailbox == "events" && m.Generation == 0 {
		var input struct {
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
		}
		err := messaging.Decode(m.Payload, &input)
		var normalized []byte
		if err == nil {
			normalized, err = NormalizeIngressPayload(input.Payload)
		}
		in := CustomEventInput{Name: input.Name, Key: "mqtt-" + messaging.Hash([]byte(m.Key())), Payload: normalized, Bytes: len(normalized), SHA256: messaging.Hash(normalized)}
		if err == nil {
			err = validateCustomInput(&in)
		}
		if err != nil {
			receipt.Status = "rejected"
			receipt.Detail = "invalid service event payload"
		} else if _, exists := s.customEvents[customEventKey(in.Name, in.Key)]; exists {
			receipt.Status = "rejected"
			receipt.Detail = "service event identity already belongs to another admission"
		} else {
			record := &CustomEventRecord{Input: in}
			if s.services != nil {
				record.Revision = s.services.Revision
			}
			record.Recipients = s.allocateEventRecipients([]string{in.Name}, 0, 0, "", "", change{}, time.Now().UTC())
			c.CustomEvent = record
			kind = "custom_event_emitted"
			receipt.Status = "applied"
		}
	} else if m.Kind == "note" && m.Type == "text" {
		var note struct {
			Body string `json:"body"`
		}
		if err := messaging.Decode(m.Payload, &note); err != nil || len(note.Body) == 0 || len(note.Body) > 8000 {
			receipt.Status = "rejected"
			receipt.Detail = "note requires 1–8000 bytes of text"
		} else if err := s.messageGeneration(m.Project, m.Mailbox, m.Generation); err != nil {
			receipt.Status = "rejected"
			receipt.Detail = err.Error()
		}
	} else {
		receipt.Status = "rejected"
		receipt.Detail = "unsupported message operation"
	}
	incoming := messaging.Inbound{Message: m, Receipt: receipt, Sequence: s.seq + 1}
	c.Message = &messageChange{Inbound: &incoming}
	if err := s.commit("", "effect", kind, c); err != nil {
		return messaging.Receipt{}, err
	}
	return receipt, nil
}
func (s *Store) messageGeneration(project, mailbox string, generation int) error {
	if project == "studio" && mailbox == "operator" && generation == 0 {
		return nil
	}
	var p struct {
		Responsibilities []desk.Responsibility `json:"responsibilities"`
	}
	if err := json.Unmarshal(s.deskState, &p); err != nil {
		return errors.New("responsibility unavailable")
	}
	for _, r := range p.Responsibilities {
		if r.ProjectID == project && r.ID == mailbox {
			if generation > 0 && r.Generation == generation {
				return nil
			}
			return errors.New("stale coordinator generation")
		}
	}
	return errors.New("unknown responsibility mailbox")
}
func (s *Store) MessageInbox(project, mailbox string, after int64) []messaging.Inbound {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []messaging.Inbound{}
	for _, v := range s.messagesIn {
		if v.Message.Project == project && v.Message.Mailbox == mailbox && v.Sequence > after {
			out = append(out, copyMessage(v))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}
func (s *Store) AcknowledgeMessage(sender, id string, generation int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.messagesIn[sender+"/"+id]
	if !ok {
		return ErrNotFound
	}
	if v.Message.Kind != "note" || v.Receipt.Status != "accepted" {
		return fmt.Errorf("%w: only admitted notes can be acknowledged", ErrConflict)
	}
	if err := s.messageGeneration(v.Message.Project, v.Message.Mailbox, generation); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if v.Acknowledged {
		return nil
	}
	v.Acknowledged = true
	v.AckGeneration = generation
	return s.commit("", "effect", "message_acknowledged", change{Message: &messageChange{Inbound: &v}})
}
