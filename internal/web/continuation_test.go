package web

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"mitos/internal/agentcli"
	"mitos/internal/desk"
)

func TestSessionContinuationAPI(t *testing.T) {
	s, e := testServer(t)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "fixture", Name: "Fixture", Kind: "agent", Executables: []string{shell}, Args: []string{"-c", "echo ready; cat"}, Prompt: "positional", SessionProtocol: "claude"}}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Desk.CreateProject("Test", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	source, err := e.Desk.StartAgent(p.ID, agentcli.Request{AgentID: "fixture", Prompt: "Test sessions"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	request := map[string]string{"key": "same-request", "mode": "context", "notes": strings.Repeat("\n", 32<<10), "prompt": strings.Repeat("p", 32<<10)}
	w := deskRequest(s, "POST", "/api/sessions/"+source.ID+"/fork", request)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var fork desk.Session
	if err := json.Unmarshal(w.Body.Bytes(), &fork); err != nil {
		t.Fatal(err)
	}
	if fork.Continuation.SourceID != source.ID || fork.Continuation.Kind != "fork" {
		t.Fatal(fork)
	}
	w = deskRequest(s, "POST", "/api/sessions/"+source.ID+"/fork", request)
	var repeated desk.Session
	json.Unmarshal(w.Body.Bytes(), &repeated)
	if w.Code != 200 || repeated.ID != fork.ID {
		t.Fatal("duplicate fork", w.Code, repeated)
	}
	request["notes"] = "Changed request"
	if w := deskRequest(s, "POST", "/api/sessions/"+source.ID+"/fork", request); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := deskRequest(s, "POST", "/api/sessions/"+source.ID+"/resume", map[string]string{"key": "live"}); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := deskRequest(s, "POST", "/api/sessions/missing/fork", map[string]string{"key": "missing"}); w.Code != 404 {
		t.Fatal(w.Code)
	}
	w = deskRequest(s, "GET", "/workspace?project="+p.ID+"&session="+fork.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Source session ↗") || strings.Contains(w.Body.String(), "id=\"session-input\"") {
		t.Fatal("continuation UI", w.Code)
	}
	w = deskRequest(s, "POST", "/api/sessions/"+source.ID+"/handover", map[string]string{"key": "handover", "agent_id": "fixture"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := e.Desk.Session(source.ID)
		if v.Status == "stopped" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("handover did not stop source")
}
