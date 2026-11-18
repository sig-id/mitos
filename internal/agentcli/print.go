package agentcli

import (
	"errors"
	"fmt"
	"strings"
)

// PrintArguments selects the native, foreground non-interactive interface.
// The potentially large assignment is piped on stdin, never interpolated into
// a command or exposed in the process list.
func PrintArguments(id, model, effort string, granted func(string) bool) ([]string, error) {
	var args []string
	switch id {
	case "opencode", "kilo":
		args = []string{"run"}
	case "pi":
		args = []string{"--print", "--no-session", "--no-extensions", "--no-mcp"}
		var tools []string
		if granted("workspace.read") {
			tools = append(tools, "read", "grep", "find", "ls")
		}
		if granted("workspace.write") {
			tools = append(tools, "edit", "write")
		}
		if granted("command.run") {
			tools = append(tools, "bash")
		}
		if len(tools) == 0 {
			args = append(args, "--no-tools")
		} else {
			args = append(args, "--tools", strings.Join(tools, ","))
		}
	case "gemini", "qwen":
		args = []string{"--output-format", "text"}
		if granted("workspace.write") {
			mode := "auto_edit"
			if id == "qwen" {
				mode = "auto-edit"
			}
			args = append(args, "--approval-mode", mode)
		}
	case "copilot":
		args = []string{"-s", "--no-ask-user", "--excluded-tools=task"}
		if granted("workspace.write") {
			args = append(args, "--allow-tool=write")
		}
	case "goose":
		args = []string{"run", "--no-session", "-i", "-"}
	case "amp":
		args = []string{"--execute"}
	case "cursor":
		args = []string{"--print", "--output-format", "text"}
		if !granted("workspace.write") {
			args = append(args, "--mode", "ask")
		}
	case "cline":
		args = []string{"--acp", "--auto-approve", "false"}
	case "aider":
		args = []string{"--message-file", "/dev/stdin", "--no-show-release-notes", "--no-pretty", "--no-stream", "--no-auto-commits", "--no-dirty-commits", "--no-gitignore", "--no-check-update", "--chat-history-file", "/dev/null", "--input-history-file", "/dev/null"}
		if !granted("workspace.write") {
			args = append(args, "--chat-mode", "ask")
		}
	default:
		return nil, errors.New("no print adapter for " + id)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		flag := ""
		switch id {
		case "opencode", "kilo":
			flag = "--variant"
		case "pi", "cline":
			flag = "--thinking"
		case "amp":
			flag = "--effort"
		default:
			return nil, fmt.Errorf("%s cannot select reasoning effort", id)
		}
		args = append(args, flag, effort)
	}
	return args, nil
}
