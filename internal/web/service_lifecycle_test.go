package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServiceLifecycleHTTPStopHarvestAndResume(t *testing.T) {
	s, e := testServer(t)
	e.Apps = append(e.Apps, studio.Application{ID: "stop-worker", Name: "Stop worker", Directory: t.TempDir(), Entrypoint: "Run", Command: []string{"/bin/sh", "-c", "printf started; printf result > result.txt; sleep 30"}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "test -s \"$MITOS_WORKSPACE/result.txt\""}}})
	b := studio.Brief{Title: "Work to stop", Workspace: e.Root, AppID: "stop-worker", TimeoutMinutes: 1}
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "worker", Scope: "project", Trigger: "manual", Relations: service.Relations{PartOf: []string{"group"}}}}, Targets: []service.Target{{ID: "group", Scope: "project"}}, Intent: service.Intent{Enable: []string{"worker"}}}, Work: map[string]studio.Brief{"worker": b}}
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	run, err := e.TriggerService("worker", "first")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(e.Store.Dir(), "workspaces", run.ID, "workspace", "result.txt")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err = os.Stat(filepath.Join(e.Store.Dir(), "workspaces", run.ID, "workspace", "result.txt")); err != nil {
		t.Fatal("worker did not start", err)
	}
	response := request(s, "POST", "/api/services/group/lifecycle", `{"action":"stop","idempotency_key":"stop-1"}`, false)
	var op studio.ServiceLifecycleView
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &op) != nil || op.Status != "stopping" || op.TaskCount != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	if response = request(s, "POST", "/api/services/worker/trigger", `{"idempotency_key":"blocked"}`, false); response.Code != 409 {
		t.Fatal("HTTP trigger bypassed stop")
	}
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		op, _ = e.Store.ServiceLifecycle(op.Seq)
		if op.Status == "stopped" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	run, _ = e.Store.Get(run.ID)
	if op.Status != "stopped" || run.Status != "cancelled" || run.Claim == nil || run.Claim.ReleasedAt == nil || run.Workspace == nil || run.Workspace.ResultCommit == "" {
		t.Fatal("shutdown did not settle and harvest", op.Status, run.Status)
	}
	if output, err := e.Store.Artifact(run.ID, "output.txt"); err != nil || !strings.Contains(string(output), "started") {
		t.Fatal("output lost", err)
	}
	for path, needle := range map[string]string{"/services": "Stopped by", fmt.Sprintf("/services/lifecycle/%d", op.Seq): "Resume triggers", "/api/service-lifecycle": "group", fmt.Sprintf("/api/service-lifecycle/%d", op.Seq): run.TaskID} {
		w := request(s, "GET", path, "", false)
		if w.Code != 200 || !strings.Contains(w.Body.String(), needle) {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
	for _, bad := range []string{`{"action":"unknown","idempotency_key":"bad"}`, `{"action":"stop","idempotency_key":"bad","unknown":1}`, `{}`} {
		if w := request(s, "POST", "/api/services/group/lifecycle", bad, false); w.Code != 400 {
			t.Fatal("malformed operation accepted", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/api/services/group/lifecycle", strings.NewReader(`{"action":"resume","idempotency_key":"cross"}`))
	r.Header.Set("Origin", "https://other.example")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin resume allowed")
	}
	resumed := request(s, "POST", "/services/group/lifecycle", "action=resume&idempotency_key=resume-1", false)
	if resumed.Code != 303 {
		t.Fatal(resumed.Body.String())
	}
	if same, err := e.ResumeService("group", "resume-1"); err != nil || same.Status != "resumed" {
		t.Fatal("form/API idempotency mismatch", err)
	}
	if _, err = e.TriggerService("worker", "after-resume"); err != nil {
		t.Fatal(err)
	}
}
