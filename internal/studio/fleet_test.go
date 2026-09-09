package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"mitos/internal/acceptance"
	"mitos/internal/autonomy"
)

func TestFleetEvidenceExcludesNarrationAndBindsReplayPrefix(t *testing.T) {
	s := testStore(t)
	const private = "WORKER_PRIVATE_NARRATION_6fac"
	b := testBrief(".")
	b.Title = private
	b.Intent = private
	r, err := s.Create(b, private, "", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"output.txt", "workspace.tar.gz", "changes.patch", "acceptance.txt", private + ".txt"} {
		if err = s.SaveArtifact(r.ID, name, []byte(private)); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordEvidence(r.ID, "workspace_harvested", private, map[string]string{"rationale": private, "assistant_text": private}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordEvidence(r.ID, private, private, private); err != nil {
		t.Fatal(err)
	}
	v := acceptance.Verdict{Verdict: "pass", WorkspaceCommit: strings.Repeat("a", 40), EvidenceRef: "acceptance.txt", Measure: []float64{7}, Delta: []float64{2}, OracleIsolation: "same_user_process"}
	if err = s.RecordWorkspace(r.ID, "workspace_harvested", private, WorkspaceRecord{Mode: "copy", BaselineCommit: strings.Repeat("b", 40), Fingerprint: strings.Repeat("c", 64), ResultCommit: v.WorkspaceCommit, ChangedFiles: 1}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordAcceptance(r.ID, v); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	err = s.commit(r.ID, "effect", "checkpoint_taken", change{Checkpoint: &autonomy.Checkpoint{ID: "checkpoint-test", WorkspaceCommit: v.WorkspaceCommit, Predicate: v, Green: true}})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	d := recoveryDecision()
	d.Question = private
	d.Context.BlockedBy = private
	if err = s.Park(r.ID, d); err != nil {
		t.Fatal(err)
	}
	e, err := s.FleetEvidence(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(e)
	for _, forbidden := range []string{private, "output.txt", "workspace.tar.gz", "rationale", "assistant_text", "blocked_by\":\"", "\"data\"", "\"summary\""} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("leaked %q", forbidden)
		}
	}
	if e.Predicate == nil || e.Predicate.Measure[0] != 7 || e.Predicate.Evidence == nil || e.Predicate.Evidence.Name != "acceptance.txt" || len(e.Checkpoints) != 1 || len(e.Artifacts) != 2 {
		t.Fatalf("lost structured evidence: %s", data)
	}
	latest, _ := s.Get(r.ID)
	head := latest.Events[len(latest.Events)-1]
	if e.Run.Head.Hash != head.Hash || e.Run.Head.Seq != head.Seq {
		t.Fatal("evidence not bound to head")
	}
	digest := e.Digest
	e.Digest = ""
	unsigned, _ := json.Marshal(e)
	hash := sha256.Sum256(unsigned)
	if digest != hex.EncodeToString(hash[:]) {
		t.Fatal("incorrect content digest")
	}
	// Mutating the returned evidence must not mutate the authoritative fold.
	e.Predicate.Measure[0] = 999
	e.Checkpoints[0].Predicate.Measure[0] = 999
	again, err := s.FleetEvidence(r.ID)
	if err != nil || again.Predicate.Measure[0] != 7 || again.Digest != digest {
		t.Fatal("reader aliases store state")
	}
	if err = s.RecordEvidence(r.ID, "observation", private, private); err != nil {
		t.Fatal(err)
	}
	advanced, err := s.FleetEvidence(r.ID)
	if err != nil || advanced.Digest == digest {
		t.Fatal("new log prefix did not change evidence anchor")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, err := reopened.FleetEvidence(r.ID)
	if err != nil || replayed.Digest != advanced.Digest {
		t.Fatal("evidence changed on replay")
	}
}

func TestFleetEvidenceExplicitWindowsAndUnknownEnums(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	s.mu.Lock()
	for i := 0; i < fleetWindow+3; i++ {
		// Simulate a long folded history without coupling the view test to execution.
		s.runs[r.ID].Segments = append(s.runs[r.ID].Segments, autonomy.Segment{Number: i, Signal: "private narration"})
		s.runs[r.ID].Checkpoints = append(s.runs[r.ID].Checkpoints, autonomy.Checkpoint{ID: fmt.Sprint(i), Predicate: acceptance.Verdict{Verdict: "private narration", OracleIsolation: "private narration", EvidenceRef: "output.txt"}})
		if err := s.commit(r.ID, "effect", "segment_finished", change{Summary: "private narration"}); err != nil {
			s.mu.Unlock()
			t.Fatal(err)
		}
	}
	s.mu.Unlock()
	e, err := s.FleetEvidence(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"segments", "checkpoints", "controls"} {
		if e.Windows[name].Omitted != 3 || e.Windows[name].Total != fleetWindow+3 {
			t.Fatalf("missing truncation for %s: %+v", name, e.Windows[name])
		}
	}
	if len(e.Segments) != fleetWindow || e.Segments[0].Number != 3 || e.Checkpoints[0].Predicate.Verdict != "unknown" || e.Checkpoints[0].Predicate.OracleIsolation != "unknown" || e.Checkpoints[0].Predicate.Evidence != nil {
		t.Fatal("unbounded or unknown fields copied")
	}
	data, _ := json.Marshal(e)
	if strings.Contains(string(data), "private narration") {
		t.Fatal("raw enum copied")
	}
}
