package studio

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mitos/internal/agentcli"
	"mitos/internal/desk"
)

func interactiveFixture(t *testing.T, e *Engine) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "worker", Name: "Worker", Kind: "agent", Executables: []string{bash}, Args: []string{"-c", "echo ready; cat"}, Prompt: "positional", SessionProtocol: "claude"}}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
}
func stopInteractive(t *testing.T, m *desk.Manager, id string) {
	t.Helper()
	if err := m.Stop(id); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		v, _ := m.Session(id)
		if v.Status == "stopped" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("process did not settle")
}
func findWork(t *testing.T, m *desk.Manager, project, id string) desk.WorkAssignment {
	t.Helper()
	v, err := m.Work(project)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range v.Tasks {
		if w.ID == id {
			return w
		}
	}
	t.Fatal("task missing")
	return desk.WorkAssignment{}
}
func TestInteractiveJournalIdentityReplacementAndAcceptance(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	legacy, err := desk.Open(filepath.Join(dir, "desk"), root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := legacy.CreateProject("Project", root)
	if err != nil {
		t.Fatal(err)
	}
	interactiveFixture(t, &Engine{Desk: legacy})
	oldSession, err := legacy.StartAgent(project.ID, agentcli.Request{AgentID: "worker", Prompt: "Legacy work"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	stopInteractive(t, legacy, oldSession.ID)
	legacy.Close()
	backup, err := os.ReadFile(filepath.Join(dir, "desk", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(store, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { engine.Close(); store.Close() }()
	interactiveFixture(t, engine)
	home := store.Home()
	imported, _ := engine.Desk.Work(project.ID)
	if len(imported.Tasks) != 0 || len(imported.Sessions) != 1 || imported.Sessions[0].ID != oldSession.ID || imported.Sessions[0].TaskID != "" {
		t.Fatal("migration invented historical lineage")
	}

	task, err := engine.Desk.CreateWork(project.ID, desk.NewWork{Key: "work", Title: "Audit architecture", Intent: "Produce findings", Criteria: "Findings cite source files", Role: "coordinator"})
	if err != nil {
		t.Fatal(err)
	}
	request := agentcli.Request{Key: "launch", AgentID: "worker", Role: "coordinator", Prompt: task.Intent, ExpectedRevision: task.Revision, Assignment: &agentcli.Assignment{TaskID: task.ID}}
	first, err := engine.Desk.StartAgent(project.ID, request, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	again, err := engine.Desk.StartAgent(project.ID, request, "http://localhost")
	if err != nil || again.ID != first.ID {
		t.Fatal("launch was not idempotent", err)
	}
	changed := request
	changed.Prompt = "different"
	if _, err = engine.Desk.StartAgent(project.ID, changed, "http://localhost"); !errors.Is(err, desk.ErrConflict) {
		t.Fatal("changed request accepted", err)
	}
	if first.TaskID != task.ID || first.ProcessID == "" || first.AttemptID == "" || first.ResponsibilityID == "" {
		t.Fatal("missing binding", first)
	}
	common, err := store.Task(task.ID)
	if err != nil || common.CurrentRunID != first.AttemptID {
		t.Fatal("common task reader", err, common)
	}
	run, err := store.Get(first.AttemptID)
	if err != nil || run.ExecutionKind != "interactive" || run.SessionID != first.ID {
		t.Fatal("attempt mapping", err)
	}
	child, err := engine.Desk.StartAgent(project.ID, agentcli.Request{Key: "child", AgentID: "worker", Prompt: "Check recovery", Assignment: &agentcli.Assignment{RequestedBy: first.ID}}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	childTask := findWork(t, engine.Desk, project.ID, child.TaskID)
	if childTask.ParentID != task.ID || childTask.GoalID != task.GoalID {
		t.Fatal("delegation lost lineage", childTask)
	}
	stopInteractive(t, engine.Desk, child.ID)
	if got := findWork(t, engine.Desk, project.ID, child.TaskID); got.Status == "accepted" {
		t.Fatal("process exit accepted work")
	}
	childTask = findWork(t, engine.Desk, project.ID, child.TaskID)
	childTask, err = engine.Desk.UpdateWork(project.ID, childTask.ID, "report", desk.WorkAction{Revision: childTask.Revision, SessionID: child.ID, Body: "Recovery findings: runner.go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Desk.UpdateWork(project.ID, childTask.ID, "accept", desk.WorkAction{Revision: childTask.Revision, Digest: "wrong", Body: "checked"}); !errors.Is(err, desk.ErrConflict) {
		t.Fatal("wrong result accepted")
	}
	childTask, err = engine.Desk.UpdateWork(project.ID, childTask.ID, "accept", desk.WorkAction{Revision: childTask.Revision, Digest: childTask.Result.Digest, Body: "Inspected cited source and matched the report to criteria"})
	if err != nil || childTask.Status != "accepted" {
		t.Fatal("human acceptance", err)
	}
	if _, err = engine.Desk.MessageResponsibility(project.ID, first.ResponsibilityID, "Review recovery findings"); err != nil {
		t.Fatal(err)
	}
	stopInteractive(t, engine.Desk, first.ID)
	firstTask := findWork(t, engine.Desk, project.ID, task.ID)
	if _, err = engine.Desk.UpdateWork(project.ID, task.ID, "report", desk.WorkAction{Revision: firstTask.Revision, SessionID: first.ID, Body: "Initial findings"}); err != nil {
		t.Fatal(err)
	}
	second, err := engine.Desk.Continue(first.ID, "handover", desk.ContinuationRequest{Key: "replacement", AgentID: "worker", Mode: "context"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if second.TaskID != first.TaskID || second.AttemptID == first.AttemptID || second.ProcessID == first.ProcessID || second.Generation != first.Generation+1 || second.ResponsibilityID != first.ResponsibilityID {
		t.Fatal("replacement changed responsibility", second)
	}
	current := findWork(t, engine.Desk, project.ID, task.ID)
	if _, err = engine.Desk.UpdateWork(project.ID, task.ID, "report", desk.WorkAction{Revision: current.Revision, SessionID: first.ID, Body: "late result"}); !errors.Is(err, desk.ErrConflict) {
		t.Fatal("obsolete report accepted", err)
	}
	if _, err = engine.Desk.StartAgent(project.ID, agentcli.Request{AgentID: "worker", Prompt: "obsolete delegation", Assignment: &agentcli.Assignment{RequestedBy: first.ID}}, "http://localhost"); !errors.Is(err, desk.ErrConflict) {
		t.Fatal("obsolete coordinator delegated", err)
	}
	stopInteractive(t, engine.Desk, second.ID)
	current = findWork(t, engine.Desk, project.ID, task.ID)
	current, err = engine.Desk.UpdateWork(project.ID, task.ID, "report", desk.WorkAction{Revision: current.Revision, SessionID: second.ID, Body: "Replacement findings"})
	if err != nil || len(current.PreviousReports) != 1 || current.PreviousReports[0].Body != "Initial findings" {
		t.Fatal("replacement lost earlier evidence", err)
	}
	oldAttempt, _ := store.Get(first.AttemptID)
	if oldAttempt.Summary != "Initial findings" {
		t.Fatal("earlier attempt report changed")
	}
	before, _ := engine.Desk.Work(project.ID)
	engine.Close()
	store.Close()
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine, err = NewEngine(store, nil, root)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := engine.Desk.Work(project.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("journal replay changed work")
	}
	if store.Home().ID != home.ID {
		t.Fatal("server identity changed")
	}
	restored, _ := store.Task(task.ID)
	if len(restored.Runs) != 2 {
		t.Fatal("attempt history lost", restored.Runs)
	}
	preserved, _ := os.ReadFile(filepath.Join(dir, "desk", "state.json"))
	if !reflect.DeepEqual(backup, preserved) {
		t.Fatal("legacy backup overwritten")
	}
	if len(after.Responsibilities[0].Messages) != 1 {
		t.Fatal("supervisor inbox lost")
	}
	if _, err := ReportAutonomy(store.List()); err != nil {
		t.Fatal("interactive history broke batch metrics", err)
	}
	if _, err := AuditAutonomyLogs(filepath.Join(dir, "events.jsonl")); err != nil {
		t.Fatal("interactive history broke offline audit", err)
	}
}
func TestInteractiveAdmissionFailurePublishesNoTaskOrProcess(t *testing.T) {
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	interactiveFixture(t, e)
	project, err := e.Desk.CreateProject("Test", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.Desk.Work(project.ID)
	s.mu.Lock()
	s.file = &controlledLog{eventLog: s.file, write: true, failure: errors.New("injected journal failure")}
	s.mu.Unlock()
	if _, err = e.Desk.StartAgent(project.ID, agentcli.Request{Key: "failed", AgentID: "worker", Prompt: "Must not launch"}, "http://localhost"); err == nil {
		t.Fatal("failed admission succeeded")
	}
	after, _ := e.Desk.Work(project.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("uncommitted work became visible")
	}
	if len(s.Tasks()) != 0 || len(s.List()) != 0 {
		t.Fatal("uncommitted attempt visible")
	}
}
