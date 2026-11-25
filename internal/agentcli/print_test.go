package agentcli

import (
	"encoding/json"
	"slices"
	"testing"
)

// Every coordinated CLI must have a non-interactive invocation, accept a
// selected model verbatim and expose only efforts it can pass to that CLI.
func TestCoordinatedPrintArguments(t *testing.T) {
	modes := map[string]string{"opencode": "run", "kilo": "run", "pi": "--print", "gemini": "--output-format", "antigravity": "--input-format", "qwen": "--output-format", "copilot": "-s", "goose": "run", "amp": "--execute", "cursor": "--print", "cline": "--acp", "aider": "--message-file"}
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
			if spec.ID == "cline" && !slices.Equal(args[:len(args)-2], []string{"--acp", "--auto-approve", "false"}) {
				t.Fatalf("Cline must request permission for native tools: %v", args)
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

func TestEditApprovalModes(t *testing.T) {
	for runtime, mode := range map[string]string{"gemini": "auto_edit", "qwen": "auto-edit"} {
		args, err := PrintArguments(runtime, "", "", func(capability string) bool { return capability == "workspace.write" })
		if err != nil || !slices.Equal(args, []string{"--output-format", "text", "--approval-mode", mode}) {
			t.Fatalf("%s: %v %v", runtime, args, err)
		}
	}
}

func TestAntigravityPrintSettings(t *testing.T) {
	for _, tc := range []struct {
		grant, mode, permission string
		sandbox                 bool
	}{
		{"workspace.read", "", "request-review", false},
		{"workspace.write", "accept-edits", "request-review", false},
		{"command.run", "", "proceed-in-sandbox", true},
	} {
		t.Run(tc.grant, func(t *testing.T) {
			args, err := PrintArguments("antigravity", "gemini-4-carbon", "high", func(capability string) bool { return capability == tc.grant })
			if err != nil || len(args) != 11 || !slices.Equal(args[:6], []string{"--input-format", "stream-json", "--output-format", "stream-json", "--disable-slash-commands", "--settings"}) || !slices.Equal(args[7:], []string{"--model", "gemini-4-carbon", "--effort", "high"}) {
				t.Fatal(args, err)
			}
			var settings map[string]any
			if err = json.Unmarshal([]byte(args[6]), &settings); err != nil {
				t.Fatal(err)
			}
			sandbox, _ := settings["enableTerminalSandbox"].(bool)
			if settings["agentMode"] != tc.mode || settings["toolPermission"] != tc.permission || settings["allowNonWorkspaceAccess"] != false || sandbox != tc.sandbox {
				t.Fatal(settings)
			}
		})
	}
}
