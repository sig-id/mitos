package studio

import (
	"io"
	"sync"
)

type eventLog interface {
	io.Reader
	io.Writer
	io.Seeker
	Sync() error
	Truncate(int64) error
	Close() error
}

// Writers serialize before taking the state lock, so a writer waiting behind
// disk IO cannot block readers. io releases only the state lock; its callback
// must not access the replayed state. The writer gate protects the log itself.
type storeMutex struct {
	writer sync.Mutex
	state  sync.RWMutex
}

func (m *storeMutex) Lock()    { m.writer.Lock(); m.state.Lock() }
func (m *storeMutex) Unlock()  { m.state.Unlock(); m.writer.Unlock() }
func (m *storeMutex) RLock()   { m.state.RLock() }
func (m *storeMutex) RUnlock() { m.state.RUnlock() }

func (m *storeMutex) io(fn func() error) error {
	m.state.Unlock()
	defer m.state.Lock()
	return fn()
}

// TransitionAfterDecision writes an authorization and its progress marker with
// one durability barrier. Both remain separate, ordered records in the log.
// The decision changes only the summary, so the transition can be derived from
// the same durable state. Neither is published until both have been synced.
func (s *Store) TransitionAfterDecision(id, decisionKind, decisionSummary, kind, status, stage, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() || (r.Status == "stopping" && status != "cancelled") {
		return ErrConflict
	}
	d, err := s.prepareEvent(id, "decision", decisionKind, change{Summary: decisionSummary}, s.seq+1, s.hash)
	if err != nil {
		return err
	}
	e, err := s.prepareEvent(id, "effect", kind, change{Status: status, Stage: stage, Summary: summary}, d.Seq+1, d.Hash)
	if err != nil {
		return err
	}
	return s.persist(d, e)
}
