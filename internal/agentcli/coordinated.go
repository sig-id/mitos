package agentcli

import "slices"

// CoordinatedRuntimes is ordered by the default runtime preference.
func CoordinatedRuntimes() []string {
	return []string{"codex", "claude", "opencode", "pi", "gemini", "copilot", "aider", "goose", "amp", "cursor", "qwen", "cline", "kilo", "antigravity"}
}

func SupportsCoordination(id string) bool { return slices.Contains(CoordinatedRuntimes(), id) }
func PrintRuntime(id string) bool         { return id != "codex" && id != "claude" && SupportsCoordination(id) }

func ReasoningEfforts(id string) []string {
	switch id {
	case "codex", "pi":
		return []string{"minimal", "low", "medium", "high", "xhigh"}
	case "claude", "amp", "antigravity":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "cline":
		return []string{"low", "medium", "high", "xhigh"}
	case "opencode", "kilo":
		return []string{"low", "medium", "high"}
	}
	return nil
}
