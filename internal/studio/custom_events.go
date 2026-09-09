package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"mitos/internal/service"
)

type CustomEventInput struct {
	Name    string          `json:"name"`
	Key     string          `json:"idempotency_key"`
	Payload json.RawMessage `json:"payload"`
	SHA256  string          `json:"sha256"`
	Bytes   int             `json:"bytes"`
}
type CustomEventRecord struct {
	Input      CustomEventInput        `json:"input"`
	Revision   int64                   `json:"catalog_revision"`
	Recipients []ServiceEventRecipient `json:"recipients"`
}
type CustomEventSaved struct {
	Record CustomEventRecord
	Seq    int64
	Hash   string
	At     time.Time
}
type CustomEventSummary struct {
	Seq      int64     `json:"seq"`
	Hash     string    `json:"hash"`
	At       time.Time `json:"at"`
	Name     string    `json:"name"`
	Key      string    `json:"idempotency_key"`
	Revision int64     `json:"catalog_revision"`
	SHA256   string    `json:"payload_sha256"`
	Bytes    int       `json:"payload_bytes"`
	Pending  int       `json:"pending"`
	Enqueued int       `json:"enqueued"`
	Skipped  int       `json:"skipped"`
	Rejected int       `json:"rejected"`
}
type CustomEventRecipientView struct {
	ServiceID string `json:"service_id"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	RunID     string `json:"run_id,omitempty"`
}
type CustomEventView struct {
	CustomEventSummary
	Payload    json.RawMessage            `json:"payload"`
	Recipients []CustomEventRecipientView `json:"recipients"`
}

func customEventKey(name, key string) string { return name + "\x00" + key }
func validateCustomInput(in *CustomEventInput) error {
	if in == nil || !service.ValidCustomEventName(in.Name) || !validServiceKey(in.Key) {
		return errors.New("custom event requires a nonreserved name and bounded idempotency key")
	}
	normalized, err := NormalizeIngressPayload(in.Payload)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(normalized)
	if !bytes.Equal(normalized, in.Payload) || in.Bytes != len(normalized) || in.SHA256 != fmt.Sprintf("%x", hash) {
		return errors.New("custom event payload evidence changed")
	}
	return nil
}
func validateCustomEvent(e Event, c change) error {
	r := c.CustomEvent
	if r == nil {
		return nil
	}
	if e.RunID != "" || e.Type != "effect" || e.Kind != "custom_event_emitted" || c.Run != nil || c.Task != nil || c.Services != nil || c.ServiceCall != nil || c.ServiceEmission != nil || r.Revision < 0 || r.Revision >= e.Seq {
		return errors.New("invalid custom event Effect")
	}
	if err := validateCustomInput(&r.Input); err != nil {
		return err
	}
	for _, recipient := range r.Recipients {
		if recipient.Revision != r.Revision {
			return errors.New("custom event recipients span catalog revisions")
		}
	}
	return validateEventRecipients(e, &ServiceEmission{Names: []string{r.Input.Name}, Recipients: r.Recipients})
}
func (s *Store) applyCustomEvent(e Event, c change) error {
	if err := validateCustomEvent(e, c); err != nil {
		return err
	}
	r := c.CustomEvent
	key := customEventKey(r.Input.Name, r.Input.Key)
	if s.customEvents == nil {
		s.customEvents = map[string]CustomEventSaved{}
	}
	if _, exists := s.customEvents[key]; exists {
		return errors.New("duplicate custom event receipt")
	}
	s.customEvents[key] = CustomEventSaved{Record: *r, Seq: e.Seq, Hash: e.Hash, At: e.At}
	cause := ServiceEventCause{Seq: e.Seq, Hash: e.Hash, Custom: &r.Input, Names: []string{r.Input.Name}, RootSeq: e.Seq, RootHash: e.Hash, Depth: 1}
	return s.applyEventRecipients(e, cause, r.Recipients)
}
func (s *Store) customEventView(saved CustomEventSaved) CustomEventView {
	r := saved.Record
	view := CustomEventView{CustomEventSummary: CustomEventSummary{Seq: saved.Seq, Hash: saved.Hash, At: saved.At, Name: r.Input.Name, Key: r.Input.Key, Revision: r.Revision, SHA256: r.Input.SHA256, Bytes: r.Input.Bytes}, Payload: append(json.RawMessage(nil), r.Input.Payload...), Recipients: []CustomEventRecipientView{}}
	for _, recipient := range r.Recipients {
		d := s.serviceEventDeliveries[fmt.Sprintf("%d:%s", saved.Seq, recipient.ServiceID)]
		entry := CustomEventRecipientView{ServiceID: recipient.ServiceID, Status: recipient.Status, Detail: recipient.Detail}
		if d != nil {
			entry.Status = d.Recipient.Status
			entry.Detail = d.Recipient.Detail
			entry.RunID = d.RunID
		}
		switch entry.Status {
		case "pending":
			view.Pending++
		case "enqueued":
			view.Enqueued++
		case "rejected":
			view.Rejected++
		default:
			view.Skipped++
		}
		view.Recipients = append(view.Recipients, entry)
	}
	return view
}
func (s *Store) CustomEvents() []CustomEventSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	saved := []CustomEventSaved{}
	for _, entry := range s.customEvents {
		saved = append(saved, entry)
	}
	sort.Slice(saved, func(i, j int) bool { return saved[i].Seq > saved[j].Seq })
	if len(saved) > 50 {
		saved = saved[:50]
	}
	out := []CustomEventSummary{}
	for _, entry := range saved {
		out = append(out, s.customEventView(entry).CustomEventSummary)
	}
	return out
}
func (s *Store) CustomEvent(seq int64) (CustomEventView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, entry := range s.customEvents {
		if entry.Seq == seq {
			return s.customEventView(entry), nil
		}
	}
	return CustomEventView{}, ErrNotFound
}
func (e *Engine) EmitServiceEvent(name, key string, payload []byte) (CustomEventView, error) {
	normalized, err := NormalizeIngressPayload(payload)
	if err != nil {
		return CustomEventView{}, err
	}
	hash := sha256.Sum256(normalized)
	in := CustomEventInput{Name: name, Key: key, Payload: normalized, Bytes: len(normalized), SHA256: fmt.Sprintf("%x", hash)}
	if err = validateCustomInput(&in); err != nil {
		return CustomEventView{}, err
	}
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	if saved, exists := e.Store.customEvents[customEventKey(name, key)]; exists {
		if saved.Record.Input.SHA256 != in.SHA256 || !bytes.Equal(saved.Record.Input.Payload, in.Payload) {
			return CustomEventView{}, fmt.Errorf("%w: event key already has different content", ErrConflict)
		}
		return e.Store.customEventView(saved), nil
	}
	r := &CustomEventRecord{Input: in}
	if e.Store.services != nil {
		r.Revision = e.Store.services.Revision
	}
	r.Recipients = e.Store.allocateEventRecipients([]string{name}, 0, 0, "", "", change{}, time.Now().UTC())
	if err = e.Store.commit("", "effect", "custom_event_emitted", change{CustomEvent: r}); err != nil {
		return CustomEventView{}, err
	}
	return e.Store.customEventView(e.Store.customEvents[customEventKey(name, key)]), nil
}
