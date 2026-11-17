package studio

import (
	"errors"
	"reflect"
	"testing"
)

// This exercises ordinary Store semantics through a non-file journal so future
// adapters cannot accidentally depend on file offsets, Sync or JSONL framing.
func TestStoreWithMemoryJournal(t *testing.T) {
	state := &memoryJournalState{}
	dir := t.TempDir()
	artifacts := &fileArtifactStore{dir: dir}
	s, err := openStoreWithJournal(dir, &memoryJournal{state: state}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	r := createRun(t, s)
	if err := s.TransitionAfterDecision(r.ID, "advance", "Prepare", "run_started", "running", "Preparing", "Starting"); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveArtifacts(r.ID, ArtifactData{Name: "report.txt", Data: []byte("a real result")}, ArtifactData{Name: "data.json", Data: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(r.ID, "Verified", true); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Get(r.ID)
	taskBefore, _ := s.Task(r.TaskID)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := openStoreWithJournal(dir, &memoryJournal{state: state}, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	after, _ := recovered.Get(r.ID)
	taskAfter, _ := recovered.Task(r.TaskID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(taskBefore, taskAfter) {
		t.Fatal("journal seam changed Task/Run replay")
	}
	data, err := recovered.Artifact(r.ID, "report.txt")
	if err != nil || string(data) != "a real result" {
		t.Fatal("artifact seam changed retained bytes", err)
	}
}

type unavailableArtifacts struct {
	artifactStore
	failure error
}

func (a unavailableArtifacts) Put(string, []ArtifactData, bool) error { return a.failure }
func (a unavailableArtifacts) Open(string, string) (artifactFile, error) {
	return nil, a.failure
}

func TestArtifactStoreFailureDoesNotPublishMetadata(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	saved := s.artifacts
	failure := errors.New("artifact storage unavailable")
	s.artifacts = unavailableArtifacts{artifactStore: saved, failure: failure}
	if err := s.SaveArtifact(r.ID, "report.txt", []byte("evidence")); !errors.Is(err, failure) {
		t.Fatal("lost artifact-store error", err)
	}
	if got, _ := s.Get(r.ID); !reflect.DeepEqual(got, r) {
		t.Fatal("artifact metadata committed despite failed byte storage")
	}
	// No journal IO was attempted, so repairing byte storage permits a retry.
	s.artifacts = saved
	if err := s.SaveArtifact(r.ID, "report.txt", []byte("evidence")); err != nil {
		t.Fatal(err)
	}
	s.artifacts = unavailableArtifacts{artifactStore: saved, failure: failure}
	if _, err := s.Artifact(r.ID, "report.txt"); !errors.Is(err, failure) {
		t.Fatal("artifact read bypassed adapter", err)
	}
}

func TestLegacyJournalRefusesProjectionWrites(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	before, _ := s.Get(r.ID)
	e := conformanceEvent(s.seq+1, s.hash, "unsupported")
	if err := s.journal.Append([]Event{e}, []journalProjection{{Namespace: "keys", Key: "request", Value: []byte("value")}}); err == nil {
		t.Fatal("legacy adapter silently ignored projections")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	after, _ := recovered.Get(r.ID)
	if !reflect.DeepEqual(before, after) || recovered.seq != s.seq {
		t.Fatal("refused projection write changed durable history")
	}
}
