package structuredworker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mitos/agents"
	"mitos/internal/compute"
)

// A failing runtime still yields its captured output and activity: failed
// attempts keep the evidence needed to diagnose them.
func TestExecutePreservesEvidenceWhenTheRuntimeFails(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'partial analysis before the crash' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	executor := Executor{Provider: &compute.Local{}, Executable: script, Base: dir, Preview: io.Discard, Activity: io.Discard}
	assignment := Context{Binding: Binding{RunID: "run_execute_test", BudgetSeconds: 30}, Definition: agents.Definition{ID: "mitos.generalist"}, Prompt: "Answer with the schema.", ResultSchema: []byte(`{"type":"object"}`), MaxTurns: 5}
	result, err := executor.Execute(context.Background(), assignment)
	if err == nil {
		t.Fatal("expected the failing runtime to fail the attempt")
	}
	names := []string{}
	for _, artifact := range result.Artifacts {
		names = append(names, artifact.Name)
	}
	for _, want := range []string{"output.txt", "activity.jsonl"} {
		if !slices.Contains(names, want) {
			t.Fatalf("failed attempt lost %s: %v", want, names)
		}
	}
	var output []byte
	for _, artifact := range result.Artifacts {
		if artifact.Name == "output.txt" {
			output = artifact.Data
		}
	}
	if !strings.Contains(string(output), "partial analysis before the crash") {
		t.Fatalf("captured output was discarded: %q", output)
	}
}
