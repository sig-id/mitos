package studio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"mitos/internal/failpoint"
	"mitos/internal/structuredworker"
)

func (s *Store) WorkerMutate(token string, in structuredworker.Mutation) (structuredworker.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.authenticateWorkerLocked(token)
	if err != nil {
		return structuredworker.Receipt{}, err
	}
	if !validGraphKey(in.RequestID) || len(in.Data) > 16<<20 || in.ID != p.ID {
		return structuredworker.Receipt{}, ErrConflict
	}
	key := workerRequestKey(p.ID, in.Incarnation, in.RequestID)
	digest := graphDigest(in)
	if receipt, ok, err := s.workerReplayLocked(key, digest); ok {
		return receipt, err
	}
	r, lease, err := s.validateWorkerBindingLocked(p, in.Binding)
	if err != nil {
		known, hasBinding := s.workerLeases[in.RunID]
		if err == ErrStaleBinding && in.Kind == "result" && hasBinding && known.Binding == in.Binding && known.ID == p.ID {
			response := structuredworker.Receipt{RequestID: in.RequestID, Seq: s.seq + 1, State: "stale_binding"}
			wc := &workerChange{Evidence: in.Data, Receipt: &workerReceipt{Key: key, Digest: digest, Response: response}}
			if commitErr := s.commit(in.RunID, "effect", "stale_worker_result", change{Worker: wc}); commitErr != nil {
				return response, commitErr
			}
			return response, err
		}
		return structuredworker.Receipt{}, err
	}
	response := structuredworker.Receipt{RequestID: in.RequestID, Seq: s.seq + 1, State: in.Kind}
	c := change{}
	kind := "worker_" + in.Kind
	var artifacts []ArtifactData
	var checkpoint *StructuredCheckpoint
	var usageChange *change
	switch in.Kind {
	case "renew":
		lease.ExpiresAt = time.Now().UTC().Add(30 * time.Second)
		response.LeaseSeconds = 30
	case "progress":
		var message string
		if json.Unmarshal(in.Data, &message) != nil || len(message) > 4096 {
			return response, ErrConflict
		}
		c.Stage = "Agent working"
		c.Summary = message
	case "checkpoint":
		if len(r.Brief.Structured.CheckpointSchema) == 0 {
			return response, fmt.Errorf("checkpoint_schema_required")
		}
		if len(in.Data) > 64<<10 {
			return response, fmt.Errorf("checkpoint_too_large")
		}
		schema, err := compileResultSchema(r.Brief.Structured.CheckpointSchema)
		if err != nil {
			return response, err
		}
		value, err := validateResult(schema, in.Data)
		if err != nil {
			return response, err
		}
		canonical, _ := json.Marshal(value)
		checkpoint = &StructuredCheckpoint{Cursor: s.structuredCheckpoints[r.TaskID].Cursor, TaskID: r.TaskID, RunID: r.ID, Generation: in.Generation, InputDigest: in.InputDigest, DefinitionDigest: in.DefinitionDigest, SchemaDigest: digestText(string(r.Brief.Structured.CheckpointSchema)), Value: canonical}
	case "wait":
		var wait structuredworker.WaitRequest
		if json.Unmarshal(in.Data, &wait) != nil || wait.Cursor < 0 || wait.Cursor > int64(len(s.taskInputs[r.TaskID])) {
			return response, ErrConflict
		}
		if prior, ok := s.structuredCheckpoints[r.TaskID]; ok && wait.Cursor < prior.Cursor {
			return response, ErrConflict
		}
		// A Task without a checkpoint schema still supports cursor-only waits:
		// its input backlog can be paged without portable checkpoints. A
		// declared schema keeps requiring a conforming checkpoint value.
		hasSchema := len(r.Brief.Structured.CheckpointSchema) > 0
		if !hasSchema && len(bytes.TrimSpace(wait.Checkpoint)) > 0 && string(bytes.TrimSpace(wait.Checkpoint)) != "null" {
			return response, fmt.Errorf("checkpoint_schema_required")
		}
		if hasSchema {
			if len(wait.Checkpoint) > 64<<10 {
				return response, fmt.Errorf("checkpoint_too_large")
			}
			schema, err := compileResultSchema(r.Brief.Structured.CheckpointSchema)
			if err != nil {
				return response, err
			}
			value, err := validateResult(schema, wait.Checkpoint)
			if err != nil {
				return response, err
			}
			canonical, _ := json.Marshal(value)
			checkpoint = &StructuredCheckpoint{TaskID: r.TaskID, RunID: r.ID, Generation: in.Generation, InputDigest: in.InputDigest, DefinitionDigest: in.DefinitionDigest, SchemaDigest: digestText(string(r.Brief.Structured.CheckpointSchema)), Cursor: wait.Cursor, Value: canonical}
		} else {
			checkpoint = &StructuredCheckpoint{TaskID: r.TaskID, RunID: r.ID, Generation: in.Generation, InputDigest: in.InputDigest, DefinitionDigest: in.DefinitionDigest, Cursor: wait.Cursor}
		}
		dormant := wait.Cursor == int64(len(s.taskInputs[r.TaskID]))
		c.Input = &taskInputChange{Wait: &taskWait{TaskID: r.TaskID, RunID: r.ID, Cursor: wait.Cursor, Dormant: dormant}}
		if dormant {
			lease.Closed = true
			response.State = "dormant"
			c.Status = "waiting"
			c.Stage = "Waiting for input"
			c.Summary = "The checkpoint and consumed input cursor are durable; no execution capacity is held."
		} else {
			response.State = "continue"
			page, err := s.taskInputPageLocked(r.TaskID, wait.Cursor, ReadDefaultLimit)
			if err != nil {
				return response, err
			}
			response.Result, _ = json.Marshal(page)
		}

	case "release":
		lease.Closed = true
		pending := true
		c.Status = "failed"
		c.Stage = "Worker released"
		c.Summary = "The worker released the attempt; a bounded replacement may claim the Task."
		c.StructuredRetryPending = &pending
	case "result":
		for _, held := range s.externalOperations {
			if held.TaskID == r.TaskID && s.actionHolds[actionHoldKey(held.TaskID, held.Target.ID)] != "" {
				return response, ErrReconciliationRequired
			}
		}
		if !s.versionsCurrentLocked(runTenant(r), r.Brief.Structured.ConsumedVersions) {
			lease.Closed = true
			response.State = "superseded_context"
			response.Seq = s.seq + 1
			rejection := change{Status: "failed", Stage: "Context superseded", Summary: "Consumed preference/profile versions changed before result publication.", Worker: &workerChange{Lease: &lease, Evidence: in.Data, Receipt: &workerReceipt{Key: key, Digest: digest, Response: response}}}
			if err := s.commit(r.ID, "effect", "superseded_worker_result", rejection); err != nil {
				return response, err
			}
			return response, ErrSuperseded
		}

		var result structuredworker.Result
		if json.Unmarshal(in.Data, &result) != nil || len(result.Artifacts) > 8 {
			return response, ErrConflict
		}
		required := int64(len(s.taskInputs[r.TaskID]))
		if result.ConsumedCursor < required || result.ConsumedCursor > required {
			lease.Closed = true
			response.State = "inputs_not_consumed"
			response.Seq = s.seq + 1
			pending := true
			rejection := change{Status: "failed", Stage: "New input available", Summary: "A newer input arrived before publication; a bounded fresh attempt must consume it.", StructuredRetryPending: &pending, Worker: &workerChange{Lease: &lease, Evidence: in.Data, Receipt: &workerReceipt{Key: key, Digest: digest, Response: response}}}
			if err := s.commit(r.ID, "effect", "unconsumed_input_result", rejection); err != nil {
				return response, err
			}
			return response, ErrInputsNotConsumed
		}

		schema, err := compileResultSchema(r.Brief.Structured.ResultSchema)
		if err != nil {
			return response, err
		}
		value, err := validateResult(schema, result.Value)
		if err != nil {
			return response, fmt.Errorf("result_schema_mismatch: %w", err)
		}
		canonical, _ := json.Marshal(value)
		artifacts = append(artifacts, ArtifactData{Name: structuredResultArtifact, Data: canonical})
		for _, a := range result.Artifacts {
			if a.Name != "output.txt" && a.Name != "activity.jsonl" && a.Name != "authority.json" && a.Name != "context.json" {
				return response, ErrConflict
			}
			if a.Name == "output.txt" || a.Name == "activity.jsonl" {
				artifacts = append(artifacts, ArtifactData{Name: a.Name, Data: a.Data})
			}
		}
		context, err := structuredWorkerContext(boundedRun(r), in.Binding, s.Dir())
		if err != nil {
			return response, err
		}
		if recorded, ok := s.workerContexts[r.ID]; ok && recorded.Generation == in.Generation {
			context.Artifacts = recorded.Artifacts
		}
		for _, name := range []string{"authority.json", "context.json"} {
			artifacts = append(artifacts, ArtifactData{Name: name, Data: context.Artifacts[name]})
		}

		response.State = "accepted"
		response.SHA256 = digestText(string(canonical))
		lease.Closed = true
		kind = "run_finished"
		c.Status = "done"
		c.Stage = "Complete"
		c.Verified = true
		c.Summary = "Typed result accepted against the declared schema (sha256 " + response.SHA256 + ")."
		usageChange, _ = structuredUsageChange(r, result.Usage)
	default:
		return response, ErrNotFound
	}
	// Artifact bytes may be orphaned by a crash; only these atomic journal records
	// register them. A retry can replace bytes that were never registered.
	events := []Event{}
	seq, previous := s.seq, s.hash
	names := map[string]bool{}
	for _, a := range r.Artifacts {
		names[a.Name] = true
	}
	for _, a := range artifacts {
		if filepath.Base(a.Name) != a.Name || names[a.Name] {
			return response, ErrConflict
		}
		names[a.Name] = true
		artifact := &Artifact{Name: a.Name, SHA256: digestText(string(a.Data)), Bytes: int64(len(a.Data))}
		event, err := s.prepareEvent(r.ID, "effect", "artifact_harvested", change{Artifact: artifact}, seq+1, previous)
		if err != nil {
			return response, err
		}
		events = append(events, event)
		seq, previous = event.Seq, event.Hash
	}
	// The cost record joins the same atomic batch as the result it belongs to,
	// ahead of the terminal result event.
	if usageChange != nil {
		event, err := s.prepareEvent(r.ID, "effect", "agent_usage_recorded", *usageChange, seq+1, previous)
		if err != nil {
			return response, err
		}
		events = append(events, event)
		seq, previous = event.Seq, event.Hash
	}
	response.Seq = seq + 1
	c.Worker = &workerChange{Checkpoint: checkpoint, Lease: &lease, Receipt: &workerReceipt{Key: key, Digest: digest, Response: response}}
	if response.State == "accepted" {
		c.Worker.Outbox = &WorkerOutbox{ID: newID("outbox"), Seq: response.Seq, Pool: s.tasks[r.TaskID].Scheduling.Pool, Capability: r.Brief.Structured.Frozen.ID}
	}
	event, err := s.prepareEvent(r.ID, "effect", kind, c, seq+1, previous)
	if err != nil {
		return response, err
	}
	events = append(events, event)
	if len(artifacts) > 0 {
		if err = s.mu.io(func() error { return s.artifacts.Put(r.ID, artifacts, true) }); err != nil {
			return response, err
		}
	}
	err = s.persist(events...)
	if err == nil && in.Kind == "result" {
		failpoint.Hit("worker.result.before_reply")
	}
	return response, err
}
