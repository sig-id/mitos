package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"mitos/internal/apiversion"
)

func TestAPIRequiresSupportedVersion(t *testing.T) {
	s, _ := testServer(t)
	for name, version := range map[string]string{"missing": "", "unsupported": "2", "malformed": "v1"} {
		r := httptest.NewRequest("GET", "http://localhost/api/runs", nil)
		if version != "" {
			r.Header.Set(apiversion.Header, version)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var body struct {
			Error     string   `json:"error"`
			Supported []string `json:"supported_versions"`
		}
		if w.Code != 400 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error != "unsupported_api_version" || len(body.Supported) != 1 {
			t.Fatalf("%s version: %d %s", name, w.Code, w.Body.String())
		}
	}
	w := request(s, "GET", "/api/runs", "", false)
	if w.Code != 200 || w.Header().Get(apiversion.Header) != apiversion.Current {
		t.Fatalf("versioned request: %d %v", w.Code, w.Header())
	}
	// Pages and static assets are not part of the versioned API.
	if w := request(s, "GET", "/static/api-version.js", "", false); w.Code != 200 {
		t.Fatalf("static asset: %d", w.Code)
	}
}

func TestApplicationCapabilitiesAndStructuredAdmission(t *testing.T) {
	s, _ := testServer(t)
	w := request(s, "GET", "/api/capabilities", "", false)
	var caps struct {
		Versions   []string        `json:"api_versions"`
		Structured map[string]any  `json:"structured_work"`
		Hosted     map[string]bool `json:"hosted"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &caps) != nil || caps.Versions[0] != "1" || caps.Structured["workspace"] != false {
		t.Fatalf("capabilities: %d %s", w.Code, w.Body.String())
	}
	for name, want := range map[string]bool{"service_principal_auth": true, "tenant_isolation": true, "remote_workers": true, "mqtt_work_claims": true, "conversation_waits": false, "postgresql": false} {
		if caps.Hosted[name] != want {
			t.Fatalf("hosted capability %s: got %v want %v", name, caps.Hosted[name], want)
		}
	}

	post := func(body string) *httptest.ResponseRecorder {
		r := versioned(httptest.NewRequest("POST", "http://localhost/api/tasks", strings.NewReader(body)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	schema := `{"type":"object","additionalProperties":false,"required":["answer"],"properties":{"answer":{"type":"integer"}}}`
	if w := post(`{"title":"Open schema","app_id":"agent:mitos.generalist","timeout_minutes":5,"structured":{"key":"k1","input":{"q":1},"result_schema":{"type":"object","properties":{}}}}`); w.Code != 422 {
		t.Fatalf("open schema admitted: %d %s", w.Code, w.Body.String())
	}
	if w := post(`{"title":"With workspace","workspace":".","app_id":"agent:mitos.generalist","timeout_minutes":5,"structured":{"key":"k2","input":{"q":1},"result_schema":` + schema + `}}`); w.Code != 422 {
		t.Fatalf("workspace admitted for structured work: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "GET", "/api/runs/missing/result", "", false); w.Code != 404 {
		t.Fatalf("missing result: %d", w.Code)
	}
	if w := request(s, "POST", "/api/runs/missing/cancel", "", false); w.Code != 404 {
		t.Fatalf("missing cancel: %d %s", w.Code, w.Body.String())
	}
}
