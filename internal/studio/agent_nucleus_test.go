package studio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mitos/agents"
	"mitos/internal/compute"
)

func TestNucleusAgentsNeedABrokerableAPIKey(t *testing.T) {
	vendor, key, err := nucleusAgentCredential("codex", []string{"OPENAI_API_KEY=sk-oai", "CODEX_API_KEY=sk-codex"})
	if err != nil || vendor != compute.BrokerVendorOpenAI || key != "sk-oai" {
		t.Fatalf("codex credential %s %s %v", vendor, key, err)
	}
	if _, key, _ = nucleusAgentCredential("codex", []string{"CODEX_API_KEY=sk-codex"}); key != "sk-codex" {
		t.Fatalf("codex fallback key %q", key)
	}
	secret := filepath.Join(t.TempDir(), "openai")
	if err = os.WriteFile(secret, []byte("sk-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, env := range [][]string{{"OPENAI_API_KEY=" + secret}, {"OPENAI_API_KEY_FILE=" + secret}} {
		if _, key, err = nucleusAgentCredential("codex", env); err != nil || key != "sk-from-file" {
			t.Fatalf("%v: key %q %v", env, key, err)
		}
	}
	var prerequisite *agentPrerequisiteError
	if _, _, err = nucleusAgentCredential("codex", []string{"OPENAI_API_KEY_FILE=" + secret + ".missing"}); !errors.As(err, &prerequisite) {
		t.Fatalf("unreadable key file: %v", err)
	}
	// A subscription login is not an API key: the agent is refused, not run
	// with a credential the broker cannot supply.
	if _, _, err = nucleusAgentCredential("claude", []string{"HOME=/home/operator"}); !errors.As(err, &prerequisite) || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("claude without key: %v", err)
	}
	if _, _, err = nucleusAgentCredential("cline", []string{"OPENAI_API_KEY=sk"}); !errors.As(err, &prerequisite) {
		t.Fatalf("unbrokered runtime: %v", err)
	}
}

func TestNucleusAgentEnvironmentHoldsNoHostCredential(t *testing.T) {
	server := []string{"PATH=/run/current-system/sw/bin", "LANG=C.UTF-8", "HOME=/home/operator", "OPENAI_API_KEY=sk-oai-real", "ANTHROPIC_API_KEY=sk-ant-real", "GITHUB_TOKEN=ghp-real", "AWS_SECRET_ACCESS_KEY=aws-real", "CODEX_HOME=/home/operator/.codex"}
	for _, runtime := range []string{"codex", "claude"} {
		base := t.TempDir()
		env, err := nucleusAgentEnvironment(runtime, base, "", false, server)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(env, "\n")
		for _, secret := range []string{"sk-oai-real", "sk-ant-real", "ghp-real", "aws-real", "/home/operator"} {
			if strings.Contains(joined, secret) {
				t.Fatalf("%s environment carries %s:\n%s", runtime, secret, joined)
			}
		}
		if envValue(env, "HOME") != filepath.Join(base, "home") || envValue(env, "PATH") == "" || envValue(env, "LANG") != "C.UTF-8" {
			t.Fatalf("%s environment %v", runtime, env)
		}
		switch runtime {
		case "codex":
			config := envValue(env, "CODEX_HOME")
			raw, err := os.ReadFile(filepath.Join(config, "auth.json"))
			var auth map[string]string
			if err != nil || json.Unmarshal(raw, &auth) != nil || auth["auth_mode"] != "apikey" || auth["OPENAI_API_KEY"] != compute.BrokerPlaceholderKey {
				t.Fatalf("codex login %s: %v", raw, err)
			}
			if !strings.HasPrefix(config, base) {
				t.Fatalf("codex home %s outside the attempt", config)
			}
		case "claude":
			if envValue(env, "ANTHROPIC_API_KEY") != compute.BrokerPlaceholderKey || envValue(env, "ANTHROPIC_BASE_URL") != "http://"+compute.EgressLoopAddr+"/anthropic" || !strings.HasPrefix(envValue(env, "CLAUDE_CONFIG_DIR"), base) {
				t.Fatalf("claude environment %v", env)
			}
		}
	}
	if config := nucleusCodexConfig(); config != `openai_base_url="http://127.0.0.1:3128/openai/v1"` {
		t.Fatalf("codex base url config %s", config)
	}
}

func TestNucleusAgentResumeSeedsOnlyThePreviousAttemptsState(t *testing.T) {
	prior := t.TempDir()
	session := filepath.Join(prior, "home", ".codex", "sessions", "2026", "rollout-thread.jsonl")
	if err := os.MkdirAll(filepath.Dir(session), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	env, err := nucleusAgentEnvironment("codex", base, prior, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(envValue(env, "CODEX_HOME"), "sessions", "2026", "rollout-thread.jsonl")); err != nil {
		t.Fatalf("native conversation not seeded: %v", err)
	}
	var prerequisite *agentPrerequisiteError
	if _, err = nucleusAgentEnvironment("claude", t.TempDir(), prior, true, nil); !errors.As(err, &prerequisite) {
		t.Fatalf("resume without the previous attempt's state: %v", err)
	}
}

func TestNucleusBrokerSocketFitsAUnixAddress(t *testing.T) {
	path := nucleusBrokerSocket("/var/lib/mitos/data", "run_0123456789abcdef0123456789abcdef")
	if len(path) > 107 || filepath.Dir(path) != "/var/lib/mitos/data/brokers" || path == nucleusBrokerSocket("/var/lib/mitos/data", "run_other") {
		t.Fatalf("broker socket %s", path)
	}
}

// Under Nucleus a withheld write is a read-only mount and general network
// access is withheld outright: the receipt says so instead of claiming the
// harness enforces it.
func TestNucleusAuthorityReceiptReflectsTheBrokeredPosture(t *testing.T) {
	caps := compute.Capabilities{CPUQuota: true, MemoryLimit: true, Isolation: "container", Egress: "broker", DataMount: "bind-ro", NetModes: []string{compute.NetOff, compute.NetBroker}, WorkspaceConfined: true, ReadOnlyWorkspace: true}
	var d agents.Definition
	d.ID, d.Revision = "researcher", 1
	d.Manifest.Capabilities.Optional = []string{"workspace.read", "workspace.write", "network.read"}
	d.Manifest.Limits.MaxTurns, d.Manifest.Limits.TimeoutSeconds = 10, 600
	receipt, err := resolveAuthority(d, "codex", EnforcementAdvisory, 0, "review", caps)
	if err != nil {
		t.Fatal(err)
	}
	write, network := receipt.capability("workspace.write"), receipt.capability("network.read")
	if write.Granted || write.Status != CapabilityEnforced {
		t.Fatalf("withheld write %+v", write)
	}
	if network.Granted || network.Status != CapabilityEnforced || !strings.Contains(network.Note, "credential broker") {
		t.Fatalf("network %+v", network)
	}
}

func TestAgentNucleusRequiresTheNucleusProvider(t *testing.T) {
	t.Setenv("MITOS_AGENT_CONFINEMENT", compute.ConfinementNucleus)
	t.Setenv("MITOS_NUCLEUS", "")
	root := t.TempDir()
	s, err := OpenStore(filepath.Join(root, ".mitos"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewEngine(s, nil, root); err == nil || !strings.Contains(err.Error(), "MITOS_NUCLEUS") {
		t.Fatalf("agents under nucleus started without it: %v", err)
	}
}
