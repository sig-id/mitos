package studio

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

var ErrStaleBinding = errors.New("stale_binding")

type WorkerCredential struct {
	ID        string    `json:"id"`
	Pools     []string  `json:"pools"`
	Agents    []string  `json:"agents"`
	ExpiresAt time.Time `json:"expires_at"`
	Epoch     string    `json:"epoch"`
	Revoked   bool      `json:"revoked"`
	TokenHash string    `json:"token_hash,omitempty"`
	Local     bool      `json:"local,omitempty"`
}
type workerRegistration struct {
	structuredworker.Identity
	Capabilities []string `json:"capabilities"`
}
type workerReceipt struct {
	Key      string                   `json:"key"`
	Digest   string                   `json:"digest"`
	Response structuredworker.Receipt `json:"response"`
}
type WorkerOutbox struct {
	ID         string `json:"id"`
	Seq        int64  `json:"seq"`
	Pool       string `json:"pool"`
	Capability string `json:"capability"`
	Published  bool   `json:"published,omitempty"`
}
type StructuredCheckpoint struct {
	TaskID           string          `json:"task_id"`
	RunID            string          `json:"run_id"`
	Generation       uint64          `json:"generation"`
	InputDigest      string          `json:"input_digest"`
	DefinitionDigest string          `json:"definition_digest"`
	SchemaDigest     string          `json:"schema_digest"`
	Value            json.RawMessage `json:"value"`
}
type workerChange struct {
	Checkpoint   *StructuredCheckpoint         `json:"checkpoint,omitempty"`
	Outbox       *WorkerOutbox                 `json:"outbox,omitempty"`
	Credential   *WorkerCredential             `json:"credential,omitempty"`
	Registration *workerRegistration           `json:"registration,omitempty"`
	Lease        *structuredworker.LeaseRecord `json:"lease,omitempty"`
	Receipt      *workerReceipt                `json:"receipt,omitempty"`
	Evidence     json.RawMessage               `json:"evidence,omitempty"`
}

func (s *Store) applyWorker(c *workerChange) {
	if s.workers == nil {
		s.workers = map[string]WorkerCredential{}
		s.workerRegistrations = map[string]workerRegistration{}
		s.workerLeases = map[string]structuredworker.LeaseRecord{}
		s.workerGenerations = map[string]uint64{}
		s.workerReceipts = map[string]workerReceipt{}
	}
	if c.Checkpoint != nil {
		if s.structuredCheckpoints == nil {
			s.structuredCheckpoints = map[string]StructuredCheckpoint{}
		}
		s.structuredCheckpoints[c.Checkpoint.TaskID] = graphClone(*c.Checkpoint)
	}
	if c.Outbox != nil {
		if s.workerOutbox == nil {
			s.workerOutbox = map[string]WorkerOutbox{}
		}
		s.workerOutbox[c.Outbox.ID] = *c.Outbox
	}
	if c.Credential != nil {
		s.workers[c.Credential.ID] = graphClone(*c.Credential)
	}
	if c.Registration != nil {
		s.workerRegistrations[c.Registration.ID] = graphClone(*c.Registration)
		if s.workerIncarnations == nil {
			s.workerIncarnations = map[string]bool{}
		}
		s.workerIncarnations[workerRequestKey(c.Registration.ID, c.Registration.Incarnation, "")] = true
	}
	if c.Lease != nil {
		l := *c.Lease
		s.workerLeases[l.RunID] = l
		if l.Generation > s.workerGenerations[l.TaskID] {
			s.workerGenerations[l.TaskID] = l.Generation
		}
	}
	if c.Receipt != nil {
		s.workerReceipts[c.Receipt.Key] = graphClone(*c.Receipt)
	}
}
func (s *Store) CreateWorkerCredential(in WorkerCredential, operator decision.Principal) (WorkerCredential, string, error) {
	if !localOperator(operator) {
		return in, "", ErrForbidden
	}
	if !ValidDeviceID(in.ID) || len(in.Pools) == 0 || len(in.Pools) > 128 || len(in.Agents) == 0 || len(in.Agents) > 128 || !in.ExpiresAt.After(time.Now()) || in.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		return in, "", errors.New("invalid worker credential")
	}
	for _, id := range append(slices.Clone(in.Pools), in.Agents...) {
		if !ValidDeviceID(id) {
			return in, "", errors.New("invalid worker scope")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.workers[in.ID]; ok {
		return in, "", ErrConflict
	}
	token, err := newDeviceToken()
	if err != nil {
		return in, "", err
	}
	in.Epoch = s.authorityEpoch
	in.TokenHash = tokenHash(token)
	in.Revoked = false
	if err = s.commit("", "effect", "worker_credential_created", change{Worker: &workerChange{Credential: &in}}); err != nil {
		return in, "", err
	}
	in.TokenHash = ""
	return in, token, nil
}
func (s *Store) RevokeWorker(id string, operator decision.Principal) error {
	if !localOperator(operator) {
		return ErrForbidden
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.workers[id]
	if !ok {
		return ErrNotFound
	}
	p.Revoked = true
	p.TokenHash = ""
	return s.commit("", "effect", "worker_revoked", change{Worker: &workerChange{Credential: &p}})
}
func (s *Store) authenticateWorkerLocked(token string) (WorkerCredential, error) {
	digest := tokenHash(token)
	for _, p := range s.workers {
		if p.TokenHash == digest && !p.Revoked && p.Epoch == s.authorityEpoch && time.Now().Before(p.ExpiresAt) {
			return p, nil
		}
	}
	return WorkerCredential{}, ErrUnauthenticated
}
func workerRequestKey(id, incarnation, request string) string {
	b, _ := json.Marshal([]string{id, incarnation, request})
	return string(b)
}
func (s *Store) workerReplayLocked(key, digest string) (structuredworker.Receipt, bool, error) {
	if r, ok := s.workerReceipts[key]; ok {
		if r.Digest != digest {
			return structuredworker.Receipt{}, true, ErrConflict
		}
		if r.Response.State == "stale_binding" {
			return r.Response, true, ErrStaleBinding
		}
		return r.Response, true, nil
	}
	return structuredworker.Receipt{}, false, nil
}
func (s *Store) RegisterWorker(token string, in structuredworker.Register) (structuredworker.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.authenticateWorkerLocked(token)
	if err != nil {
		return structuredworker.Receipt{}, err
	}
	if !ValidDeviceID(in.Incarnation) || !validGraphKey(in.RequestID) || len(in.Capabilities) == 0 || len(in.Capabilities) > 128 {
		return structuredworker.Receipt{}, ErrConflict
	}
	for _, cap := range in.Capabilities {
		if !slices.Contains(p.Agents, cap) {
			return structuredworker.Receipt{}, ErrForbidden
		}
	}
	key := workerRequestKey(p.ID, in.Incarnation, in.RequestID)
	digest := graphDigest(in)
	if receipt, ok, err := s.workerReplayLocked(key, digest); ok {
		return receipt, err
	}
	if s.workerIncarnations[workerRequestKey(p.ID, in.Incarnation, "")] && s.workerRegistrations[p.ID].Incarnation != in.Incarnation {
		return structuredworker.Receipt{}, ErrStaleBinding
	}
	r := workerRegistration{Identity: structuredworker.Identity{ID: p.ID, Incarnation: in.Incarnation}, Capabilities: slices.Clone(in.Capabilities)}
	response := structuredworker.Receipt{RequestID: in.RequestID, Seq: s.seq + 1, State: "registered"}
	err = s.commit("", "effect", "worker_registered", change{Worker: &workerChange{Registration: &r, Receipt: &workerReceipt{Key: key, Digest: digest, Response: response}}})
	return response, err
}
func (s *Store) workerActiveLocked() []string {
	active := []string{}
	// Include all admitted execution, including workspace work in the local dispatcher.
	for _, r := range s.runs {
		if (r.Status == "running" || r.Status == "stopping") && r.Brief.AppID != WorkflowAppID && r.ExecutionKind != "interactive" {
			active = append(active, r.ID)
		}
	}
	return active
}
func (s *Store) includeWorkerActiveLocked(ids []string) []string {
	out := slices.Clone(ids)
	for _, id := range s.workerActiveLocked() {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
func (s *Store) WorkerClaim(token string, in structuredworker.Claim) (structuredworker.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.authenticateWorkerLocked(token)
	if err != nil {
		return structuredworker.Receipt{}, err
	}
	reg := s.workerRegistrations[p.ID]
	if reg.Incarnation != in.Incarnation || in.ID != p.ID || !validGraphKey(in.RequestID) {
		return structuredworker.Receipt{}, ErrStaleBinding
	}
	key := workerRequestKey(p.ID, in.Incarnation, in.RequestID)
	digest := graphDigest(in)
	if receipt, ok, err := s.workerReplayLocked(key, digest); ok {
		return receipt, err
	}
	// ReadyRuns owns ordering and fairness. Claim's eligibility checks and binding
	// run under the same writer lock as ordinary local pool admission.
	candidates, err := s.readyRunsLocked(s.workerActiveLocked())
	if err != nil {
		return structuredworker.Receipt{}, err
	}
	for _, candidate := range candidates {
		r := candidate.Run
		if in.RunID != "" && in.RunID != r.ID {
			continue
		}
		if r.Brief.Structured == nil || r.Brief.Structured.Frozen == nil || r.GraphAttempt != nil || r.Brief.Workspace != "" || r.ExecutionKind == "interactive" || r.Sample {
			continue
		}
		task := s.tasks[r.TaskID]
		if task == nil || !slices.Contains(p.Pools, task.Scheduling.Pool) || !slices.Contains(reg.Capabilities, r.Brief.Structured.Frozen.ID) {
			continue
		}
		if !s.admittedPrincipalAllowedLocked(r.PrincipalID, runTenant(&r)) {
			continue
		}
		remaining := time.Until(s.structuredTaskDeadline(&r))
		budget := r.Brief.TimeoutMinutes * 60
		if remaining > 0 && int(remaining.Seconds()) < budget {
			budget = int(remaining.Seconds())
		}
		// The lease budget is the narrowest of every admitted limit: the brief,
		// the tenant's elapsed budget, an explicit scheduling deadline and the
		// frozen definition's own execution timeout.
		if deadline := r.Brief.Scheduling.Deadline; deadline != nil {
			if window := int(time.Until(*deadline).Seconds()); window > 0 && window < budget {
				budget = window
			}
		}
		if limit := r.Brief.Structured.Frozen.Manifest.Limits.TimeoutSeconds; limit > 0 && limit < budget {
			budget = limit
		}
		if budget <= 0 {
			continue
		}
		lease := structuredworker.LeaseRecord{Binding: structuredworker.Binding{Identity: reg.Identity, TaskID: r.TaskID, RunID: r.ID, Generation: s.workerGenerations[r.TaskID] + 1, Epoch: s.authorityEpoch, InputDigest: digestText(string(r.Brief.Structured.Input)), DefinitionDigest: r.Brief.Structured.Digest, SchemaDigest: digestText(string(r.Brief.Structured.ResultSchema)), Acceptance: "exact-schema/current-permissions", LeaseSeconds: 30, BudgetSeconds: budget}, ExpiresAt: time.Now().UTC().Add(30 * time.Second), BudgetEndsAt: time.Now().UTC().Add(time.Duration(budget) * time.Second)}
		response := structuredworker.Receipt{RequestID: in.RequestID, Seq: s.seq + 1, State: "claimed", Binding: &lease.Binding}
		wc := &workerChange{Lease: &lease, Receipt: &workerReceipt{Key: key, Digest: digest, Response: response}}
		acquired, err := s.scheduleLocked(r.ID, s.workerActiveLocked(), &candidate.Policy, wc)
		if err != nil {
			return structuredworker.Receipt{}, err
		}
		if acquired {
			return response, nil
		}
	}
	// Empty polls have no effects and can safely be retried with their request ID.
	return structuredworker.Receipt{RequestID: in.RequestID, State: "empty"}, nil
}
func (s *Store) validateWorkerBindingLocked(p WorkerCredential, b structuredworker.Binding) (*Run, structuredworker.LeaseRecord, error) {
	l, ok := s.workerLeases[b.RunID]
	reg := s.workerRegistrations[p.ID]
	r := s.runs[b.RunID]
	if !ok || r == nil || l.Closed || l.Binding != b || b.ID != p.ID || reg.Incarnation != b.Incarnation || b.Epoch != s.authorityEpoch || s.workerGenerations[b.TaskID] != b.Generation || !time.Now().Before(l.ExpiresAt) || !time.Now().Before(l.BudgetEndsAt) || r.Status != "running" || s.tasks[b.TaskID] == nil || s.tasks[b.TaskID].CurrentRunID != b.RunID {
		return nil, l, ErrStaleBinding
	}
	if !s.admittedPrincipalAllowedLocked(r.PrincipalID, runTenant(r)) {
		return nil, l, ErrForbidden
	}
	return r, l, nil
}
