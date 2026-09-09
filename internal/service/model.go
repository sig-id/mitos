// Package service resolves declarative service activation independently of execution.
package service

import (
	"errors"
	"fmt"
	"regexp"
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,95}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }

type Relations struct {
	Requires      []string `json:"requires,omitempty"`
	Wants         []string `json:"wants,omitempty"`
	ConflictsWith []string `json:"conflicts_with,omitempty"`
	Before        []string `json:"before,omitempty"`
	After         []string `json:"after,omitempty"`
	PartOf        []string `json:"part_of,omitempty"`
}
type Unit struct {
	DependsOn      []string      `json:"depends_on,omitempty"`
	Condition      *Condition    `json:"condition,omitempty"`
	Health         *Health       `json:"health,omitempty"`
	Triggers       *Triggers     `json:"triggers,omitempty"`
	RestartPolicy  RestartPolicy `json:"restart_policy,omitempty"`
	CircuitBreaker BreakerConfig `json:"circuit_breaker,omitempty"`
	ID             string        `json:"id"`
	Description    string        `json:"description,omitempty"`
	Scope          string        `json:"scope"`
	Trigger        string        `json:"trigger"`
	Schedule       *Schedule     `json:"schedule,omitempty"`
	Concurrency    Concurrency   `json:"concurrency,omitempty"`
	Relations
}
type Target struct {
	ID          string `json:"id"`
	Description string `json:"description,omitempty"`
	Scope       string `json:"scope"`
	Relations
}
type Preset struct {
	ProjectType string   `json:"project_type"`
	Enable      []string `json:"enable"`
	Disable     []string `json:"disable"`
}
type Intent struct {
	ProjectType string   `json:"project_type,omitempty"`
	Targets     []string `json:"targets,omitempty"`
	Enable      []string `json:"enable,omitempty"`
	Disable     []string `json:"disable,omitempty"`
}
type Config struct {
	Units   []Unit   `json:"units"`
	Targets []Target `json:"targets"`
	Presets []Preset `json:"presets"`
	Intent  Intent   `json:"intent"`
}
type Why struct {
	Source   string `json:"source"`
	Relation string `json:"relation"`
	Strength string `json:"strength"`
	Root     string `json:"root"`
}
type Row struct {
	IngressSource string   `json:"ingress_source,omitempty"`
	DependsOn     []string `json:"depends_on,omitempty"`
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	State         string   `json:"state"`
	Trigger       string   `json:"trigger,omitempty"`
	Why           []Why    `json:"why"`
	PartOf        []string `json:"part_of,omitempty"`
}
type Issue struct {
	ID      string `json:"id"`
	Related string `json:"related,omitempty"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail,omitempty"`
}
type Resolution struct {
	Rows   []Row    `json:"rows"`
	Order  []string `json:"order"`
	Issues []Issue  `json:"issues"`
}

func refs(values []string) error {
	if len(values) > 128 {
		return errors.New("at most 128 relation references")
	}
	seen := map[string]bool{}
	for _, id := range values {
		if !ValidID(id) || seen[id] {
			return errors.New("references require unique bounded identifiers")
		}
		seen[id] = true
	}
	return nil
}
func (c Config) Validate() error {
	if len(c.Units)+len(c.Targets) > 256 || len(c.Presets) > 32 {
		return errors.New("catalog limit is 256 units/targets and 32 presets")
	}
	seen := map[string]bool{}
	check := func(id, scope, description string, r Relations) error {
		if !ValidID(id) || seen[id] {
			return fmt.Errorf("invalid or duplicate service/target %q", id)
		}
		seen[id] = true
		if scope != "project" {
			return errors.New("the local service catalog requires project scope")
		}
		if len(description) > 2000 {
			return errors.New("service description exceeds 2000 characters")
		}
		for _, values := range [][]string{r.Requires, r.Wants, r.ConflictsWith, r.Before, r.After, r.PartOf} {
			if err := refs(values); err != nil {
				return err
			}
		}
		return nil
	}
	for _, u := range c.Units {
		if err := u.ValidateRuntime(); err != nil {
			return err
		}
		if err := check(u.ID, u.Scope, u.Description, u.Relations); err != nil {
			return err
		}
	}
	for _, t := range c.Targets {
		if err := check(t.ID, t.Scope, t.Description, t.Relations); err != nil {
			return err
		}
	}
	if _, err := DependencyOrder(c.Units); err != nil {
		return err
	}
	presets := map[string]bool{}
	for _, p := range c.Presets {
		if !ValidID(p.ProjectType) || presets[p.ProjectType] {
			return errors.New("invalid or duplicate project preset")
		}
		presets[p.ProjectType] = true
		for _, r := range [][]string{p.Enable, p.Disable} {
			if err := refs(r); err != nil {
				return err
			}
		}
	}
	for _, r := range [][]string{c.Intent.Targets, c.Intent.Enable, c.Intent.Disable} {
		if err := refs(r); err != nil {
			return err
		}
	}
	if c.Intent.ProjectType != "" && !presets[c.Intent.ProjectType] {
		return errors.New("unknown project preset")
	}
	for _, id := range append(append([]string{}, c.Intent.Enable...), c.Intent.Disable...) {
		if !seen[id] {
			return fmt.Errorf("unknown explicit service/target %s", id)
		}
	}
	for _, id := range c.Intent.Targets {
		found := false
		for _, t := range c.Targets {
			if t.ID == id {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("unknown activation target %s", id)
		}
	}
	return nil
}
