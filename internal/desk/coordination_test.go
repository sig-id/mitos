package desk

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/agentcli"
)

func TestProjectProfileRevisionAndDurability(t *testing.T) {
	m, p := testManager(t)
	profile := DefaultProjectProfile()
	profile.Goal = "Ship a reviewed parser"
	profile.TempPaths = []string{filepath.Join(p.Directory, "scratch")}
	saved, err := m.SaveProfile(p.ID, profile, 0)
	if err != nil || saved.Revision != 1 {
		t.Fatal(saved, err)
	}
	if _, err = m.SaveProfile(p.ID, profile, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted", err)
	}
	invalid := saved
	invalid.TempPaths = []string{"../outside"}
	if _, err = m.SaveProfile(p.ID, invalid, 1); err == nil {
		t.Fatal("relative temp path accepted")
	}
	profile.TempPaths[0] = "/mutated"
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Project(p.ID)
	if err != nil || got.Profile.Goal != saved.Goal || got.Profile.TempPaths[0] == "/mutated" {
		t.Fatal(got, err)
	}
}

func TestCoordinatorDelegationContextAndCompletion(t *testing.T) {
	m, p := continuationManager(t)
	coordinator, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "source", Role: "coordinator", Prompt: "Deliver a parser"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "target", Prompt: "Write parser tests", Assignment: &agentcli.Assignment{RequestedBy: coordinator.ID, TaskID: "parser-tests"}}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if worker.CoordinatorID != coordinator.ID || worker.Workspace != p.Directory {
		t.Fatal(worker)
	}
	await(t, func() bool {
		out, _ := m.ReadOutput(coordinator.ID, 0)
		return strings.Contains(string(out.Data), "You are the Mitos coordinator") && strings.Contains(string(out.Data), "desk start-session") && strings.Contains(string(out.Data), "Before implementation") && strings.Contains(string(out.Data), "do not make implementation edits yourself")
	})
	profile := DefaultProjectProfile()
	profile.MaxAgents = 1
	if _, err = m.SaveProfile(p.ID, profile, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = m.StartAgent(p.ID, agentcli.Request{AgentID: "target", Prompt: "Too many"}, "http://localhost"); err == nil {
		t.Fatal("concurrency limit ignored")
	}
	replacement, err := m.Continue(worker.ID, "handover", ContinuationRequest{Key: "replace", Mode: "context", AgentID: "source"}, "http://localhost")
	if err != nil {
		t.Fatal("replacement counts twice against agent limit", err)
	}
	if replacement.CoordinatorID != coordinator.ID {
		t.Fatal("handover lost parent", replacement)
	}
	stopContinuation(t, m, replacement)
	inbox, err := m.Inbox(coordinator.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range inbox {
		if strings.Contains(n.Body, replacement.ID) && strings.Contains(n.Body, "not task verification") {
			found = true
		}
	}
	if !found {
		t.Fatal("coordinator not notified of exit", inbox)
	}
}

func TestAttentionAcknowledgmentReleaseAndRestart(t *testing.T) {
	m, p := testManager(t)
	coordinator, err := startTestCommand(m, p.ID, "Coordinator", "coordinator", "sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := startTestCommand(m, p.ID, "Worker", "agent", "sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	n, err := m.Broadcast(p.ID, coordinator.ID, "Hold the shared index", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Targets) != 1 || n.Targets[0].SessionID != worker.ID {
		t.Fatal(n)
	}
	if err = m.Acknowledge(n.ID, coordinator.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("non-target acknowledged", err)
	}
	if err = m.NoticeDelivery(n.ID, worker.ID, "context_queued", "Hook delivered"); err != nil {
		t.Fatal(err)
	}
	view, _ := m.Coordination(p.ID)
	if view.Notices[0].Targets[0].AcknowledgedAt != nil {
		t.Fatal("delivery falsely acknowledged")
	}
	if err = m.Acknowledge(n.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	inbox, _ := m.Inbox(worker.ID)
	if len(inbox) != 1 {
		t.Fatal("acknowledgment removed active pause", inbox)
	}
	late, err := startTestCommand(m, p.ID, "Late worker", "agent", "sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ = m.Inbox(late.ID)
	if len(inbox) != 1 {
		t.Fatal("new worker missed breakpoint")
	}
	if err = m.ReleaseNotice(n.ID, worker.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("worker released coordinator breakpoint", err)
	}
	if err = m.ReleaseNotice(n.ID, coordinator.ID); err != nil {
		t.Fatal(err)
	}
	inbox, _ = m.Inbox(worker.ID)
	if len(inbox) != 1 || inbox[0].ReleasedAt == nil {
		t.Fatal("release not available to native adapter", inbox)
	}
	// A late delivery receipt for the original pause must not consume the release.
	if err = m.NoticeDelivery(n.ID, worker.ID, "context_queued", "Late receipt"); err != nil {
		t.Fatal(err)
	}
	inbox, _ = m.Inbox(worker.ID)
	if len(inbox) != 1 {
		t.Fatal("release lost to stale receipt")
	}
	if err = m.ReleaseDelivery(n.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	inbox, _ = m.Inbox(worker.ID)
	if len(inbox) != 0 {
		t.Fatal("release still pending", inbox)
	}
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	view, err = reopened.Coordination(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got Notice
	for _, v := range view.Notices {
		if v.ID == n.ID {
			got = v
		}
	}
	if got.ReleasedAt == nil || !got.Targets[0].ReleaseQueued || got.Targets[0].AcknowledgedAt == nil {
		t.Fatal("receipts not durable", got)
	}
}

func TestAgentWorktreeAndContinuationPreserveFiles(t *testing.T) {
	m, p := continuationManager(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", p.Directory}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	git("init")
	git("add", "fixture.sh")
	git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "Initial")
	profile := DefaultProjectProfile()
	profile.GitMode = "worktree"
	if _, err := m.SaveProfile(p.ID, profile, 0); err != nil {
		t.Fatal(err)
	}
	worker := startContinuationSource(t, m, p)
	if worker.Workspace == p.Directory || worker.WorktreeBranch == "" {
		t.Fatal("worktree not allocated", worker)
	}
	if err := m.Input(worker.ID, "cwd", "\x04"); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { s, _ := m.Session(worker.ID); return !activeSession(s) })
	path := filepath.Join(worker.Workspace, "uncommitted.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	resumed, err := m.Continue(worker.ID, "resume", ContinuationRequest{Key: "resume", Mode: "context"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Workspace != worker.Workspace {
		t.Fatal("resume lost working files", resumed)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep me" {
		t.Fatal("uncommitted files lost", err)
	}
	fork, err := m.Continue(resumed.ID, "fork", ContinuationRequest{Key: "fork", Mode: "context"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if fork.Workspace != worker.Workspace {
		t.Fatal("fork silently changed checkout", fork)
	}
	if _, err := os.Stat(filepath.Join(p.Directory, "uncommitted.txt")); !os.IsNotExist(err) {
		t.Fatal("worktree leaked into project checkout", err)
	}
}

func TestPiAttentionNativeAdapter(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required for the Pi extension test")
	}
	if out, err := exec.Command(node, "testdata/pi_attention_test.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestCodexAttentionQueueWaitsForExactLink(t *testing.T) {
	m, p := testManager(t)
	fixture := filepath.Join(p.Directory, "codex.sh")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\nif [ \"$1\" = queue ]; then printf '%s\\n' \"$@\" >> queued.txt; exit; fi\nwhile read line; do :; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var err error
	m.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "codex", Name: "Codex fixture", Kind: "agent", Executables: []string{fixture}, Prompt: "positional", SessionProtocol: "codex"}}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "codex", Prompt: "Test attention"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	n, err := m.Broadcast(p.ID, "operator", "Hold Git", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(p.Directory, "queued.txt")); !os.IsNotExist(err) {
		t.Fatal("queued without exact thread link", err)
	}
	reference := "23456789-1234-1234-1234-123456789abc"
	if _, err = m.LinkNativeSession(s.ID, reference); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { v, _ := m.Coordination(p.ID); return v.Notices[0].Targets[0].State == "context_queued" })
	data, err := os.ReadFile(filepath.Join(p.Directory, "queued.txt"))
	if err != nil || !strings.Contains(string(data), reference) || !strings.Contains(string(data), n.ID) {
		t.Fatal(string(data), err)
	}
	if err = m.Acknowledge(n.ID, s.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.ReleaseNotice(n.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { v, _ := m.Coordination(p.ID); return v.Notices[0].Targets[0].ReleaseQueued })
	data, err = os.ReadFile(filepath.Join(p.Directory, "queued.txt"))
	if err != nil || !strings.Contains(string(data), "RELEASED") {
		t.Fatal(string(data), err)
	}
}
