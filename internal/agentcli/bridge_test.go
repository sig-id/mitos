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

func TestDiscoveredAgentsAdvertiseSupportedSessionTransport(t *testing.T) {
	bin := t.TempDir()
	for _, adapter := range Builtins() {
		fakeCLI(t, bin, adapter.Definition().Executables[0], "exit 0\n")
	}
	registry, err := New(Builtins(), Config{}, []string{"PATH=" + bin})
	if err != nil {
		t.Fatal(err)
	}
	for _, installation := range registry.List(t.TempDir()) {
		t.Run(installation.ID, func(t *testing.T) {
			if !installation.Installed {
				t.Fatal("installed executable missing from discovery")
			}
			managed := installation.ID == "codex" || installation.ID == "claude"
			if installation.Capabilities.RequestBridge != managed {
				t.Fatal("discovery advertised the wrong request integration")
			}
			_, err := registry.Prepare(Request{AgentID: installation.ID, Bridge: true})
			if (err == nil) != managed {
				t.Fatal("bridge launch disagrees with advertised capability", err)
			}
			if installation.Kind == "agent" && installation.Capabilities.InitialPrompt {
				launch, err := registry.Prepare(Request{AgentID: installation.ID, Prompt: "Inspect the project", Bridge: managed})
				if err != nil || launch.Bridge != managed || !slices.Contains(launch.Args, "Inspect the project") {
					t.Fatal("session lost its selected transport or assignment", launch, err)
				}
			}
		})
	}
}
