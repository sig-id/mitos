package desk

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"

	"mitos/internal/agentcli"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) (*Manager, Project) {
	t.Helper()
	root := t.TempDir()
	m, err := Open(t.TempDir(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	p, err := m.CreateProject("Test project", root)
	if err != nil {
		t.Fatal(err)
	}
	return m, p
}
func await(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("condition did not become true")
}
func TestTerminalInputResizeReplayAndSharedDirectory(t *testing.T) {
	m, p := testManager(t)
	s, err := startTestCommand(m, p.ID, "Worker", "agent", "read -r line; printf '%s' \"$line\" > result.txt; printf '\\nDONE:%s\\n' \"$line\"; stty size", "http://localhost:4646")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Resize(s.ID, 31, 99); err != nil {
		t.Fatal(err)
	}
	if err = m.Input(s.ID, "input-one", "hello\r"); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { s, _ := m.Session(s.ID); return s.Status == "exited" })
	out, err := m.ReadOutput(s.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out.Data), "DONE:hello") || !strings.Contains(string(out.Data), "31 99") {
		t.Fatalf("output %q", out.Data)
	}
	b, err := os.ReadFile(filepath.Join(p.Directory, "result.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("shared directory not used: %q %v", b, err)
	}
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.ReadOutput(s.ID, 0)
	if err != nil || string(again.Data) != string(out.Data) {
		t.Fatalf("output not durable: %v", err)
	}
}
func TestCoordinatorUniquenessAndStop(t *testing.T) {
	m, p := testManager(t)
	s, err := startTestCommand(m, p.ID, "Coordinator", "coordinator", "sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = startTestCommand(m, p.ID, "Duplicate", "coordinator", "sleep 60", "http://localhost"); err == nil {
		t.Fatal("second coordinator admitted")
	}
	if err = m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { s, _ := m.Session(s.ID); return s.Status == "stopped" })
	if err = m.Input(s.ID, "late", "anything"); err == nil {
		t.Fatal("input accepted after stop")
	}
}
func TestInputRequestNotAppliedTwice(t *testing.T) {
	m, p := testManager(t)
	s, err := startTestCommand(m, p.ID, "Reader", "shell", "read -r a; read -r b; printf '\\nRESULT:%s:%s\\n' \"$a\" \"$b\"", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ key, value string }{{"one", "first\r"}, {"one", "duplicate\r"}, {"two", "second\r"}} {
		if err = m.Input(s.ID, input.key, input.value); err != nil {
			t.Fatal(err)
		}
	}
	await(t, func() bool { s, _ := m.Session(s.ID); return s.Status == "exited" })
	out, _ := m.ReadOutput(s.ID, 0)
	if !strings.Contains(string(out.Data), "RESULT:first:second") {
		t.Fatalf("duplicate input: %q", out.Data)
	}
}
func TestTopicsProgressiveDiscoveryAndRestart(t *testing.T) {
	m, p := testManager(t)
	topic, err := m.CreateTopic(p.ID, "operator", "API contract", "Small summary", []string{"src/api"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Post(topic.ID, "operator", "Full details", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Post(topic.ID, "operator", "stale", 1); err != ErrConflict {
		t.Fatalf("stale write: %v", err)
	}
	list := m.Topics(p.ID, "src/api", "")
	if len(list) != 1 || list[0].Posts != nil {
		t.Fatal("index leaks full thread")
	}
	if err = m.Follow(topic.ID, "operator", 1); err != nil {
		t.Fatal(err)
	}
	delta, err := m.Topic(topic.ID, 1)
	if err != nil || len(delta.Posts) != 1 || delta.Posts[0].Body != "Full details" {
		t.Fatalf("delta: %+v %v", delta, err)
	}
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	followed := reopened.Topics(p.ID, "", "operator")
	if len(followed) != 1 || followed[0].Followers["operator"] != 1 {
		t.Fatal("subscription cursor lost")
	}
	if err = reopened.Follow(topic.ID, "operator", 0); err != ErrConflict {
		t.Fatal("cursor regressed")
	}
}
func TestProjectRootAndCrossProjectAuthor(t *testing.T) {
	m, p := testManager(t)
	if _, err := m.CreateProject("Escape", t.TempDir()); err == nil {
		t.Fatal("outside root accepted")
	}
	dir := filepath.Join(p.Directory, "another")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := m.CreateProject("Other", dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := startTestCommand(m, other.ID, "Worker", "agent", "sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.CreateTopic(p.ID, s.ID, "Wrong project", "Body", nil); err == nil {
		t.Fatal("cross-project author accepted")
	}
}
func TestRestartDoesNotReplayActiveSessionOrBuild(t *testing.T) {
	dir, root := t.TempDir(), t.TempDir()
	saved := state{Version: 1, Projects: []Project{{ID: "p", Directory: root}}, Sessions: []Session{{ID: "s", ProjectID: "p", Status: "running", PID: 123}}, Jobs: []Job{{ID: "j", ProjectID: "p", Status: "running"}}}
	b, _ := json.Marshal(saved)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := Open(dir, root)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s, _ := m.Session("s")
	if s.Status != "interrupted" || s.PID != 0 {
		t.Fatal("stale process retained")
	}
	if jobs := m.Jobs("p"); len(jobs) != 1 || jobs[0].Status != "interrupted" {
		t.Fatal("build automatically replayed")
	}
	if err = m.Release("j", "operator", false); err == nil {
		t.Fatal("orphan hold released without inspection")
	}
	if err = m.Release("j", "operator", true); err != nil {
		t.Fatal(err)
	}
}
func TestManagedBuildSerializesAndConsumerRelease(t *testing.T) {
	m, p := testManager(t)
	bin := t.TempDir()
	cargo := filepath.Join(bin, "cargo")
	// A deterministic adapter fixture exercises actual process lifecycle, not Rust.
	if err := os.WriteFile(cargo, []byte("#!/bin/sh\necho started\nwhile [ ! -f allow-exit ]; do sleep 0.02; done\necho complete\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	s, err := startTestCommand(m, p.ID, "Consumer", "agent", "sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Build(p.ID, "operator", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Build(p.ID, "operator", []string{"--release"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "queued" {
		t.Fatal("overlapping build admitted")
	}
	if _, err = m.Join(first.ID, s.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.Release(first.ID, "operator", false); err != nil {
		t.Fatal(err)
	}
	jobs := m.Jobs(p.ID)
	if jobs[0].Status != "running" || len(jobs[0].Consumers) != 1 {
		t.Fatal("one consumer cancelled another's job")
	}
	if err = os.WriteFile(filepath.Join(p.Directory, "allow-exit"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { j := m.Jobs(p.ID); return j[0].Status == "exited" && j[1].Status == "exited" })
	if _, err = m.Build(p.ID, "operator", []string{"--config", "danger"}); err == nil {
		t.Fatal("untracked resource override admitted")
	}
	resources, err := m.Resources(context.Background(), p.ID, false)
	if err != nil || len(resources.Processes) == 0 {
		t.Fatalf("missing live session resources: %v", err)
	}
}

func TestScratchAndGitAttribution(t *testing.T) {
	m, p := testManager(t)
	s, err := startTestCommand(m, p.ID, "Writer", "shell", "printf scratch > \"$TMPDIR/test.txt\"; git init --quiet; sleep 60", "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { _, err := os.Stat(filepath.Join(s.Scratch, "test.txt")); return err == nil })
	if err = m.CleanScratch(s.ID); err == nil {
		t.Fatal("active scratch removed")
	}
	await(t, func() bool {
		_, git := m.artifactViews(p.ID)
		for _, g := range git {
			if g.Command == "init" && g.SessionID == s.ID {
				return true
			}
		}
		return false
	})
	if err = m.Stop(s.ID); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { v, _ := m.Session(s.ID); return v.Status == "stopped" })
	if err = m.CleanScratch(s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.Scratch); !os.IsNotExist(err) {
		t.Fatal("scratch remains")
	}
	if _, err = os.Stat(filepath.Join(p.Directory, ".git")); err != nil {
		t.Fatal("project files were touched by scratch cleanup")
	}
}

func TestManagerExcludesSecondWriter(t *testing.T) {
	m, _ := testManager(t)
	if other, err := Open(m.dir, m.root); err == nil {
		other.Close()
		t.Fatal("second writer admitted")
	}
}

// Explicit process fixtures exercise the PTY lifecycle without a real model CLI.
func startTestCommand(m *Manager, project, name, role, command, serverURL string) (Session, error) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		return Session{}, err
	}
	return m.start(project, name, role, command, serverURL, agentcli.Launch{Executable: shell, Args: []string{"--noprofile", "--norc", "-c", command}, Env: os.Environ()}, false, nil)
}
