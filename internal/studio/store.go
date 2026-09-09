package studio

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

type Store struct {
	mu       sync.RWMutex
	file     *os.File
	dir      string
	runs     map[string]*Run
	seq      int64
	hash     string
	writeErr error
}

func OpenStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(abs, "events.jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("this data directory is already open by another mitos process")
	}
	if err = syncDirectory(abs); err == nil {
		err = syncDirectory(filepath.Dir(abs))
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	s := &Store{file: f, dir: abs, runs: map[string]*Run{}}
	if err = s.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { s.mu.Lock(); defer s.mu.Unlock(); return s.file.Close() }
func (s *Store) Dir() string  { return s.dir }

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func eventHash(e Event) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Store) replay() error {
	r := bufio.NewReader(s.file)
	var offset int64
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			// Only an incomplete final record can be discarded. A complete corrupt
			// record is never silently skipped or rewritten.
			if len(line) > 0 {
				if err = s.file.Truncate(offset); err != nil {
					return err
				}
				if err = s.file.Sync(); err != nil {
					return err
				}
			}
			_, err = s.file.Seek(0, io.SeekEnd)
			return err
		}
		if err != nil {
			return err
		}
		var e Event
		if err = json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("invalid event at byte %d: %w", offset, err)
		}
		if e.Seq != s.seq+1 || e.Previous != s.hash || e.Hash != eventHash(e) {
			return fmt.Errorf("event chain damaged at sequence %d", e.Seq)
		}
		if err = s.apply(e); err != nil {
			return err
		}
		s.seq = e.Seq
		s.hash = e.Hash
		offset += int64(len(line))
	}
}

func (s *Store) apply(e Event) error {
	var c change
	if err := json.Unmarshal(e.Data, &c); err != nil {
		return err
	}
	if c.Run != nil {
		if s.runs[e.RunID] != nil {
			return errors.New("duplicate run creation")
		}
		s.runs[e.RunID] = c.Run
	}
	r := s.runs[e.RunID]
	if r == nil {
		return fmt.Errorf("event refers to missing run %s", e.RunID)
	}
	if c.Status != "" {
		r.Status = c.Status
	}
	if c.Stage != "" {
		r.Stage = c.Stage
	}
	if c.Summary != "" {
		r.Summary = c.Summary
	}
	if c.Verified {
		r.Verified = true
	}
	if c.Artifact != nil {
		r.Artifacts = append(r.Artifacts, *c.Artifact)
	}
	if c.Decision != nil {
		r.Decision = c.Decision
	}
	if c.Choice != "" && r.Decision != nil {
		r.Decision.Choice = c.Choice
		at := e.At
		r.Decision.ResolvedAt = &at
	}
	r.UpdatedAt = e.At
	if r.Terminal() {
		r.FinishedAt = e.At
	}
	r.Events = append(r.Events, e)
	return nil
}

// commit requires mu. Publish to readers only after the record is synced.
func (s *Store) commit(id, typ, kind string, c change) error {
	if s.writeErr != nil {
		return fmt.Errorf("event log unavailable: %w", s.writeErr)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	e := Event{Seq: s.seq + 1, RunID: id, Type: typ, Kind: kind, At: time.Now().UTC(), Data: data, Previous: s.hash}
	e.Hash = eventHash(e)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	n, err := s.file.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = s.file.Sync()
	}
	if err != nil {
		s.writeErr = err
		return err
	}
	if err = s.apply(e); err != nil {
		s.writeErr = err
		return err
	}
	s.seq = e.Seq
	s.hash = e.Hash
	return nil
}

func cloneRun(r *Run) Run {
	b, _ := json.Marshal(r)
	var out Run
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Store) Get(id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil {
		return Run{}, ErrNotFound
	}
	return cloneRun(r), nil
}
func (s *Store) List() []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, cloneRun(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) Create(b Brief, appName, parentID string, sample bool) (Run, error) {
	if err := b.Validate(); err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	taskID := newID("task")
	if parentID != "" {
		p := s.runs[parentID]
		if p == nil {
			return Run{}, ErrNotFound
		}
		taskID = p.TaskID
	}
	r := Run{ID: newID("local"), TaskID: taskID, ParentID: parentID, Brief: b, AppName: appName, Status: "queued", Stage: "Ready to start", Summary: "The brief is saved. Waiting for an execution slot.", CreatedAt: time.Now().UTC(), Sample: sample}
	err := s.commit(r.ID, "effect", "run_created", change{Run: &r})
	if err != nil {
		return Run{}, err
	}
	return cloneRun(s.runs[r.ID]), nil
}

func (s *Store) Transition(id, kind, status, stage, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	return s.commit(id, "effect", kind, change{Status: status, Stage: stage, Summary: summary})
}
func (s *Store) RecordDecision(id, kind, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	return s.commit(id, "decision", kind, change{Summary: summary})
}
func (s *Store) Complete(id, summary string, verified bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	return s.commit(id, "effect", "run_finished", change{Status: "done", Stage: "Complete", Summary: summary, Verified: verified})
}
func (s *Store) Park(id string, d *HumanDecision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	if d == nil || d.Reversibility != "external" || len(d.Options) < 2 || len(d.Options) > 3 {
		return errors.New("invalid external decision")
	}
	return s.commit(id, "effect", "approval_requested", change{Status: "parked", Stage: "Waiting for you", Summary: d.Context, Decision: d})
}
func (s *Store) Resolve(id, decisionID, choice string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Decision == nil || r.Decision.ID != decisionID {
		return ErrConflict
	}
	if r.Decision.Choice != "" {
		if r.Decision.Choice == choice {
			return nil
		}
		return ErrConflict
	}
	if r.Status != "parked" {
		return ErrConflict
	}
	valid := false
	for _, o := range r.Decision.Options {
		if o.ID == choice {
			valid = true
		}
	}
	if !valid {
		return errors.New("choose one of the available options")
	}
	return s.commit(id, "effect", "decision_resolved", change{Choice: choice, Status: "cancelled", Stage: "Closed", Summary: "Decision recorded: " + choice + ". The previous attempt is preserved."})
}

func (s *Store) SaveArtifact(id, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." {
		return errors.New("invalid artifact name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	for _, a := range r.Artifacts {
		if a.Name == name {
			return errors.New("artifact already exists")
		}
	}
	dir := filepath.Join(s.dir, "artifacts", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, bytes.NewReader(data))
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Persist the file name and newly created parent directories before the
	// event can promise that this artifact is durably available.
	for _, path := range []string{dir, filepath.Dir(dir), s.dir} {
		if err := syncDirectory(path); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(data)
	return s.commit(id, "effect", "artifact_harvested", change{Artifact: &Artifact{Name: name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}})
}
func (s *Store) Artifact(id, name string) ([]byte, error) {
	r, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	for _, a := range r.Artifacts {
		if a.Name == name {
			b, err := os.ReadFile(filepath.Join(s.dir, "artifacts", id, name))
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) != a.SHA256 {
				return nil, errors.New("artifact checksum mismatch")
			}
			return b, nil
		}
	}
	return nil, ErrNotFound
}
