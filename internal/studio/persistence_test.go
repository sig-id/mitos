package studio

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// controlledLog reproduces an indefinitely stalled disk without relying on
// filesystem speed. Only the first selected operation stalls.
type controlledLog struct {
	eventLog
	write   bool
	entered chan struct{}
	release chan struct{}
	failure error
	writes  atomic.Int64
	syncs   atomic.Int64
}

func (f *controlledLog) pause() error {
	if f.entered != nil {
		close(f.entered)
		<-f.release
	}
	return f.failure
}
func (f *controlledLog) Write(b []byte) (int, error) {
	if f.writes.Add(1) == 1 && f.write {
		if err := f.pause(); err != nil {
			return 0, err
		}
	}
	return f.eventLog.Write(b)
}
func (f *controlledLog) Sync() error {
	if f.syncs.Add(1) == 1 && !f.write {
		if err := f.pause(); err != nil {
			return err
		}
	}
	return f.eventLog.Sync()
}

func awaitPersistence[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("operation blocked on stalled persistence")
		var zero T
		return zero
	}
}

func TestReadsRemainResponsiveDuringPersistence(t *testing.T) {
	for _, phase := range []string{"write", "sync"} {
		t.Run(phase, func(t *testing.T) {
			s := testStore(t)
			r := createRun(t, s)
			log := &controlledLog{eventLog: s.file, write: phase == "write", entered: make(chan struct{}), release: make(chan struct{})}
			s.file = log
			var release sync.Once
			defer release.Do(func() { close(log.release) })
			first := make(chan error, 1)
			go func() {
				first <- s.TransitionAfterDecision(r.ID, "advance", "Prepare", "run_started", "running", "Preparing", "Starting")
			}()
			awaitPersistence(t, log.entered)

			// Queue more writers as well: they must wait outside the read lock.
			const writers = 8
			queued := make(chan struct{}, writers)
			finished := make(chan error, writers)
			for range writers {
				go func() {
					queued <- struct{}{}
					finished <- s.RecordDecision(r.ID, "observe", "Queued writer")
				}()
			}
			for range writers {
				awaitPersistence(t, queued)
			}
			read := make(chan error, 1)
			go func() {
				got, err := s.Get(r.ID)
				if err == nil && !reflect.DeepEqual(got, r) {
					err = errors.New("unsynced run became visible")
				}
				task, taskErr := s.Task(r.TaskID)
				if taskErr != nil || task.Status != "queued" {
					err = errors.New("unsynced task became visible")
				}
				_ = s.List()
				_ = s.Tasks()
				_ = s.Pools()
				_ = s.FleetIndex()
				_, fleetErr := s.FleetEvidence(r.ID)
				read <- errors.Join(err, fleetErr)
			}()
			if err := awaitPersistence(t, read); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-first:
				t.Fatalf("execution barrier returned before sync: %v", err)
			default:
			}
			release.Do(func() { close(log.release) })
			if err := awaitPersistence(t, first); err != nil {
				t.Fatal(err)
			}
			for range writers {
				if err := awaitPersistence(t, finished); err != nil {
					t.Fatal(err)
				}
			}
			if log.syncs.Load() != writers+1 {
				t.Fatalf("decision and progress did not share a flush: %d", log.syncs.Load())
			}
			before, _ := s.Get(r.ID)
			if before.Status != "running" || len(before.Events) != len(r.Events)+writers+2 {
				t.Fatalf("lost or reordered records: %+v", before)
			}
			assertPersistenceReplay(t, s, before)
		})
	}
}

func assertPersistenceReplay(t *testing.T, s *Store, before Run) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.Get(before.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("replay changed durable state: %v\nbefore=%+v\nafter=%+v", err, before, after)
	}
}

func TestFailedPersistenceDoesNotPublishOrPermitMoreWrites(t *testing.T) {
	for _, phase := range []string{"write", "sync"} {
		t.Run(phase, func(t *testing.T) {
			s := testStore(t)
			r := createRun(t, s)
			failure := errors.New("disk unavailable")
			log := &controlledLog{eventLog: s.file, write: phase == "write", failure: failure}
			s.file = log
			err := s.TransitionAfterDecision(r.ID, "advance", "Prepare", "run_started", "running", "Preparing", "Starting")
			if !errors.Is(err, failure) {
				t.Fatalf("lost persistence error: %v", err)
			}
			got, _ := s.Get(r.ID)
			if !reflect.DeepEqual(got, r) {
				t.Fatal("failed batch was published")
			}
			if err := s.Complete(r.ID, "Done", true); !errors.Is(err, failure) {
				t.Fatalf("allowed completion after persistence failure: %v", err)
			}
			if log.writes.Load() != 1 {
				t.Fatal("wrote beyond uncertain log head")
			}
		})
	}
}

func TestArtifactBatchPublishesAfterOneLogFlush(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	log := &controlledLog{eventLog: s.file, entered: make(chan struct{}), release: make(chan struct{})}
	s.file = log
	var release sync.Once
	defer release.Do(func() { close(log.release) })
	artifacts := []ArtifactData{{Name: "inventory.json", Data: []byte("{}")}, {Name: "report.md", Data: []byte("Report")}}
	done := make(chan error, 1)
	go func() { done <- s.SaveArtifacts(r.ID, artifacts...) }()
	awaitPersistence(t, log.entered)
	read := make(chan error, 1)
	go func() {
		_, err := s.Artifact(r.ID, artifacts[0].Name)
		read <- err
	}()
	if err := awaitPersistence(t, read); !errors.Is(err, ErrNotFound) {
		t.Fatalf("artifact visible before metadata synced: %v", err)
	}
	release.Do(func() { close(log.release) })
	if err := awaitPersistence(t, done); err != nil {
		t.Fatal(err)
	}
	for _, a := range artifacts {
		data, err := s.Artifact(r.ID, a.Name)
		if err != nil || string(data) != string(a.Data) {
			t.Fatalf("artifact %s: %s %v", a.Name, data, err)
		}
	}
	if log.writes.Load() != 1 || log.syncs.Load() != 1 {
		t.Fatalf("artifact log writes=%d syncs=%d", log.writes.Load(), log.syncs.Load())
	}
	// Validate the whole batch before creating any file.
	if err := s.SaveArtifacts(r.ID, ArtifactData{Name: "new.txt"}, artifacts[0]); err == nil {
		t.Fatal("duplicate artifact accepted")
	}
	if _, err := os.Stat(filepath.Join(s.Dir(), "artifacts", r.ID, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid batch created a file: %v", err)
	}
	before, _ := s.Get(r.ID)
	assertPersistenceReplay(t, s, before)
}

func TestBatchedEventsRecoverAtRecordBoundaries(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	if err := s.TransitionAfterDecision(r.ID, "advance", "Prepare", "run_started", "running", "Preparing", "Starting"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	// A crash can leave any complete prefix of a batched write. Recovery must
	// retain a durable decision without inventing its missing execution marker.
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "decision_only", true: "partial_effect"}[partial], func(t *testing.T) {
			dir := t.TempDir()
			prefix := append(append([]byte{}, lines[0]...), lines[1]...)
			torn := append([]byte{}, prefix...)
			if partial {
				torn = append(torn, lines[2][:len(lines[2])/2]...)
			}
			path := filepath.Join(dir, "events.jsonl")
			if err := os.WriteFile(path, torn, 0600); err != nil {
				t.Fatal(err)
			}
			recovered, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			got, err := recovered.Get(r.ID)
			if err != nil || got.Status != "queued" || len(got.Events) != 2 || got.Events[1].Kind != "advance" {
				t.Fatalf("incorrect partial-batch recovery: %+v %v", got, err)
			}
			remaining, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(remaining, prefix) {
				t.Fatalf("incorrect tail repair: %v", err)
			}
			if err := recovered.Transition(r.ID, "run_started", "running", "Preparing", "Starting"); err != nil {
				t.Fatal(err)
			}
			before, _ := recovered.Get(r.ID)
			assertPersistenceReplay(t, recovered, before)
		})
	}
}

func TestParkPublishesStatusAndDecisionTogether(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	log := &controlledLog{eventLog: s.file, entered: make(chan struct{}), release: make(chan struct{})}
	s.file = log
	var release sync.Once
	defer release.Do(func() { close(log.release) })
	done := make(chan error, 1)
	go func() { done <- s.Park(r.ID, recoveryDecision()) }()
	awaitPersistence(t, log.entered)
	read := make(chan Run, 1)
	go func() { got, _ := s.Get(r.ID); read <- got }()
	if got := awaitPersistence(t, read); !reflect.DeepEqual(got, r) {
		t.Fatal("parked status or card appeared before sync")
	}
	release.Do(func() { close(log.release) })
	if err := awaitPersistence(t, done); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	if got.Status != "parked" || got.Decision == nil || got.PendingDecision != nil || log.syncs.Load() != 1 {
		t.Fatalf("status and decision were not published together: status=%s decision=%v syncs=%d", got.Status, got.Decision, log.syncs.Load())
	}
	assertPersistenceReplay(t, s, got)
}
