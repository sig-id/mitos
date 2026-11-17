package harness

import (
	"slices"
	"testing"
)

func TestClaudeArgumentsCarryEffort(t *testing.T) {
	args := ClaudeArguments(CodexOptions{Model: "m", Effort: "high"}, []string{"Read"})
	i := slices.Index(args, "--effort")
	if i < 0 || args[i+1] != "high" {
		t.Fatalf("effort missing: %q", args)
	}
	if slices.Contains(ClaudeArguments(CodexOptions{}, nil), "--effort") {
		t.Fatal("empty effort passed")
	}
}
