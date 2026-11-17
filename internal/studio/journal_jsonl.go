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
)

// jsonlJournal is the transitional adapter until the SQLite cutover in #77/2.2.
// It retains historical record-prefix crash recovery, NOT transaction-level crash
// atomicity. It deliberately refuses projections instead of silently losing them.
// Only the transactional adapters run the atomic journal conformance suite.
type jsonlJournal struct {
	file *os.File
}

func openJSONLJournal(dir string) (*jsonlJournal, error) {
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("this data directory is already open by another mitos process")
	}
	if err = syncDirectory(dir); err == nil {
		err = syncDirectory(filepath.Dir(dir))
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return &jsonlJournal{file: f}, nil
}

func (j *jsonlJournal) Replay(visit func(Event) error) error {
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(j.file)
	var offset, seq int64
	var previous string
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			// Only the incomplete final record may be discarded. Complete
			// corrupt records are never silently skipped or rewritten.
			if len(line) > 0 {
				if err = j.file.Truncate(offset); err != nil {
					return err
				}
				if err = j.file.Sync(); err != nil {
					return err
				}
			}
			_, err = j.file.Seek(0, io.SeekEnd)
			return err
		}
		if err != nil {
			return err
		}
		var e Event
		if err = json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("invalid event at byte %d: %w", offset, err)
		}
		if err = validateJournalEvent(e, seq, previous); err != nil {
			return fmt.Errorf("event chain damaged at sequence %d: %w", e.Seq, err)
		}
		if err = visit(e); err != nil {
			return err
		}
		seq, previous = e.Seq, e.Hash
		offset += int64(len(line))
	}
}

func (j *jsonlJournal) Append(events []Event, projections []journalProjection) error {
	if len(projections) > 0 {
		return errors.New("JSONL journal does not support transactional projections; SQLite cutover required")
	}
	var data []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	if len(data) == 0 {
		return nil
	}
	n, err := j.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.file.Sync()
	}
	return err
}

func (j *jsonlJournal) Close() error { return j.file.Close() }
