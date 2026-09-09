package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/autonomy"
	"mitos/internal/studio"
)

func TestCheckpointAPIAndPhoneDecisionUseSharedRecovery(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "health"), []byte("healthy"), 0600); err != nil {
		t.Fatal(err)
	}
	app := studio.Application{ID: "recover-app", Name: "Recover", Entrypoint: "Run", Directory: t.TempDir(), Supervision: &autonomy.Supervision{Strategy: "escalate"}, Command: []string{"/bin/sh", "-c", `request=$(cat); case "$request" in *'"number":1'*) printf broken > health; exit 1;; *) test "$(cat health)" = healthy;; esac`}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", `test "$(cat "$MITOS_WORKSPACE/health")" = healthy`}}}
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	engine, err := studio.NewEngine(store, []studio.Application{app}, root)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	server, err := New(engine, false)
	if err != nil {
		t.Fatal(err)
	}
	run, err := engine.Launch(studio.Brief{Title: "Restore this checkpoint", Workspace: root, AppID: app.ID, TimeoutMinutes: 1}, "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = store.Get(run.ID)
		if run.Status == "parked" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != "parked" {
		t.Fatalf("run did not park: %s %s", run.Status, run.Summary)
	}
	response := request(server, "GET", "/api/runs/"+run.ID+"/checkpoints", "", false)
	var checkpoints []autonomy.Checkpoint
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &checkpoints) != nil || len(checkpoints) != 2 || !checkpoints[0].Green {
		t.Fatalf("invalid checkpoint API: %s", response.Body.String())
	}
	page := request(server, "GET", "/runs/"+run.ID, "", false)
	if !strings.Contains(page.Body.String(), "RECOVERY POINTS") || !strings.Contains(page.Body.String(), `value="rewind"`) {
		t.Fatal("recovery was not offered in the run view")
	}
	bad := httptest.NewRequest("POST", "http://localhost/api/runs/"+run.ID+"/rewind", strings.NewReader(`{"scope":"step","from_segment":1,"reason_signal":"operator_recovery"}`))
	bad.Header.Set("Content-Type", "application/json")
	rejected := httptest.NewRecorder()
	server.ServeHTTP(rejected, bad)
	if rejected.Code != http.StatusConflict {
		t.Fatalf("memo-free API recovery accepted: %d", rejected.Code)
	}
	for _, body := range []string{
		`{"scope":"step","memo":"repair","reason_signal":"operator_recovery"}`,
		`{"scope":"step","memo":"repair","reason_signal":"operator_recovery","from_segment":1,"applied_at":"2026-09-09T00:00:00Z"}`,
		`{"scope":"step","memo":"repair","reason_signal":"operator_recovery","from_segment":1} {}`,
	} {
		req := httptest.NewRequest("POST", "http://localhost/api/runs/"+run.ID+"/rewind", strings.NewReader(body))
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid recovery body accepted: %d %s", recorder.Code, body)
		}
	}
	path := "/runs/" + run.ID + "/decisions/" + run.Decision.ID
	for range 2 {
		response = request(server, "POST", path, "choice=rewind", false)
		if response.Code != http.StatusSeeOther {
			t.Fatalf("phone decision failed: %d %s", response.Code, response.Body.String())
		}
	}
	for time.Now().Before(deadline) {
		run, _ = store.Get(run.ID)
		if run.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != "done" || len(run.Segments) != 2 || len(run.Rewinds) != 1 {
		t.Fatalf("decision did not recover exactly once: %s %s segments=%d", run.Status, run.Summary, len(run.Segments))
	}
}
