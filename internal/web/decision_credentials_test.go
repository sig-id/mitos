package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/studio"
)

func TestConnectionKeySaveTestReloadAndSwitch(t *testing.T) {
	for _, scope := range []string{"system", "project"} {
		t.Run(scope, func(t *testing.T) {
			s, e := testServer(t)
			path := "/api/settings"
			if scope == "project" {
				p, err := e.Desk.CreateProject("Credential test", e.Root)
				if err != nil {
					t.Fatal(err)
				}
				path = "/api/projects/" + p.ID + "/settings"
			}
			calls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer web-test-secret" {
					t.Error("missing test credential")
				}
				_, _ = w.Write([]byte(`{"model":"test-model","answers":{"action":{"type":"choice","choice":"connected","confidence":0.99,"probabilities":{"connected":0.99,"unavailable":0.01}}}}`))
			}))
			defer provider.Close()
			c := map[string]any{"protocol": "systemone", "endpoint": provider.URL, "model": "test-model", "api_key": "web-test-secret", "hooks": []string{"before_segment"}, "instructions": "Choose connected.", "timeout_ms": 1000, "max_calls": 4, "min_confidence": .8, "min_probability": .8}
			w := deskRequest(s, "POST", "/api/settings/decision-provider/test", map[string]any{"config": c})
			if w.Code != 200 || calls != 1 {
				t.Fatal("direct test failed", w.Code, w.Body.String())
			}
			if _, err := os.Stat(filepath.Join(e.Store.Dir(), "credentials")); !os.IsNotExist(err) {
				t.Fatal("connection test persisted key", err)
			}
			w = deskRequest(s, "PUT", path, map[string]any{"revision": 0, "decision_provider": c})
			if w.Code != 200 || strings.Contains(w.Body.String(), "web-test-secret") {
				t.Fatal("save failed or leaked key", w.Code)
			}
			var saved studio.SystemConfiguration
			if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || saved.DecisionProvider.APIKeyRef == "" {
				t.Fatal("missing key reference", err)
			}
			w = request(s, "GET", path, "", false)
			if w.Code != 200 || strings.Contains(w.Body.String(), "web-test-secret") || !strings.Contains(w.Body.String(), saved.DecisionProvider.APIKeyRef) {
				t.Fatal("reload lost key or exposed it")
			}
			w = deskRequest(s, "POST", "/api/settings/decision-provider/test", map[string]any{"config": saved.DecisionProvider})
			if w.Code != 200 || calls != 2 {
				t.Fatal("saved connection test failed", w.Code, w.Body.String())
			}
			w = deskRequest(s, "PUT", path, saved)
			if w.Code != 200 {
				t.Fatal("unchanged save failed", w.Code, w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
				t.Fatal(err)
			}
			// A stale write must not create a credential file.
			w = deskRequest(s, "PUT", path, map[string]any{"revision": 0, "decision_provider": c})
			if w.Code != 409 {
				t.Fatal("stale write accepted", w.Code)
			}
			entries, err := os.ReadDir(filepath.Join(e.Store.Dir(), "credentials"))
			if err != nil || len(entries) != 1 {
				t.Fatal("stale write stored a key", err)
			}
			t.Setenv("WEB_TEST_DECISION_KEY", "web-test-secret")
			saved.DecisionProvider.APIKeyRef = ""
			saved.DecisionProvider.APIKeyEnv = "WEB_TEST_DECISION_KEY"
			w = deskRequest(s, "PUT", path, saved)
			if w.Code != 200 {
				t.Fatal("env switch failed", w.Code, w.Body.String())
			}
			w = deskRequest(s, "POST", "/api/settings/decision-provider/test", map[string]any{"config": saved.DecisionProvider})
			if w.Code != 200 || calls != 3 {
				t.Fatal("env test failed", w.Code, w.Body.String())
			}
		})
	}
}
