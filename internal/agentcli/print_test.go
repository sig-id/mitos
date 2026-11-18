package agentcli

import (
	"slices"
	"testing"
)

// Every coordinated CLI must have a non-interactive invocation, accept a
// selected model verbatim and expose only efforts it can pass to that CLI.
func TestCoordinatedPrintArguments(t *testing.T) {
	modes := map[string]string{"opencode": "run", "kilo": "run", "pi": "--print", "gemini": "--output-format", "qwen": "--output-format", "copilot": "-s", "goose": "run", "amp": "--execute", "cursor": "--print", "cline": "--auto-approve", "aider": "--message-file"}
	for _, adapter := range Builtins() {
		spec := adapter.Definition()
		if spec.Kind != "agent" {
			continue
		}
		if !adapter.Capabilities().CoordinatedWork {
			t.Fatalf("missing adapter for %s", spec.ID)
		}
		if !PrintRuntime(spec.ID) {
			continue
		}
		t.Run(spec.ID, func(t *testing.T) {
			granted := func(string) bool { return false }
			args, err := PrintArguments(spec.ID, "provider/model", "", granted)
			if err != nil || args[0] != modes[spec.ID] || !slices.Equal(args[len(args)-2:], []string{"--model", "provider/model"}) {
				t.Fatalf("%s: %v", args, err)
			}
			if spec.ID == "pi" && !slices.Contains(args, "--no-tools") {
				t.Fatal(args)
			}
			for _, effort := range adapter.Capabilities().ReasoningEfforts {
				args, err = PrintArguments(spec.ID, "provider/model", effort, granted)
				if err != nil || args[len(args)-1] != effort {
					t.Fatalf("%s: %v", args, err)
				}
			}
		})
	}
	if _, err := PrintArguments("shell", "", "", nil); err == nil {
		t.Fatal("shell accepted")
	}
}
