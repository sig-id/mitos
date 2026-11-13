package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"mitos/internal/decision"
)

func TestDecisionJSONAndConsoleShareOneResolution(t *testing.T) {
	server, engine := testServer(t)
	seedPendingRun(t, engine)
	listed := request(server, "GET", "/api/decisions", "", false)
	var cards []decision.Card
	if listed.Code != 200 || json.Unmarshal(listed.Body.Bytes(), &cards) != nil || len(cards) == 0 {
		t.Fatal("pending cards unavailable")
	}
	card := cards[0]
	fetched := request(server, "GET", "/api/runs/"+card.RunID+"/decision", "", false)
	if fetched.Code != 200 || !strings.Contains(fetched.Body.String(), `"default":null`) || !strings.Contains(fetched.Body.String(), `"attempted"`) {
		t.Fatal("canonical card not exposed")
	}
	path := "/api/runs/" + card.RunID + "/decisions/" + card.ID
	for _, body := range []string{`{"choice":"cancel","resolver":"forged"}`, `{"choice":"cancel","resolved_at":"2000-01-01"}`, `{"choice":"cancel"} {}`} {
		req := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(body))
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatalf("client-supplied authority accepted: %d", w.Code)
		}
	}
	req := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(`{"choice":"cancel","note":"Keep this stopped"}`))
	req.Header.Set("X-Mitos-Client", "cli")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("resolution failed: %s", w.Body.String())
	}
	run, _ := engine.Store.Get(card.RunID)
	if run.Decision.Resolution == nil || run.Decision.Resolution.Resolver != "local-operator" || run.Decision.Resolution.Via != "cli" || run.Decision.AppliedAt == nil {
		t.Fatal("resolution has no bound provenance/application")
	}
	again := request(server, "POST", "/runs/"+card.RunID+"/decisions/"+card.ID, "choice=cancel", false)
	if again.Code != 303 {
		t.Fatal("console duplicate did not return the first result")
	}
	final, _ := engine.Store.Get(card.RunID)
	count := 0
	for _, event := range final.Events {
		if event.Kind == "approval_resolved" {
			count++
		}
	}
	if count != 1 || final.Decision.Resolution.Via != "cli" {
		t.Fatal("second surface replaced the first choice")
	}
}
