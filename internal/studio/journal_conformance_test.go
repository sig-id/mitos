package studio

import (
	"bytes"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// This harness is reusable by the SQLite adapter at cutover. Projection reads
// are test inspection, not a second Store read API or lifecycle implementation.
type journalHarness struct {
	open       func() journal
	projection func(namespace, key string) ([]byte, bool)
}

func journalConformance(t *testing.T, factory func(*testing.T) journalHarness) {
	t.Helper()
	t.Run("ordered replay and reopen", func(t *testing.T) {
		h := factory(t)
		j := h.open()
		defer func() { _ = j.Close() }()
		one := conformanceEvent(1, "", "first")
		two := conformanceEvent(2, one.Hash, "second")
		if err := j.Append([]Event{one, two}, []journalProjection{{Namespace: "cursor", Key: "run", Value: []byte("2")}}); err != nil {
			t.Fatal(err)
		}
		assertJournalReplay(t, j, []Event{one, two})
		if got, ok := h.projection("cursor", "run"); !ok || string(got) != "2" {
			t.Fatal("lost projection", string(got), ok)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		j = h.open()
		assertJournalReplay(t, j, []Event{one, two})
		if got, ok := h.projection("cursor", "run"); !ok || string(got) != "2" {
			t.Fatal("projection lost on reopen", string(got), ok)
		}
		three := conformanceEvent(3, two.Hash, "third")
		if err := j.Append([]Event{three}, []journalProjection{{Namespace: "cursor", Key: "run", Value: []byte("3")}}); err != nil {
			t.Fatal(err)
		}
		assertJournalReplay(t, j, []Event{one, two, three})
	})
	t.Run("reject batch without publishing prefix or projections", func(t *testing.T) {
		for _, fault := range []string{"sequence", "duplicate_sequence", "previous", "hash", "projection", "missing_events"} {
			t.Run(fault, func(t *testing.T) {
				h := factory(t)
				j := h.open()
				defer j.Close()
				one := conformanceEvent(1, "", "first")
				if err := j.Append([]Event{one}, []journalProjection{{Namespace: "cursor", Key: "run", Value: []byte("1")}}); err != nil {
					t.Fatal(err)
				}
				two := conformanceEvent(2, one.Hash, "second")
				three := conformanceEvent(3, two.Hash, "third")
				projections := []journalProjection{{Namespace: "cursor", Key: "run", Value: []byte("3")}}
				switch fault {
				case "sequence":
					three.Seq++
					three.Hash = eventHash(three)
				case "duplicate_sequence":
					three.Seq = one.Seq
					three.Hash = eventHash(three)
				case "previous":
					three.Previous = "unrelated"
					three.Hash = eventHash(three)
				case "hash":
					three.Data[0] ^= 1
				case "projection":
					projections = append(projections, journalProjection{Key: "missing_namespace", Value: []byte("bad")})
				}
				events := []Event{two, three}
				if fault == "missing_events" {
					events = nil
				}
				if err := j.Append(events, projections); err == nil {
					t.Fatal("invalid batch accepted")
				}
				assertJournalReplay(t, j, []Event{one})
				if got, ok := h.projection("cursor", "run"); !ok || string(got) != "1" {
					t.Fatal("projection changed after rejected append", string(got), ok)
				}
				if err := j.Append([]Event{two}, nil); err != nil {
					t.Fatal("rejected batch advanced the head", err)
				}
			})
		}
	})
	t.Run("projection deletion and empty value", func(t *testing.T) {
		h := factory(t)
		j := h.open()
		defer j.Close()
		one := conformanceEvent(1, "", "first")
		if err := j.Append([]Event{one}, []journalProjection{{Namespace: "keys", Key: "delete", Value: []byte("one")}, {Namespace: "keys", Key: "empty", Value: []byte{}}}); err != nil {
			t.Fatal(err)
		}
		two := conformanceEvent(2, one.Hash, "second")
		if err := j.Append([]Event{two}, []journalProjection{{Namespace: "keys", Key: "delete"}}); err != nil {
			t.Fatal(err)
		}
		if _, ok := h.projection("keys", "delete"); ok {
			t.Fatal("projection not deleted")
		}
		if got, ok := h.projection("keys", "empty"); !ok || len(got) != 0 {
			t.Fatal("empty projection confused with deletion")
		}
	})
	t.Run("immutable inputs and replay values", func(t *testing.T) {
		h := factory(t)
		j := h.open()
		defer j.Close()
		one := conformanceEvent(1, "", "first")
		events := []Event{graphClone(one)}
		value := []byte("1")
		if err := j.Append(events, []journalProjection{{Namespace: "cursor", Key: "run", Value: value}}); err != nil {
			t.Fatal(err)
		}
		events[0].Data[0] ^= 1
		value[0] = '9'
		if err := j.Replay(func(e Event) error { e.Data[0] ^= 1; return nil }); err != nil {
			t.Fatal(err)
		}
		assertJournalReplay(t, j, []Event{one})
		if got, _ := h.projection("cursor", "run"); string(got) != "1" {
			t.Fatal("caller mutated a projection")
		}
	})
	t.Run("visitor failure and closed journal", func(t *testing.T) {
		h := factory(t)
		j := h.open()
		defer j.Close()
		one := conformanceEvent(1, "", "first")
		if err := j.Append([]Event{one}, nil); err != nil {
			t.Fatal(err)
		}
		stop := errors.New("stop replay")
		if err := j.Replay(func(Event) error { return stop }); !errors.Is(err, stop) {
			t.Fatal("lost visitor error", err)
		}
		assertJournalReplay(t, j, []Event{one})
		if err := j.Append(nil, nil); err != nil {
			t.Fatal("empty batch", err)
		}
		if err := j.Close(); err != nil {
			t.Fatal(err)
		}
		if err := j.Append(nil, nil); err == nil {
			t.Fatal("append after close")
		}
		if err := j.Replay(func(Event) error { return nil }); err == nil {
			t.Fatal("replay after close")
		}
	})
}

func conformanceEvent(seq int64, previous, kind string) Event {
	e := Event{Seq: seq, RunID: "run", Type: "effect", Kind: kind, At: time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC), Data: []byte(`{"summary":"evidence"}`), Previous: previous}
	e.Hash = eventHash(e)
	return e
}

func assertJournalReplay(t *testing.T, j journal, expected []Event) {
	t.Helper()
	var events []Event
	if err := j.Replay(func(e Event) error { events = append(events, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, expected) {
		t.Fatalf("replay mismatch: got %+v, want %+v", events, expected)
	}
}

// The in-memory fake models a transactional journal, not file IO. Keep it test
// only: it must never become an operator-selectable runtime adapter.
type memoryJournalState struct {
	mu          sync.Mutex
	events      []Event
	projections map[[2]string][]byte
}

type memoryJournal struct {
	state  *memoryJournalState
	closed bool
}

func (j *memoryJournal) Replay(visit func(Event) error) error {
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	if j.closed {
		return errJournalClosed
	}
	var seq int64
	var previous string
	for _, saved := range j.state.events {
		e := graphClone(saved)
		if err := validateJournalEvent(e, seq, previous); err != nil {
			return err
		}
		if err := visit(e); err != nil {
			return err
		}
		seq, previous = e.Seq, e.Hash
	}
	return nil
}

func (j *memoryJournal) Append(events []Event, projections []journalProjection) error {
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	if j.closed {
		return errJournalClosed
	}
	if len(events) == 0 && len(projections) > 0 {
		return errors.New("projections require journal events")
	}
	var seq int64
	var previous string
	if len(j.state.events) > 0 {
		tail := j.state.events[len(j.state.events)-1]
		seq, previous = tail.Seq, tail.Hash
	}
	for _, e := range events {
		if err := validateJournalEvent(e, seq, previous); err != nil {
			return err
		}
		seq, previous = e.Seq, e.Hash
	}
	for _, p := range projections {
		if p.Namespace == "" || p.Key == "" {
			return errors.New("invalid projection")
		}
	}
	// All validation precedes the only publication point.
	j.state.events = append(j.state.events, graphClone(events)...)
	if j.state.projections == nil {
		j.state.projections = map[[2]string][]byte{}
	}
	for _, p := range projections {
		key := [2]string{p.Namespace, p.Key}
		if p.Value == nil {
			delete(j.state.projections, key)
		} else {
			j.state.projections[key] = bytes.Clone(p.Value)
		}
	}
	return nil
}

func (j *memoryJournal) Close() error {
	j.state.mu.Lock()
	defer j.state.mu.Unlock()
	j.closed = true
	return nil
}

func TestMemoryJournalConformance(t *testing.T) {
	journalConformance(t, func(*testing.T) journalHarness {
		state := &memoryJournalState{}
		return journalHarness{
			open: func() journal { return &memoryJournal{state: state} },
			projection: func(namespace, key string) ([]byte, bool) {
				state.mu.Lock()
				defer state.mu.Unlock()
				value, ok := state.projections[[2]string{namespace, key}]
				return bytes.Clone(value), ok
			},
		}
	})
}
