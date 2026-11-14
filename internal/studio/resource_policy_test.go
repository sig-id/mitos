package studio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/resourcepolicy"
)

func TestResourcePreferencesCompatibilityAndConflicts(t *testing.T) {
	modern := resourcepolicy.Policy{Workflow: resourcepolicy.Workflow{Attempts: resourcepolicy.Capped(8)}}
	effective := effectivePreferences(RuntimePreferences{Minutes: 45, Attempts: 30}, RuntimePreferences{Resources: &modern})
	if effective.Minutes != 45 || effective.Attempts != 8 || effective.Resources.Workflow.ElapsedMinutes.Amount != 45 {
		t.Fatalf("bad effective limits: %+v", effective)
	}
	if err := validatePreferences(effective); err != nil {
		t.Fatal(err)
	}
	if err := validatePreferences(RuntimePreferences{Attempts: 9, Resources: &modern}); err == nil {
		t.Fatal("conflicting legacy value accepted")
	}
	if err := validatePreferences(RuntimePreferences{Attempts: 8, Resources: &modern}); err != nil {
		t.Fatal("matching migration rejected", err)
	}
	modern.Workflow.ElapsedMinutes = resourcepolicy.Limit{Mode: "inherit", Amount: 2}
	if err := validatePreferences(RuntimePreferences{Minutes: 40, Resources: &modern}); err == nil {
		t.Fatal("legacy alias hid malformed structured policy")
	}
}
func TestUnsupportedResourceCapsCannotSaveOrLaunch(t *testing.T) {
	e, req := graphEngine(t, false)
	for _, field := range []string{"tokens", "cpu_millicores", "memory_mib"} {
		p := resourcepolicy.Policy{}
		switch field {
		case "tokens":
			p.Workflow.Tokens = resourcepolicy.Capped(100)
		case "cpu_millicores":
			p.Agent.CPUMillicores = resourcepolicy.Capped(100)
		case "memory_mib":
			p.Agent.MemoryMiB = resourcepolicy.Capped(100)
		}
		if _, err := e.SavePreferences("", RuntimePreferences{Resources: &p}); err == nil || !strings.Contains(err.Error(), field) {
			t.Fatal("unsupported preference accepted", err)
		}
	}
	setup, err := e.ProjectSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	document := "```mitos\n{\"resources\":{\"workflow\":{\"tokens\":{\"mode\":\"limit\",\"amount\":500}}}}\n```\n"
	if _, err := e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Document: &document}); err == nil {
		t.Fatal("unsupported document saved")
	}
	// Direct file edits must also fail admission, without creating a graph.
	if err := os.WriteFile(filepath.Join(e.Root, setup.FileName), []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.StartAutomaticGoal(AutomaticGoal{Key: "unsupported-caps", ProjectID: req.ProjectID, Goal: "Inspect the project"}); err == nil {
		t.Fatal("unsupported on-disk policy launched")
	}
	if len(e.Store.Graphs()) != 0 {
		t.Fatal("failed policy created work")
	}
}
func TestResourcePolicyFrozenAndInheritedByOccurrence(t *testing.T) {
	e, req := workflowEngine(t)
	req.Minutes = 0
	parent := resourcepolicy.Policy{Workflow: resourcepolicy.Workflow{ElapsedMinutes: resourcepolicy.Capped(180), Attempts: resourcepolicy.Capped(12)}}
	if _, err := e.SavePreferences("", RuntimePreferences{Resources: &parent}); err != nil {
		t.Fatal(err)
	}
	setup, err := e.ProjectSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	document := "```mitos\n{\"resources\":{\"workflow\":{\"attempts\":{\"mode\":\"limit\",\"amount\":9}}}}\n```\n"
	if _, err := e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Document: &document}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ScheduleGoal(req); err != nil {
		t.Fatal(err)
	}
	run, err := e.TriggerService(req.ID, "resources")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Store.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := e.startServiceGraph(run.ID); err != nil {
		t.Fatal(err)
	}
	g, ok := e.Store.serviceGraph(run.ID)
	if !ok || g.Setup.Resources.Workflow.Attempts.Amount != 9 || g.Limits.Attempts != 9 || g.Setup.Resources.Workflow.ElapsedMinutes.Amount != 180 || time.Until(g.Limits.Deadline) < 179*time.Minute {
		t.Fatalf("policy not frozen into runtime: %+v", g.Setup)
	}
	old, err := e.Preferences("")
	if err != nil {
		t.Fatal(err)
	}
	parent.Workflow.ElapsedMinutes = resourcepolicy.Capped(10)
	if _, err := e.SavePreferences("", RuntimePreferences{Revision: old.Revision, Resources: &parent}); err != nil {
		t.Fatal(err)
	}
	saved, err := e.Store.Graph(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Setup.Resources.Workflow.ElapsedMinutes.Amount != 180 || !saved.Limits.Deadline.Equal(g.Limits.Deadline) {
		t.Fatal("running budget changed after preferences edit")
	}
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(e.Store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	restored, err := replay.Graph(g.ID)
	if err != nil || restored.Setup.Resources != g.Setup.Resources || !restored.Limits.Deadline.Equal(g.Limits.Deadline) || restored.Attempts != g.Attempts {
		t.Fatal("replay changed resource policy or consumed budget", err)
	}
}
