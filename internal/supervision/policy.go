// Package supervision checks project boundaries independently of planning and routing.
package supervision

import (
	"encoding/json"
	"mitos/internal/agentpolicy"
	"os"
	"path/filepath"
)

// Check returns a deterministic violation or an empty string. Natural-language
// boundaries belong to the independent model and cannot expand runtime grants.
func Check(p agentpolicy.Policy, workspace string, raw json.RawMessage) string {
	if p.Supervision.Effective().Mode == "off" {
		return ""
	}
	var event struct {
		Method string `json:"method"`
		Hook   string `json:"hook_event_name"`
		Tool   string `json:"tool_name"`
		CWD    string `json:"cwd"`
		Input  struct {
			Command string `json:"command"`
			Path    string `json:"file_path"`
		} `json:"tool_input"`
		Params struct {
			Command    string          `json:"command"`
			CWD        string          `json:"cwd"`
			Additional json.RawMessage `json:"additionalPermissions"`
			Item       struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				CWD     string `json:"cwd"`
				Changes []struct {
					Path string `json:"path"`
				} `json:"changes"`
			} `json:"item"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return "supervision received malformed evidence"
	}
	command, directory := event.Params.Command, event.Params.CWD
	if event.Method == "item/started" || event.Method == "item/completed" {
		command, directory = event.Params.Item.Command, event.Params.Item.CWD
	}
	claudeFile := event.Hook == "PreToolUse" && (event.Tool == "Write" || event.Tool == "Edit")
	if event.Hook == "PreToolUse" {
		directory = event.CWD
		if event.Tool == "Bash" {
			command = event.Input.Command
		}
	}
	if command != "" {
		if rule, ok := p.Match(command, directory, workspace); ok && rule.Action == "deny" {
			return "operation matches denied command rule " + rule.ID
		}
	}
	if !p.Supervision.WorkspaceOnly {
		return ""
	}
	inside := func(path string) bool {
		if !filepath.IsAbs(path) {
			return false
		}
		root, err := resolvedPath(workspace)
		if err != nil {
			return false
		}
		path, err = resolvedPath(path)
		if err != nil {
			return false
		}
		rel, err := filepath.Rel(root, path)
		return err == nil && filepath.IsLocal(rel)
	}
	if directory != "" && !inside(directory) {
		return "action working directory is outside the project workspace boundary"
	}
	if len(event.Params.Additional) > 0 && string(event.Params.Additional) != "null" {
		return "expanded permissions require review of the workspace boundary"
	}
	if claudeFile {
		path := event.Input.Path
		if path == "" {
			return "file action is missing its workspace boundary target"
		}
		if !filepath.IsAbs(path) {
			if directory == "" || !filepath.IsAbs(directory) {
				return "relative file action is missing its absolute working directory"
			}
			path = filepath.Join(directory, path)
		}
		if !inside(path) {
			return "file action is outside the project workspace boundary"
		}
	}
	for _, change := range event.Params.Item.Changes {
		path := change.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		if !inside(path) {
			return "file change is outside the project workspace boundary"
		}
	}
	return ""
}
func Relevant(raw json.RawMessage) bool {
	var e struct {
		Method string `json:"method"`
		Params struct {
			Item struct {
				Type string `json:"type"`
			} `json:"item"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return false
	}
	if e.Method != "item/started" && e.Method != "item/completed" {
		return false
	}
	switch e.Params.Item.Type {
	case "commandExecution", "fileChange", "mcpToolCall":
		return true
	}
	return false
}

// Resolve existing ancestors so a new file through an outside symlink is caught.
func resolvedPath(path string) (string, error) {
	path = filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) || filepath.Dir(path) == path {
		return "", err
	}
	parent, err := resolvedPath(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}
