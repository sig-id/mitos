package studio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mitos/internal/autonomy"
	"mitos/internal/workpool"
)

func dataProducer(t *testing.T, s *Store, name, body string) Run {
	t.Helper()
	r := createRun(t, s)
	if err := s.SaveArtifact(r.ID, name, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(r.ID, "Produced data", true); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(r.ID)
	return r
}
func inputBrief(root, app string, source Run, name string) Brief {
	b := testBrief(root)
	b.AppID = app
	b.Scheduling.Deps = []workpool.Dependency{{From: source.TaskID, Payload: "data", Guard: "done", Artifacts: []string{name}}}
	return b
}
func TestDataDependenciesCopyExactProducerAndPreserveSource(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `cat; cat ../inputs/task_*/subject.txt > result.txt`)
	e, s := testEngine(t, root, []Application{app})
	source := dataProducer(t, s, "subject.txt", "original subject")
	run, err := e.Launch(inputBrief(root, app.ID, source, "subject.txt"), "")
	if err != nil {
		t.Fatal(err)
	}
	run = waitRecovery(t, s, run.ID)
	if run.Status != "done" {
		t.Fatalf("data consumption failed: %s", run.Summary)
	}
	task, _ := s.Task(run.TaskID)
	if len(task.DependencyEvidence) != 1 || task.DependencyEvidence[0].Artifacts[0].SHA256 != source.Artifacts[0].SHA256 {
		t.Fatal("artifact not bound at admission")
	}
	output, err := s.Artifact(run.ID, "output.txt")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Inputs []DependencyInput `json:"inputs"`
	}
	if json.Unmarshal(output, &input) != nil || len(input.Inputs) != 1 {
		t.Fatal("missing input affordance")
	}
	path := filepath.Join(input.Inputs[0].Directory, "subject.txt")
	copied, err := os.ReadFile(path)
	if err != nil || string(copied) != "original subject" {
		t.Fatal("copied bytes differ")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0444 {
		t.Fatal("inputs were not marked read-only")
	}
	if strings.HasPrefix(path, filepath.Join(s.Dir(), "artifacts")) {
		t.Fatal("worker received producer artifact location")
	}
	patch, _ := s.Artifact(run.ID, "changes.patch")
	if !strings.Contains(string(patch), "+original subject") {
		t.Fatal("consumer did not use the input")
	}
	if _, err = os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("source workspace changed")
	}
	if _, err = s.Artifact(run.ID, "dependency-inputs.json"); err != nil {
		t.Fatal("input manifest not retained")
	}
	e.Close()
	before, _ := s.Task(run.TaskID)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, _ := reopened.Task(run.TaskID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("input binding did not replay")
	}
}
func TestDataMissingAndSoftInputsAtAdmission(t *testing.T) {
	for _, soft := range []bool{false, true} {
		t.Run(map[bool]string{false: "hard", true: "soft"}[soft], func(t *testing.T) {
			s := testStore(t)
			producer := dataProducer(t, s, "present", "data")
			b := inputBrief(".", "workspace-inventory", producer, "missing")
			if soft {
				b.Scheduling.Deps[0].Strength = "soft"
			}
			r, err := s.Create(b, "Inventory", "", false)
			if err != nil {
				t.Fatal(err)
			}
			ok, err := s.Schedule(r.ID, nil)
			if err != nil {
				t.Fatal(err)
			}
			latest, _ := s.Get(r.ID)
			task, _ := s.Task(r.TaskID)
			if soft {
				if !ok || task.DependencyEvidence[0].InputOmitted != "artifact_missing" {
					t.Fatal("optional input blocked or lacked omission")
				}
			} else if ok || latest.Status != "failed" || latest.Claim != nil || len(task.Runs) != 0 {
				t.Fatal("missing hard input started execution")
			}
		})
	}
}
func TestDataBindingSurvivesProducerRetryAndRejectsChangedBytes(t *testing.T) {
	s := testStore(t)
	producer := dataProducer(t, s, "subject", "first")
	b := inputBrief(".", "unused", producer, "subject")
	r, err := s.Create(b, "Unused", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	task, _ := s.Task(r.TaskID)
	w := task.DependencyEvidence[0]
	retry, err := s.Create(producer.Brief, producer.AppName, producer.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveArtifact(retry.ID, "subject", []byte("second")); err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(retry.ID, "Done", true); err != nil {
		t.Fatal(err)
	}
	data, err := s.boundInput(w, w.Artifacts[0])
	if err != nil || string(data) != "first" {
		t.Fatal("producer retry replaced frozen input")
	}
	path := filepath.Join(s.Dir(), "artifacts", producer.ID, "subject")
	if err = os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.boundInput(w, w.Artifacts[0]); err == nil {
		t.Fatal("same-size corruption accepted")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(filepath.Join(s.Dir(), "artifacts", retry.ID, "subject"), path); err != nil {
		t.Fatal(err)
	}
	if _, err = s.boundInput(w, w.Artifacts[0]); err == nil {
		t.Fatal("artifact symlink accepted")
	}
}
func TestChangedDataInputsParkAfterHarvestAndCanRecover(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `request=$(cat); case "$request" in *'"number":1'*) chmod u+w ../inputs/task_*/subject; for file in ../inputs/task_*/subject; do printf altered > "$file"; done;; esac; cat ../inputs/task_*/subject > result.txt`)
	app.Supervision = &autonomy.Supervision{Strategy: "rewind_to_green", MaxRestarts: limit(2)}
	e, s := testEngine(t, root, []Application{app})
	producer := dataProducer(t, s, "subject", "original")
	run, err := e.Launch(inputBrief(root, app.ID, producer, "subject"), "")
	if err != nil {
		t.Fatal(err)
	}
	run = waitRecovery(t, s, run.ID)
	if run.Status != "parked" || run.Verified || run.Decision.ReasonSignal != "dependency_inputs_changed" || len(run.Segments) != 1 {
		t.Fatalf("input mutation accepted/retried automatically: %s %s", run.Status, run.Summary)
	}
	if _, err = s.Artifact(run.ID, "changes.patch"); err != nil {
		t.Fatal("mutation lost the output patch")
	}
	original, err := s.Artifact(producer.ID, "subject")
	if err != nil || string(original) != "original" {
		t.Fatal("consumer changed producer bytes")
	}
	// The initial check is unknown here; explicit recovery chooses that exact baseline.
	request := autonomy.Rewind{CheckpointID: run.Checkpoints[0].ID, Scope: "step", FromSegment: 1, Memo: "Rebuild input copies from the pinned producer.", ReasonSignal: "operator_recovery", AllowNonGreen: true}
	e.Close()
	if _, err = s.BeginRewind(run.ID, request); err != nil {
		t.Fatal(err)
	}
	e2, err := NewEngine(s, []Application{app}, root)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	final := waitRecovery(t, s, run.ID)
	if final.Status != "done" || len(final.Segments) != 2 {
		t.Fatalf("input recovery failed: %s", final.Summary)
	}
	restored, err := os.ReadFile(filepath.Join(s.Dir(), "workspaces", run.ID, "segments", "segment-002", "inputs", producer.TaskID, "subject"))
	if err != nil || string(restored) != "original" {
		t.Fatal("recovery reused mutated input")
	}
}
func TestDataCaptureIncludesGitNamesAndDetectsChanges(t *testing.T) {
	s := testStore(t)
	producer := dataProducer(t, s, ".git", "data")
	r, err := s.Create(inputBrief(".", "unused", producer, ".git"), "Unused", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	e := Engine{Store: s}
	base := t.TempDir()
	inputs, before, err := e.prepareDependencyInputs(context.Background(), r, base, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Files) != 1 || !strings.HasSuffix(before.Files[0].Path, "/.git") {
		t.Fatal("Git-named input disappeared from fingerprint")
	}
	if err = os.WriteFile(filepath.Join(inputs[0].Directory, "extra"), []byte("change"), 0600); err != nil {
		t.Fatal(err)
	}
	if verifyDependencyInputs(context.Background(), base, before) == nil {
		t.Fatal("added input data not detected")
	}
}

func TestCorruptDependencyFailsBeforeWorkerStart(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf should-not-run > result.txt`)
	e, s := testEngine(t, root, []Application{app})
	producer := dataProducer(t, s, "subject", "original")
	if err := os.WriteFile(filepath.Join(s.Dir(), "artifacts", producer.ID, "subject"), []byte("modified"), 0600); err != nil {
		t.Fatal(err)
	}
	run, err := e.Launch(inputBrief(root, app.ID, producer, "subject"), "")
	if err != nil {
		t.Fatal(err)
	}
	run = waitRecovery(t, s, run.ID)
	if run.Status != "failed" || !strings.Contains(run.Summary, "checksum mismatch") {
		t.Fatalf("corrupt data accepted: %s", run.Summary)
	}
	for _, event := range run.Events {
		if event.Kind == "provider_status" {
			t.Fatal("worker launched with corrupt input")
		}
	}
}
func TestDataSizeLimitAndUnselectedRaceInputs(t *testing.T) {
	s := testStore(t)
	first := dataProducer(t, s, "present", "data")
	second := dataProducer(t, s, "other", "data")
	b := inputBrief(".", "unused", first, "present")
	b.Scheduling.Join = workpool.Join{Rule: "any"}
	b.Scheduling.Deps = append(b.Scheduling.Deps, workpool.Dependency{From: second.TaskID, Payload: "data", Guard: "done", Artifacts: []string{"missing"}})
	run, err := s.Create(b, "Unused", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal("losing branch blocked input binding")
	}
	task, _ := s.Task(run.TaskID)
	if len(task.DependencyEvidence) != 1 || task.DependencyEvidence[0].TaskID != first.TaskID {
		t.Fatal("race inputs included non-winning producer")
	}
	s.mu.Lock()
	s.runs[first.ID].Artifacts[0].Bytes = dependencyBytes + 1
	s.mu.Unlock()
	run, err = s.Create(inputBrief(".", "unused", first, "present"), "Unused", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(run.ID, nil); err != nil || ok {
		t.Fatal("oversized input admitted")
	}
	latest, _ := s.Get(run.ID)
	if latest.Status != "failed" || latest.Claim != nil {
		t.Fatal("size rejection happened after dispatch")
	}
}

func TestVerifierCannotAcceptChangedDependencyCopies(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `cat ../inputs/task_*/subject > result.txt`)
	app.Acceptance.Cmd = []string{"/bin/sh", "-c", `test -s "$MITOS_WORKSPACE/result.txt" || exit 1; for file in "$MITOS_WORKSPACE"/../../../inputs/task_*/subject; do chmod u+w "$file"; printf changed > "$file"; done`}
	e, s := testEngine(t, root, []Application{app})
	source := dataProducer(t, s, "subject", "original")
	run, err := e.Launch(inputBrief(root, app.ID, source, "subject"), "")
	if err != nil {
		t.Fatal(err)
	}
	run = waitRecovery(t, s, run.ID)
	if run.Status != "parked" || run.Acceptance != nil || run.Verified || run.Decision.ReasonSignal != "dependency_inputs_changed" {
		t.Fatalf("verifier mutation accepted: %s %s", run.Status, run.Summary)
	}
}
