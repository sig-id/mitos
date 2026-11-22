package studio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		env, err := nucleusAgentEnvironment(runtime, nucleusAgentRuntimes[runtime].vendor, base, "", false, server)
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
	env, err := nucleusAgentEnvironment("codex", compute.BrokerVendorOpenAI, base, prior, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(envValue(env, "CODEX_HOME"), "sessions", "2026", "rollout-thread.jsonl")); err != nil {
		t.Fatalf("native conversation not seeded: %v", err)
	}
	var prerequisite *agentPrerequisiteError
	if _, err = nucleusAgentEnvironment("claude", compute.BrokerVendorAnthropic, t.TempDir(), prior, true, nil); !errors.As(err, &prerequisite) {
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

// The native workflow with Pi as the project runtime, for a live check under
// Nucleus: MITOS_TEST_NATIVE_PI=1 with MITOS_AGENT_CONFINEMENT=nucleus, a
// Nix-store pi on PATH and a brokerable key for Pi's default provider.
func TestAutomaticInstalledPi(t *testing.T) {
	if os.Getenv("MITOS_TEST_NATIVE_PI") != "1" {
		t.Skip("set MITOS_TEST_NATIVE_PI=1 for the authenticated native Pi smoke test")
	}
	e, req := workflowTestEngine(t)
	if _, err := e.SavePreferences(req.ProjectID, RuntimePreferences{Runtime: "pi"}); err != nil {
		t.Fatal(err)
	}
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "native-pi", ProjectID: req.ProjectID, Goal: "Use exactly one researcher agent to read hello.txt and report its exact contents. Do not edit any files. The orchestrator should review that report and finish; no additional workers are needed."})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(15 * time.Minute)
	for time.Now().Before(until) {
		if g, err = e.Store.Graph(g.ID); err != nil {
			t.Fatal(err)
		}
		if g.Status == "accepted" {
			for _, m := range g.Members {
				if r, err := e.Store.Get(m.CurrentRunID); err == nil {
					t.Logf("%s: %s %s", r.AppName, r.Status, r.Summary)
				}
			}
			t.Logf("Pi finished with %d attempts and %d orchestrator calls", g.Attempts, g.OrchestratorCalls)
			return
		}
		for _, x := range g.Expansions {
			if x.Error != "" {
				logPiMembers(t, e, g)
				t.Fatal(x.Error)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	logPiMembers(t, e, g)
	t.Fatalf("native Pi workflow did not complete within 15 minutes: graph %s after %d attempts and %d orchestrator calls", g.Status, g.Attempts, g.OrchestratorCalls)
}

func logPiMembers(t *testing.T, e *Engine, g WorkGraph) {
	for _, m := range g.Members {
		if m.CurrentRunID == "" {
			continue
		}
		r, _ := e.Store.Get(m.CurrentRunID)
		out, _ := e.Store.Artifact(r.ID, "output.txt")
		if len(out) > 4000 {
			out = out[len(out)-4000:]
		}
		t.Logf("%s: %s %s\n%s", r.AppName, r.Status, r.Summary, out)
	}
}

func writePiConfig(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPiBrokersItsDefaultProviderWithAStoredAPIKey(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".pi", "agent")
	writePiConfig(t, dir, map[string]string{
		"settings.json": `{"defaultProvider":"zai","defaultModel":"glm-5.3","defaultThinkingLevel":"max","theme":"dark","packages":["npm:x"]}`,
		"auth.json":     `{"zai":{"type":"api_key","key":"zai-real"},"anthropic":{"type":"oauth","access":"tok"}}`,
		"models.json":   `{"providers":{"zai":{"apiKey":"!pass show zai","headers":{"X-Secret":"s"},"models":[{"id":"glm-5.3","headers":{"X-M":"m"},"contextWindow":1000000}]},"other":{"baseUrl":"https://x"}}}`,
	})
	server := []string{"HOME=" + home, "PATH=/bin"}
	vendor, key, err := nucleusAgentCredential("pi", server)
	if err != nil || vendor != compute.BrokerVendorZAI || key != "zai-real" {
		t.Fatalf("pi credential %s %q %v", vendor, key, err)
	}
	// A server variable wins over the stored login.
	if _, key, _ = nucleusAgentCredential("pi", append(server, "ZAI_API_KEY=zai-env")); key != "zai-env" {
		t.Fatalf("server key %q", key)
	}
	base := t.TempDir()
	env, err := nucleusAgentEnvironment("pi", vendor, base, "", false, server)
	if err != nil {
		t.Fatal(err)
	}
	config := envValue(env, "PI_CODING_AGENT_DIR")
	if config != filepath.Join(base, "home", ".pi", "agent") || envValue(env, "PI_OFFLINE") != "1" || envValue(env, "PI_TELEMETRY") != "0" {
		t.Fatalf("pi environment %v", env)
	}
	if _, err = os.Stat(filepath.Join(config, "auth.json")); err == nil {
		t.Fatal("stored login copied into the sandbox")
	}
	models, _ := os.ReadFile(filepath.Join(config, "models.json"))
	for _, leaked := range []string{"pass show", "X-Secret", "X-M", "zai-real", "other"} {
		if strings.Contains(string(models), leaked) {
			t.Fatalf("sandbox models.json carries %q: %s", leaked, models)
		}
	}
	var parsed struct {
		Providers map[string]struct {
			BaseURL string           `json:"baseUrl"`
			APIKey  string           `json:"apiKey"`
			Models  []map[string]any `json:"models"`
		} `json:"providers"`
	}
	if json.Unmarshal(models, &parsed) != nil || parsed.Providers["zai"].BaseURL != "http://"+compute.EgressLoopAddr+"/zai/api/coding/paas/v4" || parsed.Providers["zai"].APIKey != compute.BrokerPlaceholderKey || len(parsed.Providers["zai"].Models) != 1 {
		t.Fatalf("sandbox models.json %s", models)
	}
	settings, _ := os.ReadFile(filepath.Join(config, "settings.json"))
	if !strings.Contains(string(settings), `"defaultModel": "glm-5.3"`) || strings.Contains(string(settings), "packages") {
		t.Fatalf("sandbox settings.json %s", settings)
	}
}

func TestPiRefusesUnbrokeredProvidersAndLogins(t *testing.T) {
	var prerequisite *agentPrerequisiteError
	for name, files := range map[string]map[string]string{
		"no default":    {"settings.json": `{}`},
		"unserved":      {"settings.json": `{"defaultProvider":"google"}`},
		"oauth login":   {"settings.json": `{"defaultProvider":"anthropic"}`, "auth.json": `{"anthropic":{"type":"oauth","key":"tok"}}`},
		"command key":   {"settings.json": `{"defaultProvider":"zai"}`, "auth.json": `{"zai":{"type":"api_key","key":"!pass show zai"}}`},
		"env reference": {"settings.json": `{"defaultProvider":"zai"}`, "auth.json": `{"zai":{"type":"api_key","key":"$ZAI"}}`},
	} {
		dir := t.TempDir()
		writePiConfig(t, dir, files)
		if _, _, err := nucleusAgentCredential("pi", []string{"PI_CODING_AGENT_DIR=" + dir}); !errors.As(err, &prerequisite) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
