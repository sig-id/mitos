package studio

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mitos/internal/failpoint"
)

// Real subprocess death exercises the same boundaries the later application and
// worker fixtures use. It must not rely on defers, clean Close, or a fake error.
func TestStoreCommitCrashRecovery(t *testing.T) {
	if dir := os.Getenv("MITOS_JOURNAL_CRASH_CHILD"); dir != "" {
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		run := os.Getenv("MITOS_JOURNAL_CRASH_RUN")
		if os.Getenv("MITOS_JOURNAL_CRASH_BATCH") == "1" {
			err = s.TransitionAfterDecision(run, "advance", "Prepared", "run_started", "running", "Started", "Atomic batch")
		} else if os.Getenv("MITOS_JOURNAL_CRASH_ARTIFACT") == "1" {
			err = s.SaveArtifact(run, "report.txt", []byte("retained evidence"))
		} else {
			err = s.RecordDecision(run, "advance", "crash boundary")
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal("selected failpoint did not crash")
	}
	for _, point := range []string{"journal.before_append", "journal.after_append", "artifacts.after_put", "sqlite.before_commit", "sqlite.after_commit"} {
		t.Run(point, func(t *testing.T) {
			t.Setenv("MITOS_ENABLE_FAILPOINTS", "0")
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			r := createRun(t, s)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("MITOS_JOURNAL_CRASH_CHILD", dir)
			t.Setenv("MITOS_JOURNAL_CRASH_RUN", r.ID)
			t.Setenv("MITOS_JOURNAL_CRASH_ARTIFACT", "0")
			t.Setenv("MITOS_JOURNAL_CRASH_BATCH", "0")
			if strings.HasPrefix(point, "sqlite.") {
				t.Setenv("MITOS_JOURNAL_CRASH_BATCH", "1")
			}
			if point == "artifacts.after_put" {
				t.Setenv("MITOS_JOURNAL_CRASH_ARTIFACT", "1")
			}
			t.Setenv("MITOS_ENABLE_FAILPOINTS", "1")
			t.Setenv("MITOS_FAILPOINT", point)
			cmd := exec.Command(os.Args[0], "-test.run=^TestStoreCommitCrashRecovery$")
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != failpoint.ExitCode {
				t.Fatalf("expected crash %d, got %v\n%s", failpoint.ExitCode, err, output)
			}
			t.Setenv("MITOS_ENABLE_FAILPOINTS", "0")
			recovered, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			got, err := recovered.Get(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if point == "sqlite.after_commit" {
				if len(got.Events) != len(r.Events)+2 || got.Status != "running" {
					t.Fatal("partial SQLite transaction recovered", got)
				}
			} else if point == "journal.after_append" {
				if len(got.Events) != len(r.Events)+1 || got.Events[len(got.Events)-1].Kind != "advance" {
					t.Fatal("durable commit lost after crash", got.Events)
				}
			} else if !reflect.DeepEqual(got, r) {
				t.Fatal("uncommitted state became visible on restart")
			}
			if point == "artifacts.after_put" {
				if _, err := os.Stat(filepath.Join(dir, "artifacts", r.ID, "report.txt")); err != nil {
					t.Fatal("artifact not persisted before crash", err)
				}
				if _, err := recovered.Artifact(r.ID, "report.txt"); !errors.Is(err, ErrNotFound) {
					t.Fatal("uncommitted artifact accessible", err)
				}
				if err := recovered.saveArtifacts(r.ID, true, ArtifactData{Name: "report.txt", Data: []byte("reconstructed")}); err != nil {
					t.Fatal("could not reconstruct uncommitted artifact", err)
				}
			}
			if err := recovered.RecordDecision(r.ID, "observe", "healthy after recovery"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A journal is allowed to return an ambiguous error after its commit. Store must
// not publish it, write again, or lose the durable event when the journal reopens.
func TestAmbiguousJournalFailureRecoversDurableTail(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	failure := errors.New("commit acknowledgement lost")
	s.journal = &controlledJournal{journal: s.journal, failure: failure}
	if err := s.RecordDecision(r.ID, "advance", "durable but unacknowledged"); !errors.Is(err, failure) {
		t.Fatal("lost commit error", err)
	}
	if got, _ := s.Get(r.ID); !reflect.DeepEqual(got, r) {
		t.Fatal("ambiguous commit published before recovery")
	}
	if err := s.RecordDecision(r.ID, "observe", "must not write"); !errors.Is(err, failure) {
		t.Fatal("writer did not latch ambiguous failure", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	got, _ := recovered.Get(r.ID)
	if len(got.Events) != len(r.Events)+1 || got.Events[len(got.Events)-1].Kind != "advance" {
		t.Fatal("did not recover committed tail", got.Events)
	}
}
