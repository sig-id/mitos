package agentcli

import (
	"strings"
	"testing"
)

func TestProjectContextUsesFreshRestrictedNativeInvocation(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"codex", "claude", "pi"} {
		fakeCLI(t, dir, id, "exit 0")
	}
	r, err := New(Builtins(), Config{Profiles: []Profile{{ID: "chosen", Name: "Chosen", AgentID: "codex", Env: map[string]string{"OPENAI_API_KEY": "CHOSEN_KEY"}}}}, []string{"PATH=" + dir, "CHOSEN_KEY=chosen-secret", "OPENAI_API_KEY=wrong-account"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"codex", "claude", "pi"} {
		request := Request{AgentID: id, Prompt: "Inspect project"}
		if id == "codex" {
			request.ProfileID = "chosen"
		}
		launch, err := r.PrepareProjectContext(request, "/tmp/result.json")
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Join(launch.Args, " ")
		if launch.NativeSessionID != "" || strings.Contains(args, "--resume") || strings.Contains(args, "--continue") || !strings.HasSuffix(args, "Inspect project") {
			t.Fatal(launch)
		}
		switch id {
		case "codex":
			if !strings.Contains(args, "--sandbox read-only") || !strings.Contains(args, "--ephemeral") || !strings.Contains(strings.Join(launch.Env, "\n"), "OPENAI_API_KEY=chosen-secret") {
				t.Fatal(launch)
			}
		case "claude":
			if !strings.Contains(args, "--no-session-persistence") || !strings.Contains(args, "--tools Read,Glob,Grep") {
				t.Fatal(args)
			}
		case "pi":
			if !strings.Contains(args, "--no-session") || !strings.Contains(args, "--tools read,grep,find,ls") {
				t.Fatal(args)
			}
		}
	}
	if _, err = r.PrepareProjectContext(Request{AgentID: "shell"}, "/tmp/result"); err == nil {
		t.Fatal("shell accepted for analysis")
	}
}
