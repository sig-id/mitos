package desk

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"mitos/internal/agentcli"
)

func continuationManager(t *testing.T) (*Manager, Project) {
	t.Helper()
	m, p := testManager(t)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(p.Directory, "fixture.sh")
	if err := os.WriteFile(fixture, []byte("#!/bin/bash\nprintf 'ARGV:%s\\n' \"$@\"\nprintf 'PROFILE:%s\\n' \"$AGENT_TEST_PROFILE\"\nif test -n \"$MITOS_HANDOVER\"; then cat \"$MITOS_HANDOVER\"; fi\nprintf '\\nREADY\\n'\nwhile read -r line; do printf 'INPUT:%s\\n' \"$line\"; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	defs := []agentcli.Definition{}
	for _, id := range []string{"source", "target"} {
		defs = append(defs, agentcli.Definition{ID: id, Name: id, Kind: "agent", Executables: []string{shell}, Args: []string{fixture}, Prompt: "positional", SessionProtocol: "claude"})
	}
	m.Agents, err = agentcli.New(nil, agentcli.Config{Applications: defs, Profiles: []agentcli.Profile{{ID: "target-env", Name: "Target environment", AgentID: "target", Env: map[string]string{"AGENT_TEST_PROFILE": "TARGET_PROFILE"}}}}, append(os.Environ(), "TARGET_PROFILE=target-value"))
	if err != nil {
		t.Fatal(err)
	}
	return m, p
}

func startContinuationSource(t *testing.T, m *Manager, p Project) Session {
	t.Helper()
	s, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "source", Prompt: "Implement checkout", Assignment: &agentcli.Assignment{GoalID: "goal-checkout", TaskID: "task-test"}}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { out, _ := m.ReadOutput(s.ID, 0); return strings.Contains(string(out.Data), "READY") })
	return s
}

func stopContinuation(t *testing.T, m *Manager, s Session) {
	t.Helper()
	if err := m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { s, _ := m.Session(s.ID); return s.Status == "stopped" })
}

func TestNativeResumePersistenceAndCompetingRequests(t *testing.T) {
	m, p := continuationManager(t)
	source := startContinuationSource(t, m, p)
	request := ContinuationRequest{Key: "resume", Mode: "native", Prompt: "Continue checkout"}
	if _, err := m.Continue(source.ID, "resume", request, "http://localhost"); !errors.Is(err, ErrConflict) {
		t.Fatal("resumed a live source", err)
	}
	stopContinuation(t, m, source)
	registry := m.Agents
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Agents = registry
	m = reopened
	var wg sync.WaitGroup
	results := make(chan Session, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := m.Continue(source.ID, "resume", request, "http://localhost")
			results <- s
			failures <- err
		}()
	}
	wg.Wait()
	a, b := <-results, <-results
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.ID == source.ID || a.NativeSessionID != source.NativeSessionID || a.Continuation.SourceID != source.ID {
		t.Fatal(a, b)
	}
	request.Key = "other"
	if _, err := m.Continue(source.ID, "resume", request, "http://localhost"); !errors.Is(err, ErrConflict) {
		t.Fatal("concurrent native writer", err)
	}
	request.Key = "resume"
	request.Prompt = "different"
	if _, err := m.Continue(source.ID, "resume", request, "http://localhost"); !errors.Is(err, ErrConflict) {
		t.Fatal("key collision accepted", err)
	}
	await(t, func() bool {
		out, _ := m.ReadOutput(a.ID, 0)
		return strings.Contains(string(out.Data), "ARGV:--resume") && strings.Contains(string(out.Data), source.NativeSessionID)
	})
	if a.Assignment.GoalID != "goal-checkout" || a.Assignment.RequestedBy != source.ID {
		t.Fatal("lineage lost", a)
	}
	stopContinuation(t, m, a)
	m.Close()
	saved, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	saved.Agents = registry
	replay, err := saved.Continue(source.ID, "resume", ContinuationRequest{Key: "resume", Mode: "native", Prompt: "Continue checkout"}, "http://localhost")
	if err != nil || replay.ID != a.ID || replay.Status != "stopped" {
		t.Fatal("durable idempotency lost", replay, err)
	}
}

func TestForkAndContextHandover(t *testing.T) {
	m, p := continuationManager(t)
	source := startContinuationSource(t, m, p)
	fork, err := m.Continue(source.ID, "fork", ContinuationRequest{Key: "fork", Mode: "native"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if fork.NativeSessionID != "" || fork.Continuation.NativeSourceID != source.NativeSessionID {
		t.Fatal("fork reused source ID", fork)
	}
	current, _ := m.Session(source.ID)
	if current.Status != "running" {
		t.Fatal("fork stopped source")
	}
	stopContinuation(t, m, fork)
	topic, err := m.CreateTopic(p.ID, source.ID, "Handoff", "Checkout handler implemented; tests remain", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Post(topic.ID, source.ID, "Run checkout tests and inspect cart.go", topic.Revision)
	if err != nil {
		t.Fatal(err)
	}
	target, err := m.Continue(source.ID, "handover", ContinuationRequest{Key: "handover", AgentID: "target", ProfileID: "target-env", HandoffTopicID: topic.ID, Notes: "Preserve API behavior", Prompt: "Finish the tests"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { s, _ := m.Session(source.ID); return s.Status == "stopped" })
	await(t, func() bool { out, _ := m.ReadOutput(target.ID, 0); return strings.Contains(string(out.Data), "READY") })
	out, _ := m.ReadOutput(target.ID, 0)
	for _, text := range []string{"PROFILE:target-value", "Implement checkout", "Finish the tests", "Preserve API behavior", "Run checkout tests and inspect cart.go"} {
		if !strings.Contains(string(out.Data), text) {
			t.Fatal("target did not receive context", text, string(out.Data))
		}
	}
	if target.AgentID != "target" || target.Continuation.Mode != "context" || target.Assignment.HandoffTopicID != topic.ID {
		t.Fatal(target)
	}
	context, err := os.ReadFile(target.Continuation.ContextPath)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(target.Continuation.ContextPath); info.Mode().Perm() != 0600 {
		t.Fatal("handover not private")
	}
	_, err = m.Post(topic.ID, "operator", "Later change", topic.Revision+1)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(target.Continuation.ContextPath)
	if string(again) != string(context) {
		t.Fatal("handover changed after capture")
	}
	if target.Continuation.OutputBytes == 0 {
		t.Fatal("source evidence missing")
	}
	stopContinuation(t, m, target)
	portable, err := m.Continue(target.ID, "resume", ContinuationRequest{Key: "portable", Mode: "context"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if portable.NativeSessionID == target.NativeSessionID {
		t.Fatal("portable resume reused native conversation")
	}
}

func TestContinuationRejectionsKeepSourceRunning(t *testing.T) {
	m, p := continuationManager(t)
	source := startContinuationSource(t, m, p)
	for _, r := range []ContinuationRequest{{Key: "bad", AgentID: "missing"}, {Key: "profile", AgentID: "target", ProfileID: "missing"}, {Key: "topic", AgentID: "target", HandoffTopicID: "missing"}, {Key: "mode", Mode: "native", AgentID: "target"}} {
		if _, err := m.Continue(source.ID, "handover", r, "http://localhost"); err == nil {
			t.Fatal("invalid handover accepted", r)
		}
		current, _ := m.Session(source.ID)
		if current.Status != "running" {
			t.Fatal("failed handover stopped source")
		}
	}
	if _, err := m.Continue(source.ID, "fork", ContinuationRequest{Key: "different-id", Mode: "native", NativeSessionID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}, "http://localhost"); err == nil {
		t.Fatal("substituted native identity")
	}
	if _, err := m.LinkNativeSession(source.ID, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"); !errors.Is(err, ErrConflict) {
		t.Fatal("rebound native identity", err)
	}
	if _, err := m.Continue(source.ID, "fork", ContinuationRequest{Key: "change-agent", AgentID: "target"}, "http://localhost"); err == nil {
		t.Fatal("fork changed agent")
	}
	// A valid executable with an invalid interpreter fails at process launch.
	broken := filepath.Join(p.Directory, "broken")
	os.WriteFile(broken, []byte("#!/missing/interpreter\n"), 0700)
	m.Agents, _ = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "broken", Name: "Broken", Kind: "agent", Executables: []string{broken}, Prompt: "positional"}}}, os.Environ())
	if _, err := m.Continue(source.ID, "handover", ContinuationRequest{Key: "broken", AgentID: "broken"}, "http://localhost"); err == nil {
		t.Fatal("broken executable started")
	}
	current, _ := m.Session(source.ID)
	if current.Status != "running" {
		t.Fatal("failed process launch stopped source")
	}
	b, _ := json.Marshal(current)
	if strings.Contains(string(b), "target-value") {
		t.Fatal("environment leaked to session metadata")
	}
}

func TestCoordinatorContinuationAndHandoffBoundaries(t *testing.T) {
	m, p := continuationManager(t)
	source, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "source", Role: "coordinator", Prompt: "Coordinate checkout"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	fork, err := m.Continue(source.ID, "fork", ContinuationRequest{Key: "coordinator-fork", Mode: "context"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if fork.Role != "agent" {
		t.Fatal("fork created another coordinator")
	}
	otherDir := filepath.Join(p.Directory, "other")
	if err := os.Mkdir(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := m.CreateProject("Other", otherDir)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := m.CreateTopic(other.ID, "operator", "Private project", "Other project context", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Continue(source.ID, "handover", ContinuationRequest{Key: "cross-project", AgentID: "target", HandoffTopicID: topic.ID}, "http://localhost"); err == nil {
		t.Fatal("cross-project handoff admitted")
	}
	target, err := m.Continue(source.ID, "handover", ContinuationRequest{Key: "coordinator-handover", AgentID: "target"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if target.Role != "coordinator" {
		t.Fatal("handover lost coordinator role")
	}
	await(t, func() bool { s, _ := m.Session(source.ID); return s.Status == "stopped" })
	if _, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "source", Role: "coordinator"}, "http://localhost"); err == nil {
		t.Fatal("second coordinator admitted")
	}
	m.mu.Lock()
	saved, _ := m.sessionLocked(fork.ID)
	saved.Status = "interrupted"
	m.mu.Unlock()
	if _, err := m.Continue(fork.ID, "fork", ContinuationRequest{Key: "interrupted", Mode: "context"}, "http://localhost"); !errors.Is(err, ErrConflict) {
		t.Fatal("interrupted fork admitted", err)
	}
}
