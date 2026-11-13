package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/decision"
	"mitos/internal/studio"
)

func seedPendingRun(t *testing.T, engine *studio.Engine) studio.Run {
	t.Helper()
	run, err := engine.Store.Create(studio.Brief{Title: "Inspect interrupted work", Intent: "Check the saved changes before retrying.", Workspace: engine.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}, "Workspace inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	err = engine.Store.Park(run.ID, &studio.HumanDecision{ID: "decision-test", ReasonSignal: "execution_interrupted", Question: "Restart this interrupted work?", Kind: "choose", Context: decision.Context{BlockedBy: "The previous executor stopped before completion."}, Reversibility: "external", Options: []studio.Option{{ID: "retry", Label: "Restart", Consequence: "Run the same request again."}, {ID: "cancel", Label: "Close this work", Consequence: "Keep the record without another attempt."}}})
	if err != nil {
		t.Fatal(err)
	}
	run, _ = engine.Store.Get(run.ID)
	return run
}

func TestDemoHistoryDoesNotRequestAttention(t *testing.T) {
	server, engine := testServer(t)
	if err := studio.SeedDemo(engine.Store); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/workspace-home", "/api/decisions", "/decisions", "/overview"} {
		response := request(server, "GET", path, "", false)
		if response.Code != 200 || strings.Contains(response.Body.String(), "Finish the settings migration") {
			t.Fatalf("sample leaked into attention at %s: %s", path, response.Body.String())
		}
	}
	if len(engine.Store.Attention()) != 0 {
		t.Fatal("demo requests human attention")
	}
	for _, run := range engine.Store.List() {
		response := request(server, "GET", "/runs/"+run.ID, "", false)
		body := response.Body.String()
		for _, want := range []string{"This is demo history", "no agent ran it", "belongs to no project", "Example activity", "Event details"} {
			if response.Code != 200 || !strings.Contains(body, want) {
				t.Fatalf("sample origin missing %q: %s", want, body)
			}
		}
		for _, unwanted := range []string{"Stop work", "Run again", `action="/runs/` + run.ID + `/decisions/`, `class="run-decision"`, `hx-trigger="every 3s"`} {
			if strings.Contains(body, unwanted) {
				t.Fatalf("sample exposes real execution control %q", unwanted)
			}
		}
	}
	real := seedPendingRun(t, engine)
	attention := engine.Store.Attention()
	if len(attention) != 1 || attention[0].OwnerRunID != real.ID {
		t.Fatalf("real request lost: %+v", attention)
	}
	var cards []decision.Card
	response := request(server, "GET", "/api/decisions", "", false)
	if err := json.Unmarshal(response.Body.Bytes(), &cards); err != nil || len(cards) != 1 || cards[0].RunID != real.ID {
		t.Fatalf("real pending card lost: %s", response.Body.String())
	}
}

func TestRunShowsProvenanceSavedOutputAndEventDetails(t *testing.T) {
	server, engine := testServer(t)
	project, err := engine.Desk.CreateProject("Migration project", engine.Root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := engine.Store.Create(studio.Brief{ProjectID: project.ID, Title: "Migrate settings", Intent: "Preserve existing preferences during migration.", Workspace: engine.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}, "Migration worker", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = engine.Store.SaveArtifact(run.ID, "output.txt", []byte("Agent reply: checking preferences\n<script>alert('log')</script>")); err != nil {
		t.Fatal(err)
	}
	if err = engine.Store.SaveArtifact(run.ID, "segment-002-output.txt", []byte(strings.Repeat("x", 70<<10)+"last diagnostic")); err != nil {
		t.Fatal(err)
	}
	if err = engine.Store.Transition(run.ID, "executor_failed", "failed", "Failed", "Migration command exited with code 2"); err != nil {
		t.Fatal(err)
	}
	response := request(server, "GET", "/runs/"+run.ID, "", false)
	body := response.Body.String()
	for _, want := range []string{"Manual request; individual identity was not recorded", "Migration project", "/projects/" + project.ID, "Original request", run.Brief.Intent, "Migration worker", "Agent reply: checking preferences", "last diagnostic", "Showing the last 64 KiB", "Migration command exited with code 2", "Event details", "executor_failed", "Download output.txt"} {
		if response.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("run context missing %q: %s", want, body[:min(len(body), 2000)])
		}
	}
	if strings.Contains(body, "<script>alert('log')</script>") || strings.Contains(body, strings.Repeat("x", 65<<10)) {
		t.Fatal("log preview is unescaped or unbounded")
	}
}
