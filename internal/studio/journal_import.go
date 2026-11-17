package studio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// importJSONL is the only runtime JSONL reader. It never edits the original.
// The transaction marker is committed with the imported records, so restart
// either retries the entire import or ignores the backup forever.
func (j *sqliteJournal) importJSONL(dir string) error {
	var imported string
	if err := j.db.QueryRow("SELECT value FROM metadata WHERE key='jsonl_imported'").Scan(&imported); err != nil {
		return err
	}
	if imported == "true" {
		return nil
	}
	if imported != "false" {
		return errors.New("unknown JSONL import state")
	}
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if f != nil {
		defer f.Close()
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return errors.New("legacy data directory is already open by another mitos process")
		}
	}
	tx, err := j.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("unmarked JSONL import has existing events")
	}
	var seq int64
	var previous string
	if f != nil {
		reader := bufio.NewReader(f)
		for {
			line, readErr := reader.ReadBytes('\n')
			// An interrupted final write was never acknowledged. Preserve it in
			// the backup, but import only the complete verified record prefix.
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return readErr
			}
			var e Event
			if err = json.Unmarshal(line, &e); err != nil {
				return err
			}
			if err = validateJournalEvent(e, seq, previous); err != nil {
				return fmt.Errorf("JSONL import at sequence %d: %w", e.Seq, err)
			}
			if _, err = tx.Exec("INSERT INTO events(seq,run_id,event) VALUES(?,?,?)", e.Seq, e.RunID, []byte(line)); err != nil {
				return err
			}
			projections, err := projectJournalEvents([]Event{e})
			if err != nil {
				return err
			}
			if err = writeJournalProjections(tx, projections); err != nil {
				return err
			}
			seq, previous = e.Seq, e.Hash
		}
	}
	library, err := readLegacyLibrary(dir)
	if err != nil {
		return err
	}
	if library != nil {
		data, _ := json.Marshal(change{AgentLibrary: library})
		e := Event{Seq: seq + 1, Type: "effect", Kind: "agent_library_imported", At: time.Now().UTC(), Previous: previous, Data: data}
		e.Hash = eventHash(e)
		raw, _ := json.Marshal(e)
		if _, err = tx.Exec("INSERT INTO events(seq,run_id,event) VALUES(?,?,?)", e.Seq, e.RunID, raw); err != nil {
			return err
		}
		projections, err := projectJournalEvents([]Event{e})
		if err != nil {
			return err
		}
		if err = writeJournalProjections(tx, projections); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE metadata SET value='true' WHERE key='jsonl_imported'"); err != nil {
		return err
	}
	return tx.Commit()
}
