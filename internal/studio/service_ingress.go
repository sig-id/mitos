package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"time"
	"unicode/utf8"

	"mitos/internal/service"
)

const MaxIngressBytes = 64 << 10

type ServiceIngress struct {
	Kind       string          `json:"kind"`
	Source     string          `json:"source"`
	DeliveryID string          `json:"delivery_id"`
	Cycle      string          `json:"cycle,omitempty"`
	ReceivedAt time.Time       `json:"received_at"`
	Payload    json.RawMessage `json:"payload"`
	SHA256     string          `json:"sha256"`
	Bytes      int             `json:"bytes"`
}

// NormalizeIngressPayload preserves numeric tokens, sorts object keys, and
// rejects duplicate keys, invalid UTF-8, excessive depth/size, and extra values.
func NormalizeIngressPayload(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxIngressBytes || !utf8.Valid(data) {
		return nil, errors.New("ingress requires UTF-8 JSON of at most 64 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	nodes := 0
	var read func(int) (any, error)
	read = func(depth int) (any, error) {
		nodes++
		if depth > 32 || nodes > 10000 {
			return nil, errors.New("ingress JSON exceeds depth or value limits")
		}
		token, err := d.Token()
		if err != nil {
			return nil, err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return token, nil
		}
		switch delim {
		case '{':
			value := map[string]any{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New("invalid JSON object key")
				}
				if _, exists := value[key]; exists {
					return nil, errors.New("duplicate JSON object key")
				}
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				value[key] = child
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, errors.New("unterminated JSON object")
			}
			return value, nil
		case '[':
			value := []any{}
			for d.More() {
				child, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				value = append(value, child)
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, errors.New("unterminated JSON array")
			}
			return value, nil
		default:
			return nil, errors.New("unexpected JSON delimiter")
		}
	}
	value, err := read(0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errors.New("send one JSON value")
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(normalized) > MaxIngressBytes {
		return nil, errors.New("normalized ingress payload exceeds 64 KiB")
	}
	return normalized, nil
}
func newServiceIngress(kind, source, key, cycle string, payload []byte) (*ServiceIngress, error) {
	if kind != "webhook" && kind != "external_signal" || !validServiceKey(key) || cycle != "" && !validServiceKey(cycle) {
		return nil, errors.New("delivery requires a supported kind and bounded delivery/cycle keys")
	}
	if kind == "webhook" && !service.ValidWebhookPath(source) || kind == "external_signal" && !service.ValidID(source) {
		return nil, errors.New("invalid delivery source")
	}
	normalized, err := NormalizeIngressPayload(payload)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(normalized)
	return &ServiceIngress{Kind: kind, Source: source, DeliveryID: key, Cycle: cycle, ReceivedAt: time.Now().UTC(), Payload: normalized, SHA256: fmt.Sprintf("%x", hash), Bytes: len(normalized)}, nil
}
func validateServiceIngress(e Event, c change) error {
	if c.ServiceCall != nil {
		call := c.ServiceCall
		expected := call.Origin == "webhook" || call.Origin == "external_signal"
		if expected != (call.Ingress != nil) {
			return errors.New("ingress provenance does not match trigger origin")
		}
		if call.Ingress != nil && (call.Ingress.Kind != call.Origin || call.Ingress.DeliveryID != call.Key || c.Task == nil || !reflect.DeepEqual(c.Task.ServiceIngress, call.Ingress)) {
			return errors.New("delivery provenance is not atomic with Task creation")
		}
	}
	if c.Task == nil || c.Task.ServiceIngress == nil {
		return nil
	}
	in := c.Task.ServiceIngress
	checked, err := newServiceIngress(in.Kind, in.Source, in.DeliveryID, in.Cycle, in.Payload)
	if err != nil {
		return err
	}
	if in.ReceivedAt.IsZero() || in.ReceivedAt.After(e.At) || !bytes.Equal(checked.Payload, in.Payload) || checked.Bytes != in.Bytes || checked.SHA256 != in.SHA256 {
		return errors.New("invalid saved ingress payload evidence")
	}
	return nil
}
func (s *Store) trackServiceIngress(e Event, c change) error {
	if err := validateServiceIngress(e, c); err != nil {
		return err
	}
	if c.Task == nil {
		return nil
	}
	if prior := s.tasks[c.Task.ID]; prior != nil && !reflect.DeepEqual(prior.ServiceIngress, c.Task.ServiceIngress) {
		return errors.New("Task ingress provenance changed")
	}
	if c.ServiceCall != nil && c.ServiceCall.Ingress != nil && c.ServiceCall.Ingress.Kind == "webhook" {
		if s.webhookReceipts == nil {
			s.webhookReceipts = map[string]string{}
		}
		in := c.ServiceCall.Ingress
		key := in.Source + "\x00" + in.DeliveryID
		if s.webhookReceipts[key] != "" {
			return errors.New("duplicate webhook delivery receipt")
		}
		s.webhookReceipts[key] = e.RunID
	}
	return nil
}
func (s *Store) ingressDuplicate(runID string, in *ServiceIngress) (Run, error) {
	run := s.runs[runID]
	if run == nil {
		return Run{}, ErrNotFound
	}
	task := s.tasks[run.TaskID]
	prior := task.ServiceIngress
	if prior == nil || prior.Kind != in.Kind || prior.Source != in.Source || prior.DeliveryID != in.DeliveryID || prior.Cycle != in.Cycle || prior.SHA256 != in.SHA256 || !bytes.Equal(prior.Payload, in.Payload) {
		return Run{}, fmt.Errorf("%w: delivery key already has different source, cycle, or content", ErrConflict)
	}
	return cloneRun(run), nil
}
func (e *Engine) DeliverSignal(id, source, key, cycle string, payload []byte) (Run, error) {
	if !service.ValidID(id) {
		return Run{}, ErrNotFound
	}
	in, err := newServiceIngress("external_signal", source, key, cycle, payload)
	if err != nil {
		return Run{}, err
	}
	e.actions.Lock()
	defer e.actions.Unlock()
	return e.deliverServiceIngress(id, in)
}
func (e *Engine) DeliverWebhook(path, key, cycle string, payload []byte) (Run, error) {
	in, err := newServiceIngress("webhook", path, key, cycle, payload)
	if err != nil {
		return Run{}, err
	}
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	if prior := e.Store.webhookReceipts[path+"\x00"+key]; prior != "" {
		run, err := e.Store.ingressDuplicate(prior, in)
		e.Store.mu.RUnlock()
		return run, err
	}
	active := activeServiceUnits(e.Store.services)
	e.Store.mu.RUnlock()
	for id, u := range active {
		if u.TriggerKind() == "webhook" && u.Schedule.Path == path {
			return e.deliverServiceIngress(id, in)
		}
	}
	return Run{}, ErrNotFound
}

// Caller holds actions; the Store writer fences configuration changes at commit.
func (e *Engine) deliverServiceIngress(id string, in *ServiceIngress) (Run, error) {
	e.Store.mu.RLock()
	if prior := e.Store.serviceCalls[serviceCallIndex(id, in.DeliveryID, in.Kind)]; prior != "" {
		run, err := e.Store.ingressDuplicate(prior, in)
		e.Store.mu.RUnlock()
		return run, err
	}
	catalog := cloneCatalog(e.Store.services)
	e.Store.mu.RUnlock()
	unit, active := activeServiceUnits(&catalog)[id]
	if !active || unit.TriggerKind() != in.Kind {
		return Run{}, errors.New("service is not active for this delivery kind")
	}
	if in.Kind == "external_signal" && unit.Schedule.Source != in.Source || in.Kind == "webhook" && unit.Schedule.Path != in.Source {
		return Run{}, errors.New("delivery source does not match the configured service")
	}
	if len(unit.DependsOn) > 0 && in.Cycle == "" {
		return Run{}, errors.New("dependent deliveries require an explicit shared cycle key")
	}
	brief, name, err := e.prepareBrief(catalog.Work[id])
	if err != nil {
		return Run{}, err
	}
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	if e.Store.services == nil || e.Store.services.Revision != catalog.Revision {
		return Run{}, ErrConflict
	}
	if e.Store.serviceAborted(id) || !e.Store.serviceBreakerAllows(id, time.Now().UTC()) || !e.Store.serviceCanEnqueue(id) {
		return Run{}, errors.New("service admission is withholding this delivery; retry with the same delivery key")
	}
	call := &ServiceCall{ServiceID: id, Revision: catalog.Revision, Key: in.DeliveryID, Origin: in.Kind, Ingress: in}
	if in.Cycle != "" {
		call.Cycle = e.Store.serviceCycleBinding(id, "ingress:"+in.Cycle, catalog.Revision)
	}
	return e.Store.createLocked(brief, name, "", false, call)
}
