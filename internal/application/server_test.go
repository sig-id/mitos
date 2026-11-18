package application

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mitos/internal/apiversion"
	"mitos/internal/decision"
	"mitos/internal/structuredworker"
	"mitos/internal/studio"
)

func TestApplicationListenerRequiresTLSVersionAndLiveCredential(t *testing.T) {
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	principal, token, err := store.CreateServicePrincipal(studio.ServicePrincipalInput{ID: "application", Name: "Application", Tenant: decision.Tenant{Org: "tenant", Project: "app"}, Read: true, Write: true, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	handler := New(&studio.Engine{Store: store})
	plain := httptest.NewRequest("GET", "http://localhost/api/capabilities", nil)
	apiversion.Set(plain)
	plain.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, plain)
	if w.Code != 403 {
		t.Fatal("plaintext accepted", w.Code)
	}
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	call := func(path, credential, version string) (int, string) {
		req, _ := http.NewRequest("GET", server.URL+path, nil)
		if credential != "" {
			req.Header.Set("Authorization", "Bearer "+credential)
		}
		if version != "" {
			req.Header.Set(apiversion.Header, version)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	for _, tc := range []struct {
		path, token, version string
		want                 int
	}{
		{"/api/capabilities", token, "", 400}, {"/api/capabilities", token, "2", 400}, {"/api/capabilities", "", "1", 401}, {"/api/capabilities", token, "1", 200},
		{"/api/storage/backup", token, "1", 404}, {"/api/service-principals", token, "1", 404}, {"/api/projects", token, "1", 404}, {"/api/runs/foreign", token, "1", 404},
	} {
		if code, body := call(tc.path, tc.token, tc.version); code != tc.want {
			t.Fatal(tc.path, code, body)
		}
	}
	_, replacement, err := store.ChangeServicePrincipal(principal.ID, true, time.Now().Add(time.Hour), decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if code, body := call("/api/capabilities", token, "1"); code != 401 {
		t.Fatal("old keepalive credential accepted", code, body)
	}
	if code, body := call("/api/capabilities", replacement, "1"); code != 200 || strings.Contains(body, "token_hash") {
		t.Fatal(code, body)
	}
	if _, _, err = store.ChangeServicePrincipal(principal.ID, false, time.Time{}, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if code, body := call("/api/capabilities", replacement, "1"); code != 401 {
		t.Fatal("revocation delayed", code, body)
	}
}

// The recovery decision recorded for interrupted work must be answerable over
// the authenticated application listener itself.
func TestApplicationListenerRoutesDecisionResolution(t *testing.T) {
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, token, err := store.CreateServicePrincipal(studio.ServicePrincipalInput{ID: "application", Name: "Application", Tenant: decision.Tenant{Org: "tenant", Project: "app"}, Read: true, Write: true, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	handler := New(&studio.Engine{Store: store})
	server := httptest.NewTLSServer(handler)
	defer server.Close()
	post := func(path, body string) (int, string) {
		req, _ := http.NewRequest("POST", server.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		apiversion.Set(req)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := post("/api/runs/foreign/decision", `{"decision_id":"decision_x","choice":"retry"}`); code != 404 {
		t.Fatal("foreign run resolution", code, body)
	}
	if code, _ := post("/api/runs/foreign/decision", `{"choice":"retry"}`); code != 400 {
		t.Fatal("resolution without decision id accepted", code)
	}
	if code, _ := post("/api/runs/foreign/decision", `{"decision_id":"decision_x","choice":"retry","extra":1}`); code != 400 {
		t.Fatal("unknown resolution field accepted", code)
	}
}

func TestWorkerListenerHasIndependentVersionAndCredential(t *testing.T) {
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, appToken, err := store.CreateServicePrincipal(studio.ServicePrincipalInput{ID: "app", Name: "App", Tenant: decision.Tenant{Org: "tenant", Project: "app"}, Read: true, Write: true, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	_, workerToken, err := store.CreateWorkerCredential(studio.WorkerCredential{ID: "worker", Pools: []string{"interactive"}, Agents: []string{"mitos.generalist"}, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(New(&studio.Engine{Store: store}))
	defer server.Close()
	call := func(token, version string) int {
		req, _ := http.NewRequest("POST", server.URL+"/worker/v1/register", strings.NewReader(`{"request_id":"register","incarnation":"incarnation","capabilities":["mitos.generalist"]}`))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(structuredworker.VersionHeader, version)
		apiversion.Set(req)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := call(workerToken, ""); code != 400 {
		t.Fatal("application version admitted worker", code)
	}
	if code := call(appToken, "1"); code != 401 {
		t.Fatal("application token admitted worker", code)
	}
	if code := call(workerToken, "1"); code != 200 {
		t.Fatal("worker registration", code)
	}
	selector, _ := http.NewRequest("POST", server.URL+"/worker/v1/claim", strings.NewReader(`{"id":"worker","incarnation":"incarnation","request_id":"choose-run","run_id":"chosen-run"}`))
	selector.Header.Set("Authorization", "Bearer "+workerToken)
	selector.Header.Set(structuredworker.VersionHeader, "1")
	selected, err := server.Client().Do(selector)
	if err != nil {
		t.Fatal(err)
	}
	selected.Body.Close()
	if selected.StatusCode != 400 {
		t.Fatal("remote selector bypasses scheduler fairness", selected.StatusCode)
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/capabilities", nil)
	apiversion.Set(req)
	req.Header.Set("Authorization", "Bearer "+workerToken)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("worker token admitted application", resp.StatusCode)
	}
	if err = store.RevokeWorker("worker", decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if code := call(workerToken, "1"); code != 401 {
		t.Fatal("live worker revocation", code)
	}
}

func TestApplicationVersionInputsUseTenantCASAndBoundedFeed(t *testing.T) {
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	create := func(id, tenant string, write bool) string {
		_, token, err := store.CreateServicePrincipal(studio.ServicePrincipalInput{ID: id, Name: id, Tenant: decision.Tenant{Org: tenant, Project: "app"}, Read: true, Write: write, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	alpha := create("version-alpha", "alpha", true)
	beta := create("version-beta", "beta", true)
	reader := create("version-reader", "alpha", false)
	server := httptest.NewTLSServer(New(&studio.Engine{Store: store}))
	defer server.Close()
	input := map[string]any{"key": "preferences", "data": map[string]string{"marker": "alpha-only-version-data"}, "versions": []map[string]any{{"name": "preferences", "expected": 0, "value": 1}}}
	raw, status := fixtureCall(t, server.Client(), "POST", server.URL+"/api/inputs", alpha, input)
	if status != 201 {
		t.Fatal(status, string(raw))
	}
	if _, status = fixtureCall(t, server.Client(), "POST", server.URL+"/api/inputs", reader, input); status != 403 {
		t.Fatal("read-only version write", status)
	}
	input["key"] = "wrong-cas"
	if _, status = fixtureCall(t, server.Client(), "POST", server.URL+"/api/inputs", alpha, input); status != 409 {
		t.Fatal("stale version CAS", status)
	}
	raw, status = fixtureCall(t, server.Client(), "GET", server.URL+"/api/events?after=0&limit=1", beta, nil)
	if status != 200 || strings.Contains(string(raw), "alpha-only-version-data") {
		t.Fatal("global version feed escaped tenant", status, string(raw))
	}
	raw, status = fixtureCall(t, server.Client(), "POST", server.URL+"/api/inputs", beta, input)
	if status != 201 {
		t.Fatal("tenant versions not independent", status, string(raw))
	}
}
