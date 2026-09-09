package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mitos/internal/workpool"
)

func waitTask(t *testing.T, s *Store, id, status string) Task {
	t.Helper()
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		task, err := s.Task(id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == status {
			return task
		}
		time.Sleep(20 * time.Millisecond)
	}
	task, _ := s.Task(id)
	t.Fatalf("task status %s; wanted %s", task.Status, status)
	return task
}
func TestTasksBlockAcrossPoolsAndInjectAdmissionWitness(t *testing.T) {
	root := t.TempDir()
	aRoot, bRoot := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, p := range []string{aRoot, bRoot} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	gate := filepath.Join(t.TempDir(), "release")
	producer := executionApp(t, fmt.Sprintf(`while test ! -f %q; do sleep .05; done; printf result > result.txt`, gate))
	producer.ID = "producer"
	consumer := executionApp(t, `cat; printf result > result.txt`)
	consumer.ID = "consumer"
	e, s := testEngine(t, root, []Application{producer, consumer})
	a := launchApp(t, e, aRoot, producer)
	brief := testBrief(bRoot)
	brief.AppID = consumer.ID
	brief.Scheduling = workpool.Request{Pool: "batch", Deps: []workpool.Dependency{{From: a.TaskID, Guard: "done"}}}
	b, err := e.Launch(brief, "")
	if err != nil {
		t.Fatal(err)
	}
	task := waitTask(t, s, b.TaskID, "blocked")
	r, _ := s.Get(b.ID)
	if len(task.WaitingFor) != 1 || len(task.Runs) != 0 || r.Status != "queued" || r.Claim != nil || len(r.Segments) != 0 || len(r.ClaimConflicts) != 0 {
		t.Fatal("dependency gating occurred after execution/ownership admission")
	}
	before := len(r.Events)
	time.Sleep(450 * time.Millisecond)
	r, _ = s.Get(b.ID)
	if len(r.Events) != before {
		t.Fatal("blocked task rewrote unchanged state each tick")
	}
	if err = os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a = waitRecovery(t, s, a.ID)
	b = waitRecovery(t, s, b.ID)
	if a.Status != "done" || b.Status != "done" {
		t.Fatalf("chain failed: %s %s", a.Summary, b.Summary)
	}
	upstream, _ := s.Task(a.TaskID)
	downstream, _ := s.Task(b.TaskID)
	if upstream.Status != "done" || downstream.Status != "done" || len(downstream.DependencyEvidence) != 1 {
		t.Fatal("task completion/witness missing")
	}
	witness := downstream.DependencyEvidence[0]
	if witness.RunID != a.ID || witness.Hash != upstream.Head.Hash || witness.Seq != upstream.Head.Seq || witness.Status != "done" {
		t.Fatal("unbound prerequisite result")
	}
	output, err := s.Artifact(b.ID, "output.txt")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		TaskID       string             `json:"task_id"`
		Dependencies []workpool.Witness `json:"dependencies"`
	}
	if json.Unmarshal(output, &input) != nil || input.TaskID != b.TaskID || !reflect.DeepEqual(input.Dependencies, downstream.DependencyEvidence) {
		t.Fatal("worker did not receive persisted control witnesses")
	}
	var terminal change
	if err = json.Unmarshal(b.Events[len(b.Events)-1].Data, &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Task == nil || terminal.Task.Status != "done" || terminal.TaskEffect != "task_done" {
		t.Fatal("task completion was not atomic with run completion")
	}
	for _, run := range []Run{a, b} {
		for _, event := range run.Events {
			var c change
			_ = json.Unmarshal(event.Data, &c)
			if c.Task != nil && event.Type != "effect" {
				t.Fatal("task lifecycle published as a Decision")
			}
		}
	}
	e.Close()
	live, _ := json.Marshal(s.Tasks())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, _ := json.Marshal(reopened.Tasks())
	if string(live) != string(replayed) {
		t.Fatal("task graph did not replay exactly")
	}
}
func TestTaskRetryKeepsIdentityAndReleasesSuccessDependents(t *testing.T) {
	root := t.TempDir()
	gate := filepath.Join(t.TempDir(), "success")
	app := executionApp(t, fmt.Sprintf(`test -f %q || exit 7; printf result > result.txt`, gate))
	e, s := testEngine(t, root, []Application{app})
	first := launchApp(t, e, root, app)
	first = waitRecovery(t, s, first.ID)
	if first.Status != "failed" {
		t.Fatal("setup did not fail")
	}
	b := testBrief(root)
	b.AppID = app.ID
	b.Scheduling.Deps = []workpool.Dependency{{From: first.TaskID, Guard: "done"}}
	follower, err := e.Launch(b, "")
	if err != nil {
		t.Fatal(err)
	}
	waitTask(t, s, follower.TaskID, "blocked")
	if err = os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	retry, err := e.Retry(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.TaskID != first.TaskID {
		t.Fatal("retry changed task identity")
	}
	retry = waitRecovery(t, s, retry.ID)
	follower = waitRecovery(t, s, follower.ID)
	task, _ := s.Task(first.TaskID)
	if retry.Status != "done" || follower.Status != "done" || len(task.Runs) != 2 || task.CurrentRunID != retry.ID || task.Status != "done" {
		t.Fatal("retry lifecycle/dependencies failed")
	}
	dependent, _ := s.Task(follower.TaskID)
	if dependent.DependencyEvidence[0].RunID != retry.ID {
		t.Fatal("failed attempt satisfied success prerequisite")
	}
	// An old attempt's later audit/release event cannot overwrite current task state.
	if err = s.Resolve(first.ID, "stale-card", "cancel"); err == nil {
		t.Fatal("stale resolution accepted")
	}
	again, _ := s.Task(first.TaskID)
	if !reflect.DeepEqual(task, again) {
		t.Fatal("old attempt overwrote current task")
	}
}
func TestTaskWitnessFrozenAcrossUpstreamRetriesAndPoolReplay(t *testing.T) {
	s := testStore(t)
	if err := s.ConfigurePool(workpool.Pool{ID: "batch", Discipline: "lifo", Concurrency: 1}); err != nil {
		t.Fatal(err)
	}
	a := createRun(t, s)
	if err := s.Complete(a.ID, "Complete", true); err != nil {
		t.Fatal(err)
	}
	b := testBrief(".")
	b.Scheduling = workpool.Request{Pool: "batch", Deps: []workpool.Dependency{{From: a.TaskID, Guard: "done"}}}
	run, err := s.Create(b, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ready, err := s.Schedule(run.ID, nil); err != nil || !ready {
		t.Fatal(err)
	}
	bound, _ := s.Task(run.TaskID)
	if bound.Status != "scheduled" || len(bound.DependencyEvidence) != 1 {
		t.Fatal("missing scheduled task state")
	}
	retry, err := s.Create(a.Brief, a.AppName, a.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Complete(retry.ID, "Complete again", true); err != nil {
		t.Fatal(err)
	}
	frozen, _ := s.Task(run.TaskID)
	if !reflect.DeepEqual(bound.DependencyEvidence, frozen.DependencyEvidence) || frozen.DependencyEvidence[0].RunID != a.ID {
		t.Fatal("prerequisite binding drifted")
	}
	before, _ := json.Marshal(s.Pools())
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, _ := json.Marshal(reopened.Pools())
	if string(before) != string(after) {
		t.Fatal("pool config did not replay")
	}
	if _, err = AuditAutonomyLogs(s.Dir()); err != nil {
		t.Fatalf("pool events broke offline audit: %v", err)
	}
}
func TestTaskAdmissionRejectsUnknownScopeAndUnsupportedDependencies(t *testing.T) {
	s := testStore(t)
	before := s.seq
	for _, request := range []workpool.Request{{Pool: "missing"}, {Deps: []workpool.Dependency{{From: "task_missing"}}}, {Deps: []workpool.Dependency{{From: "missing", Payload: "data"}}}} {
		b := testBrief(".")
		b.Scheduling = request
		if _, err := s.Create(b, "Inventory", "", false); err == nil {
			t.Fatal("invalid task admitted")
		}
		if s.seq != before {
			t.Fatal("rejected task left an event")
		}
	}
	sample, err := s.Create(testBrief("."), "Example", "", true)
	if err != nil {
		t.Fatal(err)
	}
	b := testBrief(".")
	b.Scheduling.Deps = []workpool.Dependency{{From: sample.TaskID, Guard: "done"}}
	if _, err = s.Create(b, "Inventory", "", false); err == nil {
		t.Fatal("sample prerequisite admitted")
	}
	if err = s.ConfigurePool(workpool.Pool{ID: "interactive", Discipline: "priority", Concurrency: 2, Reserved: 2}); err != nil {
		t.Fatal(err)
	}
	if err = s.ConfigurePool(workpool.Pool{ID: "batch", Discipline: "fifo", Concurrency: 1, Reserved: 1}); err == nil {
		t.Fatal("oversubscribed reservations")
	}
}
func TestQueuedTaskDeadlineExpiresWithoutStarting(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf result > result.txt`)
	e, s := testEngine(t, root, []Application{app})
	brief := testBrief(root)
	brief.AppID = app.ID
	deadline := time.Now().Add(60 * time.Millisecond)
	brief.Scheduling.Deadline = &deadline
	r, err := e.Launch(brief, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRecovery(t, s, r.ID)
	task, _ := s.Task(r.TaskID)
	if r.Status != "failed" || task.Status != "failed" || r.Execution != nil || r.Claim != nil || len(r.Segments) != 0 || !strings.Contains(r.Summary, "deadline") {
		t.Fatal("expired queued task started")
	}
}

func TestPoolSelectionRechecksPolicyAndPreservesTurnAcrossReplay(t *testing.T) {
	s := testStore(t)
	create := func(pool string) Run {
		b := testBrief(".")
		b.Scheduling.Pool = pool
		r, err := s.Create(b, "Inventory", "", false)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	batchA, batchB := create("batch"), create("batch")
	interactive := create("interactive")
	ready, err := s.ReadyRuns(nil)
	if err != nil || len(ready) != 3 || ready[0].Run.ID != batchA.ID {
		t.Fatal("initial pool/FIFO order wrong")
	}
	oldPolicy := ready[0].Policy
	if err = s.ConfigurePool(workpool.Pool{ID: "batch", Discipline: "lifo", Concurrency: 2}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ScheduleSelected(batchA.ID, nil, oldPolicy); err != nil || ok {
		t.Fatal("stale pool selection admitted")
	}
	ready, err = s.ReadyRuns(nil)
	if err != nil || ready[0].Run.ID != batchB.ID {
		t.Fatal("updated LIFO policy ignored")
	}
	if ok, err := s.ScheduleSelected(batchB.ID, nil, ready[0].Policy); err != nil || !ok {
		t.Fatal(err)
	}
	admitted, _ := s.Task(batchB.TaskID)
	if admitted.AdmissionPool == nil || admitted.AdmissionPool.Discipline != "lifo" {
		t.Fatal("admission omitted exact policy")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	ready, err = reopened.ReadyRuns([]string{batchB.ID})
	if err != nil || ready[0].Run.ID != interactive.ID {
		t.Fatal("pool rotation reset after replay")
	}
}

func TestTaskDeadlineCapsPersistedExecution(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf result > result.txt`)
	e, s := testEngine(t, root, []Application{app})
	b := testBrief(root)
	b.AppID = app.ID
	deadline := time.Now().UTC().Add(30 * time.Second)
	b.Scheduling.Deadline = &deadline
	r, err := e.Launch(b, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRecovery(t, s, r.ID)
	if r.Status != "done" || r.Execution == nil || !r.Execution.Deadline.Equal(deadline) || !r.Execution.Launch.Posture.Deadline.Equal(deadline) {
		t.Fatal("absolute task deadline did not cap the provider's execution budget")
	}
}

func TestLegacyTaskProjectionDoesNotRewriteHistory(t *testing.T) {
	dir := t.TempDir()
	r := Run{ID: "local_legacy_task", TaskID: "task_legacy", Brief: testBrief("."), Status: "queued", CreatedAt: time.Now().UTC()}
	payload, _ := json.Marshal(change{Run: &r})
	event := Event{Seq: 1, RunID: r.ID, Type: "effect", Kind: "run_created", At: r.CreatedAt, Data: payload}
	event.Hash = eventHash(event)
	line, _ := json.Marshal(event)
	line = append(line, '\n')
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, line, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, err := s.Task(r.TaskID)
	if err != nil || !task.Legacy || task.Status != "queued" || task.Tenant.Org != "local" || len(task.Runs) != 0 {
		t.Fatal("legacy task projection invalid")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(line) {
		t.Fatal("projection rewrote legacy log")
	}
}
