package agentcli

import (
	"reflect"
	"testing"
)

func TestNativeSessionArguments(t *testing.T) {
	uuid := "11111111-2222-4333-8444-555555555555"
	for _, tc := range []struct {
		protocol, mode, reference string
		want                      []string
	}{
		{"codex", "resume", uuid, []string{"resume", uuid, "--", "--not-a-flag"}},
		{"codex", "fork", uuid, []string{"fork", uuid, "--", "--not-a-flag"}},
		{"claude", "resume", uuid, []string{"--resume", uuid, "--", "--not-a-flag"}},
		{"claude", "fork", uuid, []string{"--resume", uuid, "--fork-session", "--", "--not-a-flag"}},
		{"opencode", "resume", "ses_123456789", []string{"--session", "ses_123456789", "--prompt", "--not-a-flag"}},
		{"opencode", "fork", "ses_123456789", []string{"--session", "ses_123456789", "--fork", "--prompt", "--not-a-flag"}},
		{"pi", "resume", uuid, []string{"--session", uuid, "--", "--not-a-flag"}},
		{"pi", "fork", uuid, []string{"--fork", uuid, "--", "--not-a-flag"}},
		{"antigravity", "resume", uuid, []string{"--conversation", uuid, "--prompt-interactive", "--not-a-flag"}},
	} {
		t.Run(tc.protocol+"/"+tc.mode, func(t *testing.T) {
			prompt := "positional"
			if tc.protocol == "opencode" {
				prompt = "--prompt"
			}
			if tc.protocol == "antigravity" {
				prompt = "--prompt-interactive"
			}
			c := CLI{Definition{SessionProtocol: tc.protocol, Prompt: prompt}}
			args, err := c.SessionArguments(tc.mode, tc.reference, "--not-a-flag")
			if err != nil || !reflect.DeepEqual(args, tc.want) {
				t.Fatal(args, err)
			}
			for _, invalid := range []string{"", "--last", "latest", "short-id", uuid + "\n"} {
				if _, err := c.SessionArguments(tc.mode, invalid, ""); err == nil {
					t.Fatal("ambiguous reference accepted", invalid)
				}
			}
		})
	}
}

func TestAntigravityNativeLaunch(t *testing.T) {
	dir := t.TempDir()
	agy := fakeCLI(t, dir, "agy", `echo 1.5.16`)
	r, err := New(Builtins(), Config{}, []string{"PATH=" + dir, "HOME=" + dir, "GEMINI_API_KEY=fixture"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Inspect("antigravity", "", dir)
	if err != nil || a.Executable != agy || !a.Capabilities.InitialPrompt || !a.Capabilities.Resume || a.Capabilities.Fork || a.Capabilities.RequestBridge {
		t.Fatal(a, err)
	}
	launch, err := r.Prepare(Request{AgentID: "antigravity", Prompt: "Inspect the workspace"})
	if err != nil || !reflect.DeepEqual(launch.Args, []string{"--prompt-interactive", "Inspect the workspace"}) || launch.NativeSessionID != "" {
		t.Fatal(launch, err)
	}
	const conversation = "11111111-2222-4333-8444-555555555555"
	if _, err = r.PrepareContinuation(Request{AgentID: "antigravity"}, "fork", conversation); err == nil {
		t.Fatal("native fork accepted")
	}
}

func TestAssignedConversationIDs(t *testing.T) {
	for _, protocol := range []string{"claude", "pi"} {
		r, err := New([]Adapter{CLI{Definition{ID: protocol, SessionProtocol: protocol, Name: protocol, Kind: "agent", Executables: []string{"/bin/sh"}, Prompt: "positional"}}}, Config{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		a, err := r.Prepare(Request{AgentID: protocol})
		if err != nil {
			t.Fatal(err)
		}
		b, err := r.Prepare(Request{AgentID: protocol})
		if err != nil {
			t.Fatal(err)
		}
		if a.NativeSessionID == b.NativeSessionID || !nativeUUID.MatchString(a.NativeSessionID) {
			t.Fatal("missing distinct conversation identities", a, b)
		}
		if !reflect.DeepEqual(a.Args, []string{"--session-id", a.NativeSessionID}) {
			t.Fatal(a.Args)
		}
		resumed, err := r.PrepareContinuation(Request{AgentID: protocol}, "resume", a.NativeSessionID)
		if err != nil || resumed.NativeSessionID != a.NativeSessionID {
			t.Fatal(resumed, err)
		}
		forked, err := r.PrepareContinuation(Request{AgentID: protocol}, "fork", a.NativeSessionID)
		if err != nil || forked.NativeSessionID != "" {
			t.Fatal("fork must not claim the source identity", forked, err)
		}
	}
	c := CLI{Definition{Prompt: "none"}}
	if c.Capabilities().Resume || c.Capabilities().Fork {
		t.Fatal("unsupported session capability advertised")
	}
	if _, err := c.SessionArguments("resume", "11111111-2222-4333-8444-555555555555", ""); err == nil {
		t.Fatal("unsupported resume accepted")
	}
}
