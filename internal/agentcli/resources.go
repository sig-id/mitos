package agentcli

import (
	"os"
	"path/filepath"
	"sort"
)

// Inspect metadata only. A file's presence is evidence of local setup, not proof
// of valid credentials, enabled skills, effective configuration, or memory use.
func resources(id, workspace string, env map[string]string) []Resource {
	result := []Resource{}
	add := func(kind, scope, path string) {
		if path == "" || !filepath.IsAbs(path) {
			return
		}
		_, err := os.Stat(path)
		status := "present"
		if os.IsNotExist(err) {
			status = "absent"
		} else if err != nil {
			status = "unreadable"
		}
		result = append(result, Resource{Kind: kind, Scope: scope, Path: path, Present: err == nil, Status: status})
	}
	fallback := func(key, path string) string {
		if env[key] != "" {
			return env[key]
		}
		return path
	}
	home := env["HOME"]
	user := func(parts ...string) string {
		if home == "" {
			return ""
		}
		return filepath.Join(append([]string{home}, parts...)...)
	}
	project := func(kind, path string) {
		if workspace != "" {
			add(kind, "project", filepath.Join(workspace, path))
		}
	}
	config := fallback("XDG_CONFIG_HOME", user(".config"))
	data := fallback("XDG_DATA_HOME", user(".local", "share"))
	switch id {
	case "codex":
		root := fallback("CODEX_HOME", user(".codex"))
		for kind, paths := range map[string][]string{"config": {"config.toml"}, "auth": {"auth.json"}, "skills": {"skills"}, "memory": {"AGENTS.md", "memories"}, "history": {"sessions"}} {
			for _, path := range paths {
				add(kind, "user", filepath.Join(root, path))
			}
		}
		add("skills", "user", user(".agents", "skills"))
		project("config", ".codex/config.toml")
		project("skills", ".agents/skills")
		project("memory", "AGENTS.md")
	case "claude":
		root := fallback("CLAUDE_CONFIG_DIR", user(".claude"))
		for kind, paths := range map[string][]string{"config": {"settings.json"}, "auth": {".credentials.json"}, "skills": {"skills"}, "memory": {"CLAUDE.md"}, "history": {"projects"}} {
			for _, path := range paths {
				add(kind, "user", filepath.Join(root, path))
			}
		}
		project("config", ".claude/settings.json")
		project("config", ".claude/settings.local.json")
		project("skills", ".claude/skills")
		project("memory", "CLAUDE.md")
	case "opencode":
		root := fallback("OPENCODE_CONFIG_DIR", filepath.Join(config, "opencode"))
		add("config", "user", fallback("OPENCODE_CONFIG", filepath.Join(root, "opencode.json")))
		add("config", "user", filepath.Join(root, "opencode.jsonc"))
		add("auth", "user", filepath.Join(data, "opencode", "auth.json"))
		add("skills", "user", filepath.Join(root, "skills"))
		add("memory", "user", filepath.Join(root, "AGENTS.md"))
		project("config", "opencode.json")
		project("config", "opencode.jsonc")
		project("skills", ".opencode/skills")
		project("memory", "AGENTS.md")
	case "pi":
		root := fallback("PI_CODING_AGENT_DIR", user(".pi", "agent"))
		for kind, paths := range map[string][]string{"config": {"settings.json", "models.json"}, "auth": {"auth.json"}, "skills": {"skills"}, "extensions": {"extensions"}, "memory": {"AGENTS.md"}} {
			for _, path := range paths {
				add(kind, "user", filepath.Join(root, path))
			}
		}
		add("history", "user", fallback("PI_CODING_AGENT_SESSION_DIR", filepath.Join(root, "sessions")))
		project("config", ".pi/settings.json")
		project("skills", ".pi/skills")
		project("extensions", ".pi/extensions")
		project("memory", "AGENTS.md")
	case "aider":
		add("config", "user", fallback("AIDER_CONFIG", user(".aider.conf.yml")))
		project("config", ".aider.conf.yml")
		project("history", ".aider.chat.history.md")
	case "gemini":
		add("config", "user", user(".gemini", "settings.json"))
		add("auth", "user", user(".gemini", "oauth_creds.json"))
		add("memory", "user", user(".gemini", "GEMINI.md"))
		project("config", ".gemini/settings.json")
		project("memory", "GEMINI.md")
	case "copilot":
		add("config", "user", user(".copilot", "config.json"))
		add("skills", "user", user(".copilot", "skills"))
		add("history", "user", user(".copilot", "session-state"))
		project("memory", ".github/copilot-instructions.md")
		project("skills", ".github/skills")
	case "goose":
		add("config", "user", filepath.Join(config, "goose", "config.yaml"))
		project("memory", ".goosehints")
	case "amp":
		add("config", "user", filepath.Join(config, "amp", "settings.json"))
		project("memory", "AGENTS.md")
	case "cursor":
		add("config", "user", user(".cursor", "cli-config.json"))
		project("memory", ".cursor/rules")
	case "qwen":
		add("config", "user", user(".qwen", "settings.json"))
		project("memory", "QWEN.md")
	}
	sort.Slice(result, func(i, j int) bool {
		a, b := result[i], result[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Path < b.Path
	})
	return result
}
