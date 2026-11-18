package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

var ErrReconciliationRequired = errors.New("reconciliation_required")

type ExternalOperation struct {
	ID            string          `json:"id"`
	TaskID        string          `json:"task_id"`
	Tenant        decision.Tenant `json:"tenant"`
	Target        ActionTarget    `json:"target"`
	Key           string          `json:"key"`
	Payload       json.RawMessage `json:"payload"`
	PayloadDigest string          `json:"payload_digest"`
	Generation    uint64          `json:"generation"`
	State         string          `json:"state"`
	Result        json.RawMessage `json:"result,omitempty"`
}
type actionChange struct {
	Target    *ActionTarget      `json:"target,omitempty"`
	Operation *ExternalOperation `json:"operation,omitempty"`
}

func actionOperationKey(task, target, key string) string {
	b, _ := json.Marshal([]string{task, target, key})
	return string(b)
}
func actionHoldKey(task, target string) string { return actionOperationKey(task, target, "") }
func (s *Store) applyAction(c *actionChange) {
	if s.actionTargets == nil {
		s.actionTargets = map[string]ActionTarget{}
		s.externalOperations = map[string]ExternalOperation{}
		s.operationKeys = map[string]string{}
		s.actionHolds = map[string]string{}
	}
	if c.Target != nil {
		s.actionTargets[c.Target.ID] = graphClone(*c.Target)
	}
	if c.Operation != nil {
		op := graphClone(*c.Operation)
		s.externalOperations[op.ID] = op
		s.operationKeys[actionOperationKey(op.TaskID, op.Target.ID, op.Key)] = op.ID
		key := actionHoldKey(op.TaskID, op.Target.ID)
		if op.State == "applied" || op.State == "not_applied" {
			if s.actionHolds[key] == op.ID {
				delete(s.actionHolds, key)
			}
		} else {
			s.actionHolds[key] = op.ID
		}
	}
}

func (e *Engine) WorkerAction(ctx context.Context, token string, in structuredworker.Mutation) (structuredworker.Receipt, error) {
	s := e.Store
	s.mu.Lock()
	p, err := s.authenticateWorkerLocked(token)
	if err != nil {
		s.mu.Unlock()
		return structuredworker.Receipt{}, err
	}
	if !validGraphKey(in.RequestID) || in.ID != p.ID || len(in.Data) > 256<<10 {
		s.mu.Unlock()
		return structuredworker.Receipt{}, ErrConflict
	}
	key := workerRequestKey(p.ID, in.Incarnation, in.RequestID)
	digest := graphDigest(in)
	if receipt, ok, err := s.workerReplayLocked(key, digest); ok {
		s.mu.Unlock()
		return receipt, err
	}
	r, _, err := s.validateWorkerBindingLocked(p, in.Binding)
	if err != nil {
		s.mu.Unlock()
		return structuredworker.Receipt{}, err
	}
	if in.Kind == "action" && !s.versionsCurrentLocked(runTenant(r), r.Brief.Structured.ConsumedVersions) {
		s.mu.Unlock()
		return structuredworker.Receipt{}, ErrSuperseded
	}
	var request structuredworker.ActionRequest
	d := json.NewDecoder(bytes.NewReader(in.Data))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(new(any)) != io.EOF || !validGraphKey(request.Key) {
		s.mu.Unlock()
		return structuredworker.Receipt{}, ErrConflict
	}
	target, err := s.actionTargetAllowedLocked(r, request.Target)
	if err != nil {
		s.mu.Unlock()
		return structuredworker.Receipt{}, err
	}
	newOperation := false
	var preparedReceipt structuredworker.Receipt
	opID := s.operationKeys[actionOperationKey(r.TaskID, target.ID, request.Key)]
	op, exists := s.externalOperations[opID]
	if in.Kind == "action" {
		schema, err := compileResultSchema(target.InputSchema)
		if err != nil {
			s.mu.Unlock()
			return structuredworker.Receipt{}, err
		}
		value, err := validateResult(schema, request.Payload)
		if err != nil {
			s.mu.Unlock()
			return structuredworker.Receipt{}, err
		}
		canonical, _ := json.Marshal(value)
		if exists && op.PayloadDigest != digestText(string(canonical)) {
			s.mu.Unlock()
			return structuredworker.Receipt{}, ErrConflict
		}
		if held := s.actionHolds[actionHoldKey(r.TaskID, target.ID)]; held != "" {
			s.mu.Unlock()
			return structuredworker.Receipt{State: "reconciliation_required", OperationID: held}, ErrReconciliationRequired
		}
		if !exists {
			op = ExternalOperation{ID: newID("operation"), TaskID: r.TaskID, Tenant: runTenant(r), Target: graphClone(target), Key: request.Key, Payload: canonical, PayloadDigest: digestText(string(canonical)), Generation: in.Generation, State: "prepared"}
			preparedReceipt = structuredworker.Receipt{RequestID: in.RequestID, Seq: s.seq + 1, State: "prepared", OperationID: op.ID}
			if err = s.commit(r.ID, "effect", "external_operation_prepared", change{Action: &actionChange{Operation: &op}, Worker: &workerChange{Receipt: &workerReceipt{Key: key, Digest: digest, Response: preparedReceipt}}}); err != nil {
				s.mu.Unlock()
				return structuredworker.Receipt{}, err
			}
			newOperation = true
		}
	} else if in.Kind == "reconcile" {
		if !exists {
			s.mu.Unlock()
			return structuredworker.Receipt{}, ErrNotFound
		}
	} else {
		s.mu.Unlock()
		return structuredworker.Receipt{}, ErrConflict
	}
	s.mu.Unlock()
	if op.State != "applied" && op.State != "not_applied" {
		// Only a newly prepared identity may dispatch. Every subsequent attempt asks
		// the target about that identity; it never repeats an ambiguous POST.
		method := "POST"
		if exists || in.Kind == "reconcile" {
			method = "GET"
		}
		state, result := e.callExternalOperation(ctx, op, method)
		s.mu.Lock()
		current := s.externalOperations[op.ID]
		if current.State != "applied" && current.State != "not_applied" {
			op.State = state
			op.Result = result
			if err = s.commit(in.RunID, "effect", "external_operation_observed", change{Action: &actionChange{Operation: &op}}); err != nil {
				s.mu.Unlock()
				return structuredworker.Receipt{}, err
			}
		} else {
			op = current
		}
		s.mu.Unlock()
	}
	if newOperation {
		return preparedReceipt, nil
	}
	response := structuredworker.Receipt{RequestID: in.RequestID, State: op.State, OperationID: op.ID, Result: op.Result}
	s.mu.Lock()
	defer s.mu.Unlock()
	// An external observation remains authoritative even if the worker grant
	// ended while the request was in flight. It does not accept the Run's result.
	response.Seq = s.seq + 1
	if prior, ok, err := s.workerReplayLocked(key, digest); ok {
		return prior, err
	}
	err = s.commit(in.RunID, "effect", "external_action_receipt", change{Worker: &workerChange{Receipt: &workerReceipt{Key: key, Digest: digest, Response: response}}})
	return response, err
}

func (e *Engine) callExternalOperation(parent context.Context, op ExternalOperation, method string) (string, json.RawMessage) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	endpoint := op.Target.Endpoint + "/operations"
	var body io.Reader
	if method == "GET" {
		endpoint += "/" + url.PathEscape(op.ID)
	} else {
		raw, _ := json.Marshal(map[string]any{"idempotency_key": op.ID, "payload": op.Payload})
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return "unknown", nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", op.ID)
	if op.Target.CredentialRef != "" {
		if !ValidDeviceID(op.Target.CredentialRef) {
			return "unknown", nil
		}
		secret, err := os.ReadFile(filepath.Join(e.Store.Dir(), "credentials", op.Target.CredentialRef))
		if err != nil {
			return "unknown", nil
		}
		req.Header.Set("Authorization", "Bearer "+string(secret))
	}
	client := e.actionHTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	copy := *client
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copy.Do(req)
	if err != nil {
		return "unknown", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "unknown", nil
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10+1))
	if err != nil || len(raw) > 256<<10 {
		return "unknown", nil
	}
	var outcome struct {
		Key    string          `json:"idempotency_key"`
		State  string          `json:"state"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &outcome) != nil || outcome.Key != op.ID {
		return "unknown", nil
	}
	if outcome.State == "not_applied" {
		return "not_applied", nil
	}
	if outcome.State != "applied" {
		return "unknown", nil
	}
	schema, err := compileResultSchema(op.Target.ResultSchema)
	if err != nil {
		return "unknown", nil
	}
	value, err := validateResult(schema, outcome.Result)
	if err != nil {
		return "unknown", nil
	}
	canonical, _ := json.Marshal(value)
	return "applied", canonical
}
