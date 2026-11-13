package web

import (
	"context"
	"encoding/json"
	"fmt"
	"mitos/internal/agentpolicy"
	"mitos/internal/harness"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSupervisionHTTPExternalBridgeAndResolution(t *testing.T) {
	s, e := testServer(t)
	project, err := e.Desk.CreateProject("Supervised", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	policy := agentpolicy.Default()
	policy.Rules = []agentpolicy.Rule{{ID: "blocked", Command: "forbidden", Directory: ".", Action: "deny"}}
	if _, err = e.SaveAgentPolicy(project.ID, policy); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	bridge, err := harness.NewClient(server.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = bridge.Start(context.Background(), "codex", "External", e.Root); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"method":"item/started","params":{"item":{"type":"commandExecution","command":"forbidden","cwd":%q}}}`, e.Root))
	if err = bridge.Observe(context.Background(), raw); err == nil {
		t.Fatal("veto not returned to bridge")
	}
	endpoint := "/api/projects/" + project.ID + "/supervision/incidents"
	w := deskRequest(s, "GET", endpoint, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"held":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	incident := e.Store.SupervisionIncidents(project.ID)[0]
	w = deskRequest(s, "POST", endpoint+"/"+incident.ID+"/resolve", map[string]any{"note": "Verified external process stopped", "confirmed_stopped": true})
	if w.Code != 200 || e.Store.SupervisionHeld(project.ID) {
		t.Fatal(w.Code, w.Body.String())
	}
	page := deskRequest(s, "GET", "/projects/"+project.ID, nil)
	if !strings.Contains(page.Body.String(), `id="supervision-panel"`) {
		t.Fatal("incident panel missing")
	}
}

func TestLegacyCoordinationAPIIsRemoved(t *testing.T) {
	s, e := testServer(t)
	p, err := e.Desk.CreateProject("Coordination", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	w := deskRequest(s, "GET", "/api/projects/"+p.ID+"/supervision", nil)
	if w.Code != 404 {
		t.Fatal("legacy route still served", w.Code, w.Body.String())
	}
	w = deskRequest(s, "GET", "/api/projects/"+p.ID+"/coordination", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}
