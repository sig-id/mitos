package studio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectSetupPreservesRepositoryAndRejectsStaleSave(t *testing.T) {
	e, req := graphEngine(t, false)
	original := "# Team instructions\n\nPreserve this exact content.\n"
	path := filepath.Join(e.Root, "AGENTS.md")
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0664); err != nil {
		t.Fatal(err)
	}
	setup, err := e.ProjectSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	sections := SetupSections{Context: "# PLC control\n\nSafety-related project.", Constraints: "Spend at most $20 as guidance; ask about ambiguity.", Git: "Review all changes before publishing."}
	saved, err := e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Sections: &sections})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.Document, original) || saved.Sections != sections {
		t.Fatal("existing instructions or sections lost")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0664 {
		t.Fatal("existing file permissions changed", err)
	}
	if _, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Sections: &sections}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale file overwrite accepted")
	}
	external := saved.Document + "\nExternal edit\n"
	if err = os.WriteFile(path, []byte(external), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: saved.FileHash, Sections: &sections}); !errors.Is(err, ErrConflict) {
		t.Fatal("external edit overwritten")
	}
	saved, err = e.ProjectSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	sections.Context = "Updated context"
	saved, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: saved.FileHash, Sections: &sections})
	if err != nil || !strings.HasSuffix(saved.Document, "\nExternal edit\n") {
		t.Fatalf("suffix not preserved: %v", err)
	}
}
func TestProjectSetupLowercaseAndSymlink(t *testing.T) {
	e, req := graphEngine(t, false)
	path := filepath.Join(e.Root, "agents.md")
	if err := os.WriteFile(path, []byte("# Existing lowercase\n"), 0600); err != nil {
		t.Fatal(err)
	}
	setup, err := e.ProjectSetup(req.ProjectID)
	if err != nil || setup.FileName != "agents.md" {
		t.Fatal("lowercase file ignored", err)
	}
	if _, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Sections: &SetupSections{Context: "Context"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(e.Root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("created duplicate uppercase file")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private.md")
	os.WriteFile(outside, []byte("Private"), 0600)
	if err = os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err = e.ProjectSetup(req.ProjectID); err == nil {
		t.Fatal("followed instructions symlink")
	}
}
func TestSetupDefaultsOverridesAndFrozenExecution(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, false)
	global, err := e.SavePreferences("", RuntimePreferences{Runtime: "codex", Model: "global-model", Minutes: 30, Attempts: 40, Guidance: "Private writing preference"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.SavePreferences("", RuntimePreferences{}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale defaults accepted")
	}
	if _, err = e.SavePreferences(req.ProjectID, RuntimePreferences{Runtime: "pi"}); err == nil {
		t.Fatal("unsupported automatic runtime accepted")
	}
	_, err = e.SavePreferences(req.ProjectID, RuntimePreferences{Model: "project-model", Minutes: 10, Attempts: 12})
	if err != nil {
		t.Fatal(err)
	}
	setup, err := e.ProjectSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	sections := SetupSections{Context: "Frozen original guidance", Git: "Keep evidence"}
	setup, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Sections: &sections})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(setup.Document, global.Guidance) {
		t.Fatal("personal preference leaked to repository")
	}
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "setup-goal", ProjectID: req.ProjectID, Goal: "Inspect project setup"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Setup.Model != "project-model" || g.Setup.Personal != global.Guidance || g.Limits.Attempts != 12 || time.Until(g.Limits.Deadline) > 10*time.Minute {
		t.Fatal("settings not applied")
	}
	sections.Context = "New guidance"
	if _, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Sections: &sections}); err != nil {
		t.Fatal(err)
	}
	stored, _ := e.Store.Graph(g.ID)
	if !strings.Contains(stored.Setup.Document, "Frozen original guidance") || strings.Contains(stored.Setup.Document, "New guidance") {
		t.Fatal("running setup changed")
	}
	if !strings.Contains(stored.Setup.prompt(), stored.Setup.Document) {
		t.Fatal("orchestrator did not get full setup")
	}
}
