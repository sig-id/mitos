package agentcli

import (
	"errors"
	"fmt"
	"strings"
)

type CLI struct{ Spec Definition }

func (c CLI) Definition() Definition { return c.Spec }
func (c CLI) Capabilities() Capabilities {
	return Capabilities{Transport: "pty", InitialPrompt: c.Spec.Prompt != "none"}
}
func (c CLI) Resources(workspace string, env map[string]string) []Resource {
	return resources(c.Spec.ID, workspace, env)
}
func (c CLI) Arguments(prompt string) ([]string, error) {
	if len(prompt) > 32<<10 || strings.ContainsRune(prompt, 0) {
		return nil, errors.New("prompt must contain at most 32 KiB and no NUL bytes")
	}
	args := append([]string{}, c.Spec.Args...)
	if prompt == "" {
		return args, nil
	}
	switch c.Spec.Prompt {
	case "none":
		return nil, fmt.Errorf("%s does not support an initial prompt through this adapter; open it and use its terminal", c.Spec.Name)
	case "positional":
		return append(args, "--", prompt), nil
	default:
		return append(args, c.Spec.Prompt, prompt), nil
	}
}

func Builtins() []Adapter {
	definitions := []Definition{
		{ID: "codex", Name: "Codex", Kind: "agent", Executables: []string{"codex"}, Prompt: "positional", EnvNames: []string{"OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "OPENAI_BASE_URL", "CODEX_HOME"}, Docs: "https://developers.openai.com/codex/cli/reference/"},
		{ID: "claude", Name: "Claude Code", Kind: "agent", Executables: []string{"claude"}, Prompt: "positional", EnvNames: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CONFIG_DIR", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "AWS_PROFILE", "GOOGLE_APPLICATION_CREDENTIALS"}, Docs: "https://code.claude.com/docs/en/cli-reference"},
		{ID: "opencode", Name: "OpenCode", Kind: "agent", Executables: []string{"opencode"}, Prompt: "--prompt", EnvNames: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR"}, Docs: "https://opencode.ai/docs/cli/"},
		{ID: "pi", Name: "Pi", Kind: "agent", Executables: []string{"pi"}, Prompt: "positional", EnvNames: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR"}, Docs: "https://github.com/earendil-works/pi/tree/main/packages/coding-agent"},
		{ID: "gemini", Name: "Gemini CLI", Kind: "agent", Executables: []string{"gemini"}, Prompt: "--prompt-interactive", EnvNames: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT"}, Docs: "https://geminicli.com/docs/cli/cli-reference/"},
		{ID: "copilot", Name: "GitHub Copilot", Kind: "agent", Executables: []string{"copilot"}, Prompt: "--interactive", EnvNames: []string{"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}, Docs: "https://docs.github.com/en/copilot/reference/cli-command-reference"},
		{ID: "aider", Name: "Aider", Kind: "agent", Executables: []string{"aider"}, Prompt: "none", EnvNames: []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "AIDER_MODEL", "AIDER_CONFIG"}, Docs: "https://aider.chat/docs/config/options.html"},
		{ID: "goose", Name: "Goose", Kind: "agent", Executables: []string{"goose"}, Args: []string{"session"}, Prompt: "none", EnvNames: []string{"GOOSE_PROVIDER", "GOOSE_MODEL", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"}, Docs: "https://block.github.io/goose/"},
		{ID: "amp", Name: "Amp", Kind: "agent", Executables: []string{"amp"}, Prompt: "none", EnvNames: []string{"AMP_API_KEY", "AMP_URL"}, Docs: "https://ampcode.com/docs"},
		{ID: "cursor", Name: "Cursor Agent", Kind: "agent", Executables: []string{"cursor-agent", "agent"}, Prompt: "positional", EnvNames: []string{"CURSOR_API_KEY"}, Docs: "https://cursor.com/docs/cli/reference/parameters"},
		{ID: "qwen", Name: "Qwen Code", Kind: "agent", Executables: []string{"qwen"}, Prompt: "none", EnvNames: []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL"}, Docs: "https://qwenlm.github.io/qwen-code-docs/"},
		{ID: "cline", Name: "Cline", Kind: "agent", Executables: []string{"cline"}, Prompt: "none", Docs: "https://docs.cline.bot/cline-cli/overview"},
		{ID: "kilo", Name: "Kilo Code", Kind: "agent", Executables: []string{"kilo"}, Prompt: "none", Docs: "https://kilo.ai/docs/cli"},
		{ID: "shell", Name: "Shell", Kind: "shell", Executables: []string{"bash"}, Args: []string{"--noprofile", "--norc", "-i"}, Prompt: "none"},
	}
	adapters := make([]Adapter, 0, len(definitions))
	for _, definition := range definitions {
		definition.VersionArgs = []string{"--version"}
		adapters = append(adapters, CLI{definition})
	}
	return adapters
}
