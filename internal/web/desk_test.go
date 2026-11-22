package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/agentcli"
	"mitos/internal/desk"
	"mitos/internal/harness"
)

func TestLocalProjectAndAgentOutsideDefaultWorkspace(t *testing.T) {
	s, e := testServer(t)
	directory := t.TempDir()
	w := deskRequest(s, "POST", "/api/projects", map[string]string{"name": "Sibling project", "directory": directory})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var project desk.Project
	if err := json.Unmarshal(w.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	form := deskRequest(s, "GET", "/new?project="+project.ID, nil)
	if form.Code != 200 || !strings.Contains(form.Body.String(), directory) {
		t.Fatal("new task did not select project workspace", form.Code)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	bridge, err := harness.NewClient(server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := bridge.Start(context.Background(), "codex", "Project agent", directory); err != nil {
		t.Fatal("opened project's agent cannot connect", err)
	}
	// Runs carry the canonical workspace spelling; temp roots are
	// symlinked on some platforms.
	resolvedDir, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if path, err := e.Workspace(directory); err != nil || path != resolvedDir {
		t.Fatal("confirmed workspace rejected by task runner", path, err)
	}
	if _, err := e.Workspace(t.TempDir()); err == nil {
		t.Fatal("unconfirmed workspace accepted")
	}
}

func deskRequest(s *Server, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := versioned(httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(string(b))))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestWorkspaceRealSessionAndTopicAPI(t *testing.T) {
	s, e := testServer(t)
	w := deskRequest(s, "POST", "/api/projects", map[string]string{"name": "<script>Project</script>", "directory": e.Root})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var p desk.Project
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents, err = agentcli.New(agentcli.Builtins(), agentcli.Config{Applications: []agentcli.Definition{{ID: "reader", Name: "Reader", Kind: "application", Executables: []string{shell}, Args: []string{"-c", "read -r x; printf 'received:%s\\n' \"$x\""}, Prompt: "none"}}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	w = deskRequest(s, "GET", "/api/agents?project="+p.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"reader"`) {
		t.Fatal("agent catalog unavailable", w.Body.String())
	}
	w = deskRequest(s, "GET", "/api/agents/reader?project="+p.ID, nil)
	var installation agentcli.Installation
	if err := json.Unmarshal(w.Body.Bytes(), &installation); err != nil || w.Code != 200 || !installation.Installed || installation.Capabilities.InitialPrompt {
		t.Fatal("incorrect adapter capabilities", w.Body.String(), err)
	}
	w = deskRequest(s, "GET", "/api/agents/reader?project=missing", nil)
	if w.Code != 404 {
		t.Fatal("unknown workspace inspected", w.Code)
	}
	w = deskRequest(s, "POST", "/api/projects/"+p.ID+"/sessions", map[string]string{"agent_id": "reader", "role": "shell"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var session desk.Session
	_ = json.Unmarshal(w.Body.Bytes(), &session)
	w = deskRequest(s, "GET", "/workspace?project="+p.ID+"&session="+session.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "id=\"terminal\"") || strings.Contains(w.Body.String(), "<script>Project</script>") {
		t.Fatalf("workspace: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self';") {
		t.Fatal("scripts not restricted")
	}
	w = deskRequest(s, "POST", "/api/sessions/"+session.ID+"/input", map[string]string{"key": "one", "data": "hello\r"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := e.Desk.Session(session.ID)
		if v.Status == "exited" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	w = deskRequest(s, "GET", "/api/sessions/"+session.ID+"/output?offset=0", nil)
	var out desk.Output
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if !strings.Contains(string(out.Data), "received:hello") {
		t.Fatalf("real output missing: %s", w.Body.String())
	}
	w = deskRequest(s, "POST", "/api/projects/"+p.ID+"/topics", map[string]any{"title": "API", "summary": "Discover this", "paths": []string{"src/api"}})
	var topic desk.Topic
	_ = json.Unmarshal(w.Body.Bytes(), &topic)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = deskRequest(s, "POST", "/api/topics/"+topic.ID+"/posts", map[string]any{"body": "Private thread details", "revision": 1})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = deskRequest(s, "GET", "/api/projects/"+p.ID+"/topics", nil)
	if strings.Contains(w.Body.String(), "Private thread details") {
		t.Fatal("summary index exposes full messages")
	}
	w = deskRequest(s, "POST", "/api/topics/"+topic.ID+"/posts", map[string]any{"body": "stale", "revision": 1})
	if w.Code != 409 {
		t.Fatal("stale reply admitted")
	}
	w = deskRequest(s, "GET", "/api/topics/"+topic.ID+"?after=1", nil)
	if !strings.Contains(w.Body.String(), "Private thread details") {
		t.Fatal("topic delta missing")
	}
}
func TestWorkspaceRejectsMalformedInputAndCrossOrigin(t *testing.T) {
	s, e := testServer(t)
	p, err := e.Desk.CreateProject("P", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"name":"x","role":"shell","unknown":1}`, `{"name":"x","role":"shell"} {}`} {
		r := versioned(httptest.NewRequest("POST", "http://localhost/api/projects/"+p.ID+"/sessions", strings.NewReader(body)))
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("malformed request admitted")
		}
	}
	r := versioned(httptest.NewRequest("POST", "http://localhost/api/projects", strings.NewReader(`{"name":"x","directory":"."}`)))
	r.Header.Set("Origin", "https://other.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin mutation admitted")
	}
	w = deskRequest(s, "GET", "/api/sessions/unknown/output?offset=0", nil)
	if w.Code != 404 {
		t.Fatal("unknown output accepted")
	}
}

func TestResponsibilityMessageAPI(t *testing.T) {
	server, engine := testServer(t)
	project, err := engine.Desk.CreateProject("Inbox", engine.Root)
	if err != nil {
		t.Fatal(err)
	}
	task, err := engine.Desk.CreateWork(project.ID, desk.NewWork{Key: "inbox", Title: "Coordinate work", Intent: "Review findings", Criteria: "Record review", Role: "orchestrator"})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + project.ID + "/responsibilities/" + task.ResponsibilityID + "/messages"
	posted := deskRequest(server, "POST", path, map[string]string{"body": "Review the findings"})
	if posted.Code != 200 {
		t.Fatal(posted.Code, posted.Body.String())
	}
	var receipt struct {
		ID    string
		Route string
	}
	if err := json.Unmarshal(posted.Body.Bytes(), &receipt); err != nil || receipt.Route != "inbox" || receipt.ID == "" {
		t.Fatal("post did not return the routed delivery", err, receipt)
	}
	for _, path := range []string{path, "/api/watch-inboxes/responsibility/" + task.ResponsibilityID} {
		page := deskRequest(server, "GET", path, nil)
		if page.Code != 200 || !strings.Contains(page.Body.String(), receipt.ID) || !strings.Contains(page.Body.String(), "Review the findings") {
			t.Fatal("missing from delivery view", path, page.Code, page.Body.String())
		}
	}
	if page := deskRequest(server, "GET", strings.Replace(path, project.ID, "other-project", 1), nil); page.Code != 404 {
		t.Fatal("cross-project history read", page.Code)
	}
}
