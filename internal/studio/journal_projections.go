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
