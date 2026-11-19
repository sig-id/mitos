package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mitos/internal/compute"
)

// Agents under Nucleus (ADR 0008). Nucleus is the only enforcing layer: the
// harness runs without its own sandbox, sees the workspace copy mounted by
// role, and holds no credential. It reaches its model API only through a
// per-attempt broker that adds the operator's API key on the host.

// nucleusAgentRuntime names, per harness that can run under Nucleus, the
// vendor API its broker serves and the server environment variables that may
// hold that vendor's API key, in order of preference.
var nucleusAgentRuntimes = map[string]struct {
	vendor, configDir, configEnv string
	keys                         []string
}{
	"codex":  {compute.BrokerVendorOpenAI, ".codex", "CODEX_HOME", []string{"OPENAI_API_KEY", "CODEX_API_KEY"}},
	"claude": {compute.BrokerVendorAnthropic, ".claude", "CLAUDE_CONFIG_DIR", []string{"ANTHROPIC_API_KEY"}},
	// Pi's vendor is the operator's default Pi provider; see piVendors.
	"pi": {"", filepath.Join(".pi", "agent"), "PI_CODING_AGENT_DIR", nil},
}

// piVendors are the Pi providers the broker serves: the server variable
// holding each one's API key and the path Pi's built-in provider uses below
// its base URL.
var piVendors = map[string]struct{ key, path string }{
	compute.BrokerVendorZAI:       {"ZAI_API_KEY", "/api/coding/paas/v4"},
	compute.BrokerVendorOpenAI:    {"OPENAI_API_KEY", "/v1"},
	compute.BrokerVendorAnthropic: {"ANTHROPIC_API_KEY", ""},
}

// nucleusAgentPassEnv lists the only server environment variables an agent
// under Nucleus inherits. Everything else, credentials included, stays on the
// host.
var nucleusAgentPassEnv = []string{"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ", "NO_COLOR", "COLORTERM"}

// nucleusAgentCredential finds the API key the broker will add for runtime.
// Subscription logins are not brokered, so an agent without an API key is
// refused before any workspace is prepared.
func nucleusAgentCredential(runtime string, env []string) (vendor, key string, err error) {
	rt, ok := nucleusAgentRuntimes[runtime]
	if !ok {
		return "", "", &agentPrerequisiteError{summary: fmt.Sprintf("Only Codex, Claude Code and Pi run under Nucleus; %s has no brokered model access. Choose one of them, or run agents without MITOS_AGENT_CONFINEMENT=nucleus.", runtime)}
	}
	if runtime == "pi" {
		return piCredential(env)
	}
	for _, name := range rt.keys {
		value, err := secretValue(env, name)
		if err != nil {
			return "", "", &agentPrerequisiteError{summary: fmt.Sprintf("The %s credential for the Mitos broker cannot be read: %v", rt.vendor, err)}
		}
		if value != "" {
			return rt.vendor, value, nil
		}
	}
	return "", "", &agentPrerequisiteError{summary: fmt.Sprintf("Agents under Nucleus reach the %s API only through the Mitos credential broker, which needs %s (or the same name with _FILE) in the server environment. Subscription logins are not brokered; set the API key, or run agents without MITOS_AGENT_CONFINEMENT=nucleus.", rt.vendor, strings.Join(rt.keys, " or "))}
}

// secretValue reads a credential from the environment. NAME_FILE names a
// file holding it, as with container secrets; a NAME whose value is an
// absolute path does the same, as secret managers such as sops-nix and agenix
// provide it. No API key starts with a slash, so the two never collide. The
// file is read on every attempt, so a rotated key applies without a restart.
// piConfigDir is the operator's Pi configuration directory.
func piConfigDir(env []string) string {
	if dir := envValue(env, "PI_CODING_AGENT_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(envValue(env, "HOME"), ".pi", "agent")
}

// piDefaultProvider is the provider the operator's Pi uses by default.
func piDefaultProvider(env []string) (string, error) {
	var settings struct {
		Provider string `json:"defaultProvider"`
	}
	raw, err := os.ReadFile(filepath.Join(piConfigDir(env), "settings.json"))
	if err == nil {
		err = json.Unmarshal(raw, &settings)
	}
	if err != nil || settings.Provider == "" {
		return "", &agentPrerequisiteError{summary: "Pi under Nucleus uses the default provider of the operator's Pi configuration, and none is set. Choose a default provider in Pi."}
	}
	if _, ok := piVendors[settings.Provider]; !ok {
		return "", &agentPrerequisiteError{summary: fmt.Sprintf("Pi's default provider %s is not brokered under Nucleus; the broker serves zai, openai and anthropic.", settings.Provider)}
	}
	return settings.Provider, nil
}

// piCredential finds the API key for Pi's default provider: the provider's
// server variable, or the API key Pi stored at login. A stored OAuth login is
// not an API key and is not brokered.
func piCredential(env []string) (vendor, key string, err error) {
	if vendor, err = piDefaultProvider(env); err != nil {
		return "", "", err
	}
	name := piVendors[vendor].key
	if key, err = secretValue(env, name); err != nil {
		return "", "", &agentPrerequisiteError{summary: fmt.Sprintf("The %s credential for the Mitos broker cannot be read: %v", vendor, err)}
	}
	if key != "" {
		return vendor, key, nil
	}
	var stored map[string]struct {
		Type string `json:"type"`
		Key  string `json:"key"`
	}
	if raw, readErr := os.ReadFile(filepath.Join(piConfigDir(env), "auth.json")); readErr == nil && json.Unmarshal(raw, &stored) == nil {
		// Pi resolves "$NAME" and "!command" keys itself; the broker never
		// runs a command, so only a literal key qualifies.
		if login := stored[vendor]; login.Type == "api_key" && login.Key != "" && !strings.ContainsAny(login.Key[:1], "$!") {
			return vendor, login.Key, nil
		}
	}
	return "", "", &agentPrerequisiteError{summary: fmt.Sprintf("Agents under Nucleus reach the %s API only through the Mitos credential broker, which needs %s (or the same name with _FILE) in the server environment or an API-key login stored by Pi. Subscription logins are not brokered.", vendor, name)}
}

// piSandboxConfig writes Pi's configuration for the sandbox: the operator's
// default provider, model and thinking level, and that provider's model
// definitions pointed at the broker with a placeholder key. Keys, headers and
// every other provider stay on the host.
func piSandboxConfig(server []string, vendor, config string) error {
	host := piConfigDir(server)
	var settings map[string]any
	if raw, err := os.ReadFile(filepath.Join(host, "settings.json")); err == nil {
		_ = json.Unmarshal(raw, &settings)
	}
	kept := map[string]any{}
	for _, name := range []string{"defaultProvider", "defaultModel", "defaultThinkingLevel"} {
		if value, ok := settings[name]; ok {
			kept[name] = value
		}
	}
	provider := map[string]any{}
	var models struct {
		Providers map[string]map[string]any `json:"providers"`
	}
	if raw, err := os.ReadFile(filepath.Join(host, "models.json")); err == nil && json.Unmarshal(raw, &models) == nil && models.Providers[vendor] != nil {
		provider = models.Providers[vendor]
	}
	for _, name := range []string{"apiKey", "headers", "authHeader"} {
		delete(provider, name)
	}
	if list, ok := provider["models"].([]any); ok {
		for _, model := range list {
			if entry, ok := model.(map[string]any); ok {
				delete(entry, "headers")
			}
		}
	}
	provider["baseUrl"] = compute.BrokerBaseURL(vendor) + piVendors[vendor].path
	provider["apiKey"] = compute.BrokerPlaceholderKey
	for name, value := range map[string]any{"settings.json": kept, "models.json": map[string]any{"providers": map[string]any{vendor: provider}}} {
		raw, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(config, name), raw, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func secretValue(env []string, name string) (string, error) {
	path := envValue(env, name+"_FILE")
	value := envValue(env, name)
	if path == "" && strings.HasPrefix(value, "/") {
		path = value
	}
	if path == "" {
		return value, nil
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s_FILE must be an absolute path", name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func envValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		if v, ok := strings.CutPrefix(entry, key+"="); ok {
			value = v
		}
	}
	return value
}

// nucleusAgentEnvironment builds the harness environment and its native
// configuration directory in the attempt home. The harness is configured
// with the broker's loopback base URL and a placeholder key. A resumed native
// conversation is seeded from the previous attempt's home, which held only
// that attempt's own state.
func nucleusAgentEnvironment(runtime, vendor, base, priorBase string, resume bool, server []string) ([]string, error) {
	rt := nucleusAgentRuntimes[runtime]
	home := filepath.Join(base, "home")
	config := filepath.Join(home, rt.configDir)
	if resume {
		previous := filepath.Join(priorBase, "home", rt.configDir)
		if info, err := os.Stat(previous); err != nil || !info.IsDir() {
			return nil, &agentPrerequisiteError{summary: "The previous attempt's native conversation is not available to an agent under Nucleus. Start the goal again to continue under the current execution provider."}
		}
		if err := copyAttemptState(previous, config); err != nil {
			return nil, fmt.Errorf("seed the native conversation: %w", err)
		}
	}
	if err := os.MkdirAll(config, 0o700); err != nil {
		return nil, err
	}
	env := []string{"HOME=" + home, "TMPDIR=" + filepath.Join(base, "tmp"), rt.configEnv + "=" + config}
	for _, name := range nucleusAgentPassEnv {
		if value := envValue(server, name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	switch runtime {
	case "codex":
		auth, _ := json.Marshal(map[string]string{"auth_mode": "apikey", "OPENAI_API_KEY": compute.BrokerPlaceholderKey})
		if err := os.WriteFile(filepath.Join(config, "auth.json"), auth, 0o600); err != nil {
			return nil, err
		}
	case "claude":
		env = append(env, "ANTHROPIC_API_KEY="+compute.BrokerPlaceholderKey, "ANTHROPIC_BASE_URL="+compute.BrokerBaseURL(compute.BrokerVendorAnthropic),
			// Nothing but the model API is reachable; skip the rest instead
			// of waiting on it.
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	case "pi":
		if err := piSandboxConfig(server, vendor, config); err != nil {
			return nil, err
		}
		env = append(env, "PI_OFFLINE=1", "PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0")
	}
	return env, nil
}

// nucleusCodexConfig points Codex at the broker. Codex ignores
// OPENAI_BASE_URL; the configuration key is what it honours.
func nucleusCodexConfig() string {
	return "openai_base_url=" + strconv.Quote(compute.BrokerBaseURL(compute.BrokerVendorOpenAI)+"/v1")
}

// nucleusBrokerSocket is a short per-attempt socket path: unix socket paths
// are limited to 107 bytes.
func nucleusBrokerSocket(storeDir, runID string) string {
	sum := sha256.Sum256([]byte(runID))
	return filepath.Join(storeDir, "brokers", hex.EncodeToString(sum[:8])+".sock")
}

// copyAttemptState copies a previous attempt's native state directory. It
// holds only what that attempt's harness wrote, so nothing is filtered except
// special files, which a configuration directory never needs.
func copyAttemptState(source, target string) error {
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(target, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, body, 0o600)
	})
}
