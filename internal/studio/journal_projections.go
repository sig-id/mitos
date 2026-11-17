package studio

import (
	"encoding/json"
	"strconv"
)

// projectJournalEvents is a pure fold of canonical records. The same function
// builds live indexes, import indexes, and a complete rebuild after corruption
// or an index-format upgrade; it owns no lifecycle semantics.
func projectJournalEvents(events []Event) ([]journalProjection, error) {
	var out []journalProjection
	for _, e := range events {
		var c change
		if err := json.Unmarshal(e.Data, &c); err != nil {
			return nil, err
		}
		if c.Input != nil {
			add := func(namespace, key string, value any) {
				raw, _ := json.Marshal(value)
				out = append(out, journalProjection{Namespace: namespace, Key: key, Value: raw})
			}
			if c.Input.Record != nil {
				r := c.Input.Record
				if r.TaskID != "" {
					add("task_input", r.TaskID+"/"+strconv.FormatInt(r.Seq, 10), r)
				}
				for _, v := range r.Versions {
					add("tenant_version", scopedRequestKey(r.Tenant, v.Name), v.Value)
				}
				add("task_input_key", scopedRequestKey(r.Tenant, r.TaskID+"/"+r.Key), r.Receipt)
			}
			if c.Input.Wait != nil {
				add("task_wait", c.Input.Wait.TaskID, c.Input.Wait)
				if c.Input.Generation > 0 {
					add("task_generation", c.Input.Wait.TaskID, c.Input.Generation)
				}
			}
		}

		if c.Action != nil {
			if c.Action.Target != nil {
				raw, _ := json.Marshal(c.Action.Target)
				out = append(out, journalProjection{Namespace: "action_target", Key: c.Action.Target.ID, Value: raw})
			}
			if c.Action.Operation != nil {
				op := c.Action.Operation
				raw, _ := json.Marshal(op)
				out = append(out, journalProjection{Namespace: "external_operation", Key: op.ID, Value: raw})
				held := op.State != "applied" && op.State != "not_applied"
				holdValue := raw
				if !held {
					holdValue = nil
				}
				out = append(out, journalProjection{Namespace: "task_action_hold", Key: actionHoldKey(op.TaskID, op.Target.ID), Value: holdValue})
			}
		}

		if c.Worker != nil {
			wc := c.Worker
			add := func(namespace, key string, value any) {
				raw, _ := json.Marshal(value)
				out = append(out, journalProjection{Namespace: namespace, Key: key, Value: raw})
			}
			if wc.Context != nil {
				add("worker_context", wc.Context.RunID, wc.Context)
			}
			if wc.Checkpoint != nil {
				add("structured_checkpoint", wc.Checkpoint.TaskID, wc.Checkpoint)
			}
			if wc.Credential != nil {
				add("worker_credential", wc.Credential.ID, wc.Credential)
			}
			if wc.Registration != nil {
				add("worker_registration", wc.Registration.ID, wc.Registration)
			}
			if wc.Lease != nil {
				add("worker_lease", wc.Lease.RunID, wc.Lease)
				add("task_generation", wc.Lease.TaskID, wc.Lease.Generation)
			}
			if wc.Receipt != nil {
				add("worker_receipt", wc.Receipt.Key, wc.Receipt)
			}
			if wc.Outbox != nil {
				add("worker_outbox", wc.Outbox.ID, wc.Outbox)
			}
		}

		if c.Task != nil && c.Task.Brief.Structured != nil && c.Task.Brief.Structured.SerializationKey != "" {
			t := c.Task
			var raw []byte
			if t.Status == "running" || t.Status == "scheduled" {
				raw, _ = json.Marshal(map[string]any{"tenant": t.Tenant, "key": t.Brief.Structured.SerializationKey, "run_id": t.CurrentRunID})
			}
			out = append(out, journalProjection{Namespace: "serialization_run", Key: t.CurrentRunID, Value: raw})
		}

		ids := map[string]bool{}
		if e.RunID != "" {
			ids[e.RunID] = true
		}
		if c.Graph != nil {
			for _, r := range c.Graph.Runs {
				ids[r.ID] = true
			}
			for _, t := range c.Graph.Tasks {
				if t.CurrentRunID != "" {
					ids[t.CurrentRunID] = true
				}
			}
		}
		for id := range ids {
			out = append(out, journalProjection{Namespace: "run_cursor", Key: id, Value: []byte(strconv.FormatInt(e.Seq, 10))})
		}
		if c.Run != nil && c.Run.Brief.Structured != nil && c.Run.ParentID == "" {
			if d := c.Run.Brief.Structured.Frozen; d != nil {
				value, _ := json.Marshal(d)
				out = append(out, journalProjection{Namespace: "agent_revision", Key: c.Run.Brief.Structured.Definition, Value: value})
			}
			raw, _ := json.Marshal(map[string]any{"run_id": c.Run.ID, "principal_id": c.Run.PrincipalID, "request_hash": c.Run.Brief.Structured.RequestHash, "seq": e.Seq})
			out = append(out, journalProjection{Namespace: "request_key", Key: scopedRequestKey(runTenant(c.Run), c.Run.Brief.Structured.Key), Value: raw})
		}
		if c.ServicePrincipal != nil {
			raw, _ := json.Marshal(c.ServicePrincipal)
			out = append(out, journalProjection{Namespace: "service_principal", Key: c.ServicePrincipal.ID, Value: raw})
		}
		if c.Artifact != nil {
			value, _ := json.Marshal(c.Artifact)
			out = append(out, journalProjection{Namespace: "artifact", Key: e.RunID + "/" + c.Artifact.Name, Value: value})
		}
		if c.AgentLibrary != nil {
			for id, revisions := range c.AgentLibrary.Revisions {
				for _, d := range revisions {
					value, _ := json.Marshal(d)
					out = append(out, journalProjection{Namespace: "agent_revision", Key: id + "@" + strconv.Itoa(d.Revision), Value: value})
				}
			}
		}
		for _, key := range c.ExpiredRequestKeys {
			out = append(out, journalProjection{Namespace: "request_tombstone", Key: legacyOrScopedKey(key), Value: []byte("expired")})
		}
	}
	return out, nil
}

func (j *sqliteJournal) rebuildProjections() error {
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM projections"); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT event FROM events ORDER BY seq")
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		var e Event
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			rows.Close()
			return err
		}
		p, err := projectJournalEvents([]Event{e})
		if err != nil {
			rows.Close()
			return err
		}
		if err = writeJournalProjections(tx, p); err != nil {
			rows.Close()
			return err
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if rowErr != nil {
		return rowErr
	}
	return tx.Commit()
}

// RebuildJournalIndexes validates the canonical chain before replacing derived
// indexes. An uncertain rebuild latches the same writer gate as a failed append.
func (s *Store) RebuildJournalIndexes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	j, ok := s.journal.(*sqliteJournal)
	if !ok {
		return errJournalClosed
	}
	err := s.mu.io(func() error {
		if err := j.Replay(func(Event) error { return nil }); err != nil {
			return err
		}
		return j.rebuildProjections()
	})
	if err != nil {
		s.writeErr = err
	}
	return err
}
