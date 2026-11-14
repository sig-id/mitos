package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/agentcli"
	"mitos/internal/studio"
)

func TestSystemAndProjectConfigurationAPI(t *testing.T) {
	s, e := testServer(t)
	page := request(s, "GET", "/settings", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "System Configuration") || !strings.Contains(page.Body.String(), "/static/settings.js") {
		t.Fatal(page.Code, page.Body.String())
	}
	config := studio.SystemConfiguration{Agents: map[string]agentcli.LaunchDefaults{"codex": {Args: []string{"--model", "shared"}}}}
	w := deskRequest(s, "PUT", "/api/settings", config)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = deskRequest(s, "PUT", "/api/settings", config); w.Code != 409 {
		t.Fatal("stale write", w.Code)
	}
	p, err := e.Desk.CreateProject("Project", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Configuration studio.SystemConfiguration `json:"configuration"`
		Defaults      studio.SystemConfiguration `json:"defaults"`
	}
	w = request(s, "GET", "/api/projects/"+p.ID+"/settings", "", false)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Configuration.Revision != 1 || len(out.Configuration.Agents) != 0 || len(out.Defaults.Agents) != 1 {
		t.Fatal(w.Code, w.Body.String())
	}
	config = out.Configuration
	config.Agents = map[string]agentcli.LaunchDefaults{"codex": {Args: []string{}}}
	w = deskRequest(s, "PUT", "/api/projects/"+p.ID+"/settings", config)
	if w.Code != 200 || len(e.Store.AgentLaunchDefaults(p.ID, "codex").Args) != 0 || len(e.Store.AgentLaunchDefaults("other", "codex").Args) != 2 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/settings?project=missing", "/api/projects/missing/settings"} {
		if w = request(s, "GET", path, "", false); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	config = e.Store.SystemConfiguration()
	config.Agents = map[string]agentcli.LaunchDefaults{"missing": {}}
	if w = deskRequest(s, "PUT", "/api/settings", config); w.Code != 422 {
		t.Fatal("unknown agent accepted", w.Code)
	}
}
