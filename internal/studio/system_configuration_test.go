package studio

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"mitos/internal/agentcli"
	"mitos/internal/decision"
)

func TestSystemConfigurationInheritanceAndRevocation(t *testing.T) {
	var globalCalls, overrideCalls atomic.Int32
	global := choiceServer(t, func() string { return "approve" }, &globalCalls)
	defer global.Close()
	override := choiceServer(t, func() string { return "approve" }, &overrideCalls)
	defer override.Close()
	e, req := graphEngine(t, true)
	other, err := e.Desk.CreateProject("Other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c, err := e.SaveSystemConfiguration(SystemConfiguration{DecisionProvider: modelConfig(global.URL, "before_segment"), Agents: map[string]agentcli.LaunchDefaults{"codex": {Args: []string{"--model", "shared"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.SaveSystemConfiguration(SystemConfiguration{}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale global write accepted", err)
	}
	for _, project := range []string{req.ProjectID, other.ID} {
		p := e.Store.AgentPolicy(project)
		p.Autonomy, p.Guidance = "decision", "Allow printf reviewed."
		if project == other.ID {
			p.DecisionProvider = modelConfig(override.URL, "before_segment")
			p.AgentLaunch = map[string]agentcli.LaunchDefaults{"codex": {Args: []string{}}}
		}
		if _, err = e.SaveAgentPolicy(project, p); err != nil {
			t.Fatal(err)
		}
		session, token, err := e.Store.CreateBoundAgentSession("codex", "Review", "/tmp", project, "", "", decision.LocalOperator())
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(codexApproval))
		if err != nil {
			t.Fatal(err)
		}
		if err = e.ResolveAgentRequest(context.Background(), session.ID, token, r.RequestID); err != nil {
			t.Fatal(err)
		}
		v, err := e.Store.AgentRequestStatus(session.ID, token, r.RequestID)
		if err != nil || v.State != "accepted" {
			t.Fatal(v, err)
		}
		if project == req.ProjectID {
			old := e.Store.AgentPolicy(project)
			if old.DecisionProvider != nil {
				t.Fatal("inherited provider copied into project overrides")
			}
			c, err = e.SaveSystemConfiguration(c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.SaveAgentPolicy(project, old); !errors.Is(err, ErrConflict) {
				t.Fatal("stale project write accepted", err)
			}
			if _, err = e.Store.AgentRequestStatus(session.ID, token, r.RequestID); err == nil {
				t.Fatal("stale automatic answer delivered")
			}
		}
	}
	if globalCalls.Load() != 1 || overrideCalls.Load() != 1 {
		t.Fatal(globalCalls.Load(), overrideCalls.Load())
	}
	if d := e.Store.AgentLaunchDefaults(req.ProjectID, "codex"); len(d.Args) != 2 {
		t.Fatal(d)
	}
	if d := e.Store.AgentLaunchDefaults(other.ID, "codex"); len(d.Args) != 0 {
		t.Fatal("explicit empty override did not replace defaults", d)
	}
	// Removing a project override restores inheritance without copying defaults.
	p := e.Store.AgentPolicy(other.ID)
	p.DecisionProvider = nil
	p.AgentLaunch = nil
	if _, err = e.SaveAgentPolicy(other.ID, p); err != nil {
		t.Fatal(err)
	}
	if d := e.Store.AgentLaunchDefaults(other.ID, "codex"); len(d.Args) != 2 {
		t.Fatal(d)
	}
}

func TestSystemConfigurationReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := SystemConfiguration{Revision: 3, DecisionProvider: modelConfig("https://example.com/decisions", "before_segment"), Agents: map[string]agentcli.LaunchDefaults{"codex": {Args: []string{"--model", "shared"}}}}
	s.mu.Lock()
	err = s.commit("", "effect", "system_configuration_changed", change{SystemConfiguration: &c})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := s.SystemConfiguration()
	if got.Revision != 3 || got.DecisionProvider.Endpoint != c.DecisionProvider.Endpoint || len(s.AgentLaunchDefaults("future-project", "codex").Args) != 2 {
		t.Fatal(got)
	}
	got.Agents["codex"] = agentcli.LaunchDefaults{}
	if len(s.SystemConfiguration().Agents["codex"].Args) != 2 {
		t.Fatal("configuration aliases store")
	}
}
