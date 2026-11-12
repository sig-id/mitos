package studio

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDocumentExecutionPolicy(t *testing.T) {
	for _, doc := range []string{"```mitos\n{\"minutes\":-1}\n```", "```mitos\n{\"cost\":20}\n```", "```mitos\n{}", "```mitos\n{}\n```\n```mitos\n{}\n```"} {
		if _, err := documentPolicy(doc); err == nil {
			t.Fatalf("invalid policy accepted: %s", doc)
		}
	}
	p, err := documentPolicy("# Instructions\r\n```mitos\r\n{\"minutes\":15,\"attempts\":8,\"model\":\"example-model\"}\r\n```\r\n")
	if err != nil || p.Minutes != 15 || p.Attempts != 8 || p.Model != "example-model" {
		t.Fatalf("policy: %+v %v", p, err)
	}
	p, err = documentPolicy("Spend about an hour on this high-risk project.")
	if err != nil || p.Minutes != 0 {
		t.Fatal("prose became an enforced limit")
	}
}
func TestSetupSuggestionTargetsAndQuestions(t *testing.T) {
	s, err := parseSetupSuggestion([]byte(`{"message":"Which review policy should apply?"}`), "project")
	if err != nil || s.Document != nil || s.Preferences != nil {
		t.Fatal(s, err)
	}
	for _, raw := range []string{`{"message":"Update","preferences":{"runtime":"codex"}}`, `{"message":"Update","document":""}`, `{"message":"Update","document":"Hello","unexpected":true}`} {
		if _, err := parseSetupSuggestion([]byte(raw), "project"); err == nil {
			t.Fatal("invalid proposal accepted", raw)
		}
	}
}
func TestSetupAssistantDraftReviewAndPersistence(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
out=''
last=''
while [ "$#" -gt 0 ]; do
 if [ "$1" = '--output-last-message' ]; then shift; out="$1"; fi
 last="$1"
 shift
done
printf '%s' "$last" > "$SETUP_CAPTURE"
sleep 0.1
printf '%s' '{"message":"Added your review policy.","document":"# Team instructions\n\nRequire review before publishing.\n\nFENCEmitos\n{\"minutes\":15,\"attempts\":8,\"model\":\"setup-model\"}\nFENCE\n"}' > "$out"
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(strings.ReplaceAll(script, "FENCE", "```")), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	capture := filepath.Join(t.TempDir(), "prompt.txt")
	t.Setenv("SETUP_CAPTURE", capture)
	e, req := graphEngine(t, true)
	if _, err := e.SavePreferences("", RuntimePreferences{Guidance: "PRIVATE SENTINEL"}); err != nil {
		t.Fatal(err)
	}
	ask := SetupAsk{Key: "setup-first", Runtime: "codex", Target: "project", Prompt: "Require review before publishing"}
	first, err := e.AskSetup(req.ProjectID, ask)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.AskSetup(req.ProjectID, ask)
	if err != nil || again.Key != first.Key {
		t.Fatal("replay failed", err)
	}
	if _, err := os.Stat(filepath.Join(e.Root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("assistant wrote the file before review")
	}
	var c SetupConversation
	for until := time.Now().Add(5 * time.Second); time.Now().Before(until); {
		c, err = e.SetupConversation(req.ProjectID, "project")
		if err != nil {
			t.Fatal(err)
		}
		if c.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if c.Status != "ready" || c.Suggestion.Document == nil {
		t.Fatalf("draft: %+v", c)
	}
	captured, err := os.ReadFile(capture)
	if err != nil || strings.Contains(string(captured), "PRIVATE SENTINEL") {
		t.Fatal("personal guidance sent to project setup assistant", err)
	}
	path, _ := e.setupConversationPath(req.ProjectID, "project")
	saved, err := loadSetupConversation(path)
	if err != nil || len(saved.Messages) != 2 {
		t.Fatal("conversation not persisted", err)
	}
	if _, err := os.Stat(filepath.Join(e.Root, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatal("proposal changed the file")
	}
	document := *c.Suggestion.Document
	_, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: c.FileHash, Document: &document})
	if err != nil {
		t.Fatal(err)
	}
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "policy-goal", ProjectID: req.ProjectID, Goal: "Review instructions"})
	if err != nil {
		t.Fatal(err)
	}
	if g.Limits.Attempts != 8 || g.Setup.Model != "setup-model" || time.Until(g.Limits.Deadline) > 15*time.Minute {
		t.Fatal("file limits were not enforced")
	}
	if _, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: c.FileHash, Document: &document}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale proposal overwrote file", err)
	}
	// A stale running record is observable as interrupted after a server restart.
	saved.Status = "running"
	if err = saveSetupConversation(path, saved); err != nil {
		t.Fatal(err)
	}
	c, err = e.SetupConversation(req.ProjectID, "project")
	if err != nil || c.Status != "interrupted" {
		t.Fatal("lost running state hidden", err)
	}
	if strings.Contains(document, "Personal") {
		t.Fatal("unexpected personal guidance")
	}
}
