package web

import (
	"strings"
	"testing"

	"mitos/internal/harness"
	"mitos/internal/studio"
)

func TestCostsPageRendersRecordedUsage(t *testing.T) {
	s, e := testServer(t)
	run, err := e.Store.Create(studio.Brief{Title: "Priced attempt", Intent: "Work", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	tracker := harness.NewUsageTracker("gpt-6.2-sol")
	tracker.Observe(harness.Usage{InputTokens: 1_000_000, CachedInputTokens: 2_000_000, OutputTokens: 500_000})
	if err = e.Store.RecordAgentUsage(run.ID, "mitos.engineer", "Engineer", "codex", tracker); err != nil {
		t.Fatal(err)
	}
	w := request(s, "GET", "/costs", "", false)
	if w.Code != 200 {
		t.Fatalf("/costs with recorded usage: %d %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"Engineer", "gpt-6.2-sol", "$7.20"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("cost page lost %s", want)
		}
	}
}
