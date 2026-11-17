package studio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"

	"mitos/internal/decision"
	"mitos/internal/structuredworker"
)

type TaskInputRequest struct {
	Key  string          `json:"key"`
	Data json.RawMessage `json:"data"`
}
type TaskInputReceipt struct {
	TaskID    string `json:"task_id"`
	Seq       int64  `json:"seq"`
	GlobalSeq int64  `json:"global_seq"`
	RunID     string `json:"run_id"`
	State     string `json:"state"`
}
type TaskInput struct {
	TaskID      string          `json:"task_id"`
	Tenant      decision.Tenant `json:"tenant"`
	PrincipalID string          `json:"principal_id"`
	structuredworker.Input
	Receipt TaskInputReceipt `json:"receipt"`
}
type taskWait struct {
	TaskID  string `json:"task_id"`
	RunID   string `json:"run_id"`
	Cursor  int64  `json:"cursor"`
	Dormant bool   `json:"dormant"`
}
type taskInputChange struct {
	Record     *TaskInput `json:"record,omitempty"`
	Wait       *taskWait  `json:"wait,omitempty"`
	Generation uint64     `json:"generation,omitempty"`
}

func (s *Store) applyTaskInput(c *taskInputChange) {
	if s.taskInputs == nil {
		s.taskInputs = map[string][]TaskInput{}
		s.taskInputKeys = map[string]TaskInput{}
		s.taskWaits = map[string]taskWait{}
		s.pendingInputWakes = map[string]bool{}
	}
	if c.Record != nil {
		r := graphClone(*c.Record)
		s.taskInputs[r.TaskID] = append(s.taskInputs[r.TaskID], r)
		s.taskInputKeys[scopedRequestKey(r.Tenant, r.TaskID+"/"+r.Key)] = r
	}
	if c.Wait != nil {
		s.taskWaits[c.Wait.TaskID] = *c.Wait
		if c.Generation > 0 {
			if s.workerGenerations == nil {
				s.workerGenerations = map[string]uint64{}
			}
			s.workerGenerations[c.Wait.TaskID] = c.Generation
		}
	}
	taskID := ""
	if c.Record != nil {
		taskID = c.Record.TaskID
	}
	if c.Wait != nil {
		taskID = c.Wait.TaskID
	}
	if wait := s.taskWaits[taskID]; wait.Dormant && int64(len(s.taskInputs[taskID])) > wait.Cursor {
		s.pendingInputWakes[taskID] = true
	} else {
		delete(s.pendingInputWakes, taskID)
	}
}
func canonicalTaskInput(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return nil, errors.New("input must be a JSON object of at most 256 KiB")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return nil, err
	}
	var value map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&value) != nil || value == nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("input must be a JSON object")
	}
	return json.Marshal(value)
}
func (s *Store) AppendTaskInput(id string, in TaskInputRequest, p decision.Principal) (TaskInputReceipt, error) {
	raw, err := canonicalTaskInput(in.Data)
	if err != nil || !validGraphKey(in.Key) {
		return TaskInputReceipt{}, ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.authorizeServiceLocked(p, true); err != nil {
		return TaskInputReceipt{}, err
	}
	task := s.tasks[id]
	if task == nil || task.Tenant != p.Tenant || task.Brief.Structured == nil {
		return TaskInputReceipt{}, ErrNotFound
	}
	key := scopedRequestKey(p.Tenant, id+"/"+in.Key)
	if prior, ok := s.taskInputKeys[key]; ok {
		if prior.SHA256 != digestText(string(raw)) || prior.PrincipalID != p.ID {
			return TaskInputReceipt{}, ErrConflict
		}
		return prior.Receipt, nil
	}
	r := s.runs[task.CurrentRunID]
	if r == nil || r.Terminal() {
		return TaskInputReceipt{}, ErrConflict
	}
	seq := int64(len(s.taskInputs[id])) + 1
	receipt := TaskInputReceipt{TaskID: id, Seq: seq, GlobalSeq: s.seq + 1, RunID: r.ID, State: "stored"}
	record := TaskInput{TaskID: id, Tenant: p.Tenant, PrincipalID: p.ID, Input: structuredworker.Input{Seq: seq, Key: in.Key, Data: raw, SHA256: digestText(string(raw))}}
	c := change{Input: &taskInputChange{Record: &record}}
	runID := r.ID
	kind := "task_input_appended"
	if wait := s.taskWaits[id]; wait.Dormant && wait.RunID == r.ID {
		next, wakeErr := s.prepareInputWakeLocked(r)
		if wakeErr == nil {
			wait.Dormant = false
			c.Input.Wait = &wait
			c.Input.Generation = s.workerGenerations[id] + 1
			next.AuthorityGeneration = c.Input.Generation
			c.Run = &next
			runID = next.ID
			receipt.RunID = next.ID
			receipt.State = "woke"
			kind = "task_input_woke"
		} else {
			receipt.State = "wake_withheld"
			c.Summary = "Input is durable; current authority or task budget withholds a replacement attempt."
		}
	}
	record.Receipt = receipt
	if err = s.commit(runID, "effect", kind, c); err != nil {
		return TaskInputReceipt{}, err
	}
	return receipt, nil
}
func (s *Store) prepareInputWakeLocked(prior *Run) (Run, error) {
	p := decision.Principal{ID: prior.PrincipalID, Tenant: runTenant(prior), Authenticated: true}
	if !s.admittedPrincipalAllowedLocked(p.ID, p.Tenant) {
		return Run{}, ErrForbidden
	}
	if err := s.tenantAdmissionLocked(p, prior.Brief, prior.ID); err != nil {
		return Run{}, err
	}
	queued := 0
	for _, r := range s.runs {
		if runTenant(r) == p.Tenant && r.Brief.Structured != nil && (r.Status == "queued" || r.Status == "blocked") {
			queued++
		}
	}
	if queued >= s.tenantLimitsLocked(p.Tenant).Queue {
		return Run{}, ErrTenantCapacity
	}
	for _, id := range prior.Brief.Structured.ActionTargets {
		if _, err := s.actionTargetAllowedLocked(prior, id); err != nil {
			return Run{}, err
		}
	}
	b := graphClone(prior.Brief)
	return Run{ID: newID("local"), TaskID: prior.TaskID, ParentID: prior.ID, Tenant: p.Tenant, PrincipalID: p.ID, AdmissionLimits: graphClone(prior.AdmissionLimits), Brief: b, AppName: prior.AppName, Status: "queued", Stage: "Input available", Summary: "A durable input created a fresh attempt; the previous worker grant stays fenced.", CreatedAt: time.Now().UTC()}, nil
}

func (e *Engine) reconcileDormantInputs() error {
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	for id := range s.pendingInputWakes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.taskWaits[ids[i]], s.taskWaits[ids[j]]
		return s.taskInputs[ids[i]][a.Cursor].Receipt.GlobalSeq < s.taskInputs[ids[j]][b.Cursor].Receipt.GlobalSeq
	})
	admitted := 0
	for _, id := range ids {
		if admitted >= 50 {
			break
		}
		wait := s.taskWaits[id]
		task := s.tasks[id]
		prior := s.runs[wait.RunID]
		if !wait.Dormant || task == nil || task.CurrentRunID != wait.RunID || prior == nil || prior.Status != "waiting" {
			continue
		}
		next, err := s.prepareInputWakeLocked(prior)
		if err != nil {
			continue
		}
		wait.Dormant = false
		generation := s.workerGenerations[id] + 1
		next.AuthorityGeneration = generation
		if err = s.commit(next.ID, "effect", "task_input_wake_admitted", change{Run: &next, Input: &taskInputChange{Wait: &wait, Generation: generation}}); err != nil {
			return err
		}
		admitted++
	}
	return nil
}
func (s *Store) taskInputPageLocked(task string, after int64, limit int) (structuredworker.InputPage, error) {
	if err := validReadPage(after, limit); err != nil {
		return structuredworker.InputPage{}, err
	}
	items := s.taskInputs[task]
	if after > int64(len(items)) {
		return structuredworker.InputPage{}, ErrInvalidPage
	}
	page := structuredworker.InputPage{Inputs: []structuredworker.Input{}, Next: after}
	size := 0
	for i := int(after); i < len(items) && len(page.Inputs) < limit; i++ {
		item := items[i].Input
		raw, _ := json.Marshal(item)
		if size+len(raw) > ReadPageBytes {
			break
		}
		page.Inputs = append(page.Inputs, graphClone(item))
		page.Next = item.Seq
		size += len(raw)
	}
	page.HasMore = page.Next < int64(len(items))
	return page, nil
}
func (v *ScopedStore) Inputs(id string, after int64, limit int) (structuredworker.InputPage, error) {
	s := v.store
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.authorizeServiceLocked(v.principal, false); err != nil {
		return structuredworker.InputPage{}, err
	}
	task := s.tasks[id]
	if task == nil || task.Tenant != v.principal.Tenant || task.Brief.Structured == nil {
		return structuredworker.InputPage{}, ErrNotFound
	}
	return s.taskInputPageLocked(id, after, limit)
}
func (a *ApplicationSession) Inputs(id string, after int64, limit int) (structuredworker.InputPage, error) {
	return a.view.Inputs(id, after, limit)
}
func (a *ApplicationSession) AppendInput(id string, in TaskInputRequest) (TaskInputReceipt, error) {
	return a.view.store.AppendTaskInput(id, in, a.view.principal)
}
