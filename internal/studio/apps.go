package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// Application is the small, local manifest dialect implemented by this build.
// Published OCI manifests and agentic orchestration remain separate integrations.
type Application struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Entrypoint  string   `json:"entrypoint"`
	Command     []string `json:"command,omitempty"`
	Acceptance  []string `json:"acceptance,omitempty"`
	Directory   string   `json:"-"`
	Builtin     bool     `json:"-"`
}

func LoadApps(dir string) ([]Application, error) {
	apps := []Application{{ID: "workspace-inventory", Name: "Workspace inventory", Description: "Get a file inventory, a content fingerprint, and a portable report of a local workspace.", Category: "Workspace", Entrypoint: "Inspect workspace", Builtin: true}}
	if dir == "" {
		return apps, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{"workspace-inventory": true}
	pattern := regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(abs, entry.Name(), "app.json")
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var app Application
		if err = json.Unmarshal(b, &app); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if !pattern.MatchString(app.ID) || seen[app.ID] || app.Name == "" || app.Entrypoint == "" || len(app.Command) == 0 || len(app.Acceptance) == 0 {
			return nil, fmt.Errorf("%s needs a unique id, name, entrypoint, command, and acceptance command", path)
		}
		if !filepath.IsAbs(app.Command[0]) || !filepath.IsAbs(app.Acceptance[0]) {
			return nil, fmt.Errorf("%s: command and acceptance executables must use absolute paths", path)
		}
		app.Directory = filepath.Dir(path)
		seen[app.ID] = true
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}
