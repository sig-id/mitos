package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/studio"
)

func TestScheduledWorkflowAPIAndUI(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho 'codex-cli 1.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	s, e := testServer(t)
	project, err := e.Desk.CreateProject("Scheduled work", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	body, _ := json.Marshal(studio.ScheduledGoal{ID: "workflow", ProjectID: project.ID, Goal: "Follow the procedure I specified and report the result.", Cron: "*/5 * * * *", Timezone: "UTC", Event: "repository.changed", Minutes: 10})
	for i := 0; i < 2; i++ {
		response := request(s, "POST", "/api/services/workflows", string(body), false)
		if response.Code != 201 {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	v, _ := e.Store.Services()
	if len(v.Catalog.Config.Units) != 1 {
		t.Fatal("duplicate workflow")
	}
	page := request(s, "GET", "/projects/"+project.ID, "", false)
	for _, want := range []string{"When to run", "On a schedule and external events", "workflow_name"} {
		if !strings.Contains(page.Body.String(), want) {
			t.Fatal("missing control", want)
		}
	}
	page = request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Follow the procedure I specified") {
		t.Fatal(page.Body.String())
	}
	if response := request(s, "POST", "/api/services/workflows", `{"unknown":true}`, false); response.Code != 400 {
		t.Fatal("unknown fields accepted")
	}
}
