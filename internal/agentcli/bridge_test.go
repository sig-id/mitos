package agentcli

import (
	"reflect"
	"slices"
	"testing"
)

func TestBridgePreservesSelectedExecutableAndEnvironment(t *testing.T) {
	for _, id := range []string{"codex", "claude"} {
		original := Launch{Bridge: true, AgentID: id, Executable: "/custom/agent", Env: []string{"PROFILE=chosen"}, Args: []string{"--", "task"}, BridgePrompt: "task"}
		got, err := WrapBridge(original, "/bin/mitos", "http://127.0.0.1:4646", "/work", "Session")
		if err != nil || got.Executable != "/bin/mitos" || !reflect.DeepEqual(got.Env, original.Env) {
			t.Fatal(got, err)
		}
		if !slices.Contains(got.Args, "--bind-desk-session") {
			t.Fatal("hosted bridge lost explicit binding")
		}
		if got.Args[8] != "--executable" || got.Args[9] != original.Executable {
			t.Fatal(got.Args)
		}
		if id == "codex" && got.Args[len(got.Args)-1] != "task" {
			t.Fatal(got.Args)
		}
	}
}
