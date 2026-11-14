package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"mitos/internal/acceptance"
	"mitos/internal/autonomy"
	"mitos/internal/compute"
	"mitos/internal/decisionmodel"
	"mitos/internal/observation"
)

// Application is the small, local manifest dialect implemented by this build.
// Published OCI manifests and agentic orchestration remain separate integrations.
type Application struct {
	Agent             *AgentBinding         `json:"agent,omitempty"`
	GraphOrchestrator bool                  `json:"graph_orchestrator,omitempty"`
	DecisionModel     *decisionmodel.Config `json:"decision_model,omitempty"`
	Compute           *compute.Request      `json:"compute,omitempty"`
	Supervision       *autonomy.Supervision `json:"supervision,omitempty"`
	ID                string                `json:"id"`
	Name              string                `json:"name"`
	Description       string                `json:"description"`
	Category          string                `json:"category"`
	Entrypoint        string                `json:"entrypoint"`
	Command           []string              `json:"command,omitempty"`
	Acceptance        acceptance.Spec       `json:"acceptance,omitempty"`
	Directory         string                `json:"-"`
	Builtin           bool                  `json:"-"`
	Observation       *observation.Config   `json:"observation,omitempty"`
}

func LoadApps(dir string) ([]Application, error) {
	apps := []Application{{ID: "workspace-inventory", Name: "Workspace inventory", Description: "Get a file inventory, a content fingerprint, and a portable report of a local workspace.", Category: "Workspace", Entrypoint: "Inspect workspace", Builtin: true}}
	apps = append(apps, Application{ID: WorkflowAppID, Name: "Recurring agent workflow", Description: "Execute saved instructions with agents on a schedule or external event.", Category: "Automation", Entrypoint: "Run workflow", Builtin: true})
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
	seen := map[string]bool{"workspace-inventory": true, WorkflowAppID: true}
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
		if !pattern.MatchString(app.ID) || seen[app.ID] || app.Name == "" || app.Entrypoint == "" || len(app.Command) == 0 || len(app.Acceptance.Cmd) == 0 {
			return nil, fmt.Errorf("%s needs a unique id, name, entrypoint, command, and acceptance command", path)
		}
		if !filepath.IsAbs(app.Command[0]) || !filepath.IsAbs(app.Acceptance.Cmd[0]) {
			return nil, fmt.Errorf("%s: command and acceptance executables must use absolute paths", path)
		}
		if err := app.Acceptance.Validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if app.Supervision != nil {
			if err := app.Supervision.Validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		if app.Observation != nil {
			if err := app.Observation.Validate(); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
		}
		if err := validateDecisionModel(app); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		app.Directory = filepath.Dir(path)
		seen[app.ID] = true
		apps = append(apps, app)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

func validateDecisionModel(app Application) error {
	if app.DecisionModel == nil {
		return nil
	}
	if app.Builtin {
		return errors.New("decision_model is only supported for command applications")
	}
	if err := app.DecisionModel.Validate(); err != nil {
		return err
	}
	if app.DecisionModel.HasHook("before_recovery") && app.Supervision == nil {
		return errors.New("decision_model before_recovery requires supervision")
	}
	return nil
}
