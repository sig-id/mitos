package desk

import (
	"errors"
	"os"
	"strings"
	"testing"

	"mitos/internal/agentcli"
)

func launchDefaultsManager(t *testing.T) (*Manager, Project) {
	t.Helper()
	m, p := continuationManager(t)
	definitions := []agentcli.Definition{}
	for _, installation := range m.Agents.List(p.Directory) {
		definitions = append(definitions, installation.Definition)
	}
	var err error
	m.Agents, err = agentcli.New(nil, agentcli.Config{Applications: definitions, Profiles: []agentcli.Profile{
		{ID: "first", Name: "First account", AgentID: "source", Env: map[string]string{"AGENT_TEST_PROFILE": "FIRST_ACCOUNT"}},
		{ID: "second", Name: "Second account", AgentID: "source", Env: map[string]string{"AGENT_TEST_PROFILE": "SECOND_ACCOUNT"}},
	}}, append(os.Environ(), "AGENT_TEST_PROFILE=native", "FIRST_ACCOUNT=first", "SECOND_ACCOUNT=second"))
	if err != nil {
		t.Fatal(err)
	}
	return m, p
}

func TestContinuationPreservesProfileAfterDefaultsChange(t *testing.T) {
	for _, profile := range []string{"", "first"} {
		for _, kind := range []string{"resume", "fork"} {
			for _, mode := range []string{"native", "context"} {
				t.Run(profile+"/"+kind+"/"+mode, func(t *testing.T) {
					m, p := launchDefaultsManager(t)
					source, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "source", ProfileID: profile, Prompt: "Start work"}, "http://localhost")
					if err != nil {
						t.Fatal(err)
					}
					if kind == "resume" {
						stopContinuation(t, m, source)
					}
					m.LaunchDefaults = func(string, string) agentcli.LaunchDefaults {
						return agentcli.LaunchDefaults{ProfileID: "second", Args: []string{"configured-argument"}}
					}
					next, err := m.Continue(source.ID, kind, ContinuationRequest{Key: "continue", Mode: mode}, "http://localhost")
					if err != nil {
						t.Fatal(err)
					}
					if next.ProfileID != profile {
						t.Fatalf("continuation switched profile from %q to %q", profile, next.ProfileID)
					}
					await(t, func() bool { out, _ := m.ReadOutput(next.ID, 0); return strings.Contains(string(out.Data), "READY") })
					out, _ := m.ReadOutput(next.ID, 0)
					account := profile
					if account == "" {
						account = "native"
					}
					for _, expected := range []string{"PROFILE:" + account, "ARGV:configured-argument"} {
						if !strings.Contains(string(out.Data), expected) {
							t.Fatalf("missing %q in output %q", expected, out.Data)
						}
					}
				})
			}
		}
	}
}

type launchReceiptStore struct{ data []byte }

func (s *launchReceiptStore) LoadDesk() ([]byte, error) { return append([]byte{}, s.data...), nil }
func (s *launchReceiptStore) SaveDesk(data []byte) error {
	s.data = append([]byte{}, data...)
	return nil
}

func TestLaunchRetryUsesOriginalRequestAfterDefaultsChange(t *testing.T) {
	m, p := launchDefaultsManager(t)
	storage := &launchReceiptStore{}
	m.storage = storage
	defaults := agentcli.LaunchDefaults{ProfileID: "first"}
	m.LaunchDefaults = func(string, string) agentcli.LaunchDefaults { return defaults }
	request := agentcli.Request{Key: "lost-response", AgentID: "source", Prompt: "Start once"}
	first, err := m.StartAgent(p.ID, request, "http://localhost")
	if err != nil || first.ProfileID != "first" {
		t.Fatal(first, err)
	}
	for _, profile := range []string{"second", "missing-profile"} {
		defaults = agentcli.LaunchDefaults{ProfileID: profile, Args: []string{"new-argument"}}
		retry, err := m.StartAgent(p.ID, request, "http://localhost")
		if err != nil || retry.ID != first.ID || retry.ProfileID != "first" {
			t.Fatalf("identical retry did not return original session: %+v %v", retry, err)
		}
	}
	changed := request
	changed.Prompt = "Different work"
	if _, err := m.StartAgent(p.ID, changed, "http://localhost"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed request did not conflict", err)
	}
	stopContinuation(t, m, first)
	registry := m.Agents
	m.Close()
	reopened, err := OpenStored(m.dir, m.root, storage)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Agents, reopened.LaunchDefaults = registry, m.LaunchDefaults
	retry, err := reopened.StartAgent(p.ID, request, "http://localhost")
	if err != nil || retry.ID != first.ID || retry.Status != "stopped" {
		t.Fatal("receipt did not survive restart", retry, err)
	}
}
