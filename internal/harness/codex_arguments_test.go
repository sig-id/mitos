package harness

import (
	"slices"
	"testing"
)

func TestCodexArgumentsDisableNativeDelegation(t *testing.T) {
	got := CodexArguments(`model="fixture"`)
	want := []string{"app-server", "-c", "features.multi_agent=false", "-c", "features.multi_agent_v2=false", "-c", `model="fixture"`}
	if !slices.Equal(got, want) {
		t.Fatalf("codex arguments = %q, want %q", got, want)
	}
}

func TestClaudeArgumentsRestrictNativeTools(t *testing.T) {
	args := ClaudeArguments(CodexOptions{}, []string{"Read", "Bash"})
	value := func(flag string) string {
		t.Helper()
		i := slices.Index(args, flag)
		if i < 0 || i+1 >= len(args) {
			t.Fatalf("missing %s in %q", flag, args)
		}
		return args[i+1]
	}
	if got := value("--tools"); got != "Read,Bash" {
		t.Fatalf("--tools = %q", got)
	}
	if got := value("--disallowedTools"); got != "Agent,Task" {
		t.Fatalf("--disallowedTools = %q", got)
	}
	// An empty source list keeps user, project and local settings (and the
	// agents and hooks they define) out of the worker.
	if got := value("--setting-sources"); got != "" {
		t.Fatalf("--setting-sources = %q", got)
	}
	if !slices.Contains(args, "--strict-mcp-config") {
		t.Fatalf("missing --strict-mcp-config in %q", args)
	}
	if got := value("--mcp-config"); got != `{"mcpServers":{"mitos":{"type":"sdk","name":"mitos"}}}` {
		t.Fatalf("--mcp-config = %q", got)
	}
}
