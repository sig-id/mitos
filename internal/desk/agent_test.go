package desk

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/agentcli"
)

func TestAgentLaunchAndStablePromptName(t *testing.T) {
	m, p := testManager(t)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	m.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "reader", Name: "Reader", Kind: "application", Executables: []string{shell}, Args: []string{"--noprofile", "--norc", "-c", "read -r first; read -r second; printf '%s|%s|%s' \"$first\" \"$second\" \"$CUSTOM_KEY\""}, Prompt: "none"}}, Profiles: []agentcli.Profile{{ID: "test", Name: "Test", AgentID: "reader", Env: map[string]string{"CUSTOM_KEY": "SOURCE_KEY"}}}}, []string{"SOURCE_KEY=hidden-value"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "reader", ProfileID: "test"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if !s.NamePending || s.AgentID != "reader" || s.ProfileID != "test" {
		t.Fatal(s)
	}
	if err := m.Prompt(s.ID, "first", "Fix the tab freeze", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(s.ID, "first", "Fix the tab freeze", false); err != nil {
		t.Fatal(err)
	}
	if err := m.Prompt(s.ID, "second", "Now add discovery", false); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { v, _ := m.Session(s.ID); return v.Status == "exited" })
	named, _ := m.Session(s.ID)
	if named.Name != "Fix the tab freeze" || named.NamePending {
		t.Fatal("name changed with follow-up prompt", named)
	}
	output, err := m.ReadOutput(s.ID, 0)
	if err != nil || !strings.Contains(string(output.Data), "Fix the tab freeze|Now add discovery|hidden-value") {
		t.Fatal(string(output.Data), err)
	}
	state, err := os.ReadFile(filepath.Join(m.dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), "hidden-value") {
		t.Fatal("session metadata persisted environment secret")
	}
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, _ := reopened.Session(s.ID)
	if saved.Name != named.Name || saved.AgentID != "reader" {
		t.Fatal("identity lost on restart", saved)
	}
}

func TestPromptTitleUnicode(t *testing.T) {
	name := promptTitle(strings.Repeat("界", 200))
	if len(name) > 100 || name == "" {
		t.Fatalf("invalid bounded title: %q", name)
	}
}

func TestAgentAssignmentLineage(t *testing.T) {
	m, p := testManager(t)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	m.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "lineage", Name: "Lineage", Kind: "application", Executables: []string{shell}, Args: []string{"-c", `printf '%s|%s|%s|%s' "$MITOS_GOAL_ID" "$MITOS_TASK_ID" "$MITOS_REQUESTED_BY" "$MITOS_HANDOFF_TOPIC_ID"`}, Prompt: "none"}}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	topic, err := m.CreateTopic(p.ID, "operator", "Handoff", "Continue here", nil)
	if err != nil {
		t.Fatal(err)
	}
	assignment := &agentcli.Assignment{GoalID: "goal-1", TaskID: "task-1", RequestedBy: "operator", HandoffTopicID: topic.ID}
	s, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "lineage", Assignment: assignment}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	assignment.GoalID = "mutated"
	await(t, func() bool { v, _ := m.Session(s.ID); return v.Status == "exited" })
	output, err := m.ReadOutput(s.ID, 0)
	if err != nil || !strings.Contains(string(output.Data), "goal-1|task-1|operator|"+topic.ID) {
		t.Fatal(string(output.Data), err)
	}
	stored, _ := m.Session(s.ID)
	if stored.Assignment.GoalID != "goal-1" {
		t.Fatal("assignment retained caller-owned pointer")
	}
	child, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "lineage", Assignment: &agentcli.Assignment{RequestedBy: s.ID, HandoffTopicID: topic.ID}}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { v, _ := m.Session(child.ID); return v.Status == "exited" })
	otherDir := filepath.Join(m.root, "other")
	if err := os.Mkdir(otherDir, 0700); err != nil {
		t.Fatal(err)
	}
	other, err := m.CreateProject("Other", otherDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []*agentcli.Assignment{{RequestedBy: s.ID}, {HandoffTopicID: topic.ID}, {GoalID: "bad\nreference"}} {
		if _, err := m.StartAgent(other.ID, agentcli.Request{AgentID: "lineage", Assignment: a}, "http://localhost"); err == nil {
			t.Fatalf("invalid assignment admitted: %+v", a)
		}
	}
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, _ := reopened.Session(child.ID)
	if saved.Assignment.RequestedBy != s.ID || saved.Assignment.HandoffTopicID != topic.ID {
		t.Fatal("lineage lost on restart", saved)
	}
}
