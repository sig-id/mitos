package web

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"mitos/internal/agentcli"
	"mitos/internal/desk"
)

func TestAgentStatusEndpointRecordsSessionReports(t *testing.T) {
	s, e := testServer(t)
	w := deskRequest(s, "POST", "/api/projects", map[string]string{"name": "Status project", "directory": e.Root})
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var p desk.Project
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents, err = agentcli.New(agentcli.Builtins(), agentcli.Config{Applications: []agentcli.Definition{{ID: "sleeper", Name: "Sleeper", Kind: "agent", Executables: []string{shell}, Args: []string{"-c", "sleep 30"}, Prompt: "positional"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w = deskRequest(s, "POST", "/api/projects/"+p.ID+"/sessions", map[string]string{"agent_id": "sleeper", "prompt": "Stay alive"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session desk.Session
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	t.Cleanup(func() { _ = e.Desk.Stop(session.ID) })

	w = deskRequest(s, "POST", "/api/sessions/"+session.ID+"/agent-status", map[string]any{
		"state": "retry_scheduled", "error": "429: usage limit reached", "http_code": 429,
		"attempt": 2, "max_attempts": 3, "model": "zai/glm-5.3", "retry_in_seconds": 45,
		"usage": map[string]any{"zai/glm-5.3": map[string]any{"input_tokens": 900, "output_tokens": 80}},
	})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	stored, err := e.Desk.Session(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Retry == nil || stored.Retry.Status != "scheduled" || stored.Retry.Attempt != 2 || stored.AgentError == nil || stored.AgentError.Code != 429 || stored.Usage["zai/glm-5.3"].InputTokens != 900 {
		t.Fatal("endpoint report not stored on the session", stored.Retry, stored.AgentError, stored.Usage)
	}
	w = deskRequest(s, "POST", "/api/sessions/"+session.ID+"/agent-status", map[string]any{"state": "explode"})
	if w.Code == 200 {
		t.Fatal("unknown state accepted")
	}
	w = deskRequest(s, "POST", "/api/sessions/session_missing/agent-status", map[string]any{"state": "healthy"})
	if w.Code == 200 {
		t.Fatal("unknown session accepted")
	}
	// Sessions surfaced through the API expose the live recovery position.
	w = deskRequest(s, "GET", "/api/sessions/"+session.ID, nil)
	if !strings.Contains(w.Body.String(), `"retry_state"`) || !strings.Contains(w.Body.String(), `"model_usage"`) {
		t.Fatal("session JSON does not expose retry and usage evidence", w.Body.String())
	}
}
