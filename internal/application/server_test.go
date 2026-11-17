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
