package workerapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/structuredworker"
	"mitos/internal/studio"
)

func TestWorkerPersistenceFailuresAnswer5xxForRetry(t *testing.T) {
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := store.CreateWorkerCredential(studio.WorkerCredential{ID: "backend-worker", Pools: []string{"interactive"}, Agents: []string{"mitos.generalist"}, ExpiresAt: time.Now().Add(time.Hour)}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(Server{Engine: &studio.Engine{Store: store}})
	defer server.Close()
	register := func() int {
		body := `{"request_id":"register-backend","incarnation":"incarnation","capabilities":["mitos.generalist"]}`
		req, _ := http.NewRequest("POST", server.URL+"/worker/v1/register", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(structuredworker.VersionHeader, structuredworker.Version)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var failure map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		if resp.StatusCode != 500 {
			t.Fatalf("persistence failure answered %d (%v), expected a retryable 5xx", resp.StatusCode, failure)
		}
		return resp.StatusCode
	}
	// A closed journal models any backend persistence failure: the response
	// must be 5xx so the worker's stable request-ID retry recovers.
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	register()
}

func TestWorkerValidationFailuresStay4xx(t *testing.T) {
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	server := httptest.NewTLSServer(Server{Engine: &studio.Engine{Store: store}})
	defer server.Close()
	req, _ := http.NewRequest("POST", server.URL+"/worker/v1/claim", strings.NewReader(`{"request_id":"claim","incarnation":"incarnation","capabilities":["mitos.generalist"]}`))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
	req.Header.Set(structuredworker.VersionHeader, structuredworker.Version)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("authentication failure answered %d, expected non-retryable 4xx", resp.StatusCode)
	}
}
