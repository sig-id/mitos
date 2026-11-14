package agentcli

import (
	"reflect"
	"testing"
)

func TestLaunchDefaultsPreserveArgumentsAndExplicitProfile(t *testing.T) {
	dir := t.TempDir()
	fakeCLI(t, dir, "codex", "exit 0\n")
	r, err := New(Builtins(), Config{Profiles: []Profile{{ID: "shared", Name: "Shared", AgentID: "codex"}, {ID: "explicit", Name: "Explicit", AgentID: "codex"}}}, []string{"PATH=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	d := LaunchDefaults{ProfileID: "shared", Args: []string{"--model", "two words; $(literal)"}}
	request := Request{AgentID: "codex", Prompt: "--not-a-flag", Defaults: d}
	launch, err := r.Prepare(request)
	if err != nil || launch.ProfileID != "shared" || !reflect.DeepEqual(launch.Args, []string{"--model", "two words; $(literal)", "--", "--not-a-flag"}) {
		t.Fatal(launch.Args, err)
	}
	request.ProfileID = "explicit"
	launch, err = r.PrepareContinuation(request, "resume", "11111111-2222-4333-8444-555555555555")
	if err != nil || launch.ProfileID != "explicit" || !reflect.DeepEqual(launch.Args, []string{"--model", "two words; $(literal)", "resume", "11111111-2222-4333-8444-555555555555", "--", "--not-a-flag"}) {
		t.Fatal(launch.Args, err)
	}
	plain, err := r.Prepare(Request{AgentID: "codex"})
	if err != nil || len(plain.Args) != 0 || plain.ProfileID != "" {
		t.Fatal("defaults leaked into unrelated launch", plain.Args, err)
	}
}
