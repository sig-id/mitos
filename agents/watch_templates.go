package agents

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"

	"mitos/internal/watch"
)

// Default watch templates for adaptive orchestrators (spec 09, gate W4).
// A template is the exact input the granted watch_create tool takes: the
// creator fills the owner id from its own responsibility, the runtime clamps
// the scope to the creator's graph, and the creator renews the lease while it
// still wants the watch. Nothing here grants anything by itself.

//go:embed watch-templates
var watchTemplateFS embed.FS

// WatchCreateInput mirrors the watch_create tool input (owner, condition,
// grouping, lease_ms, default_level, optional triage). It is the authoring
// shape shared by templates and docs; the studio tool has the same fields.
type WatchCreateInput struct {
	Owner        string          `json:"owner"`
	Description  string          `json:"description"`
	Condition    watch.Condition `json:"condition"`
	Grouping     watch.Grouping  `json:"grouping"`
	LeaseMS      int64           `json:"lease_ms"`
	MaxRenewals  int             `json:"max_renewals"`
	MaxSilenceMS int64           `json:"max_silence_ms,omitempty"`
	DefaultLevel watch.Level     `json:"default_level"`
	Triage       *watch.Triage   `json:"triage,omitempty"`
}

// WatchTemplate is one named, ready-to-create watch.
type WatchTemplate struct {
	Template  string           `json:"template"` // also the file name
	Rationale string           `json:"rationale"`
	Create    WatchCreateInput `json:"create"`
}

// WatchTemplates loads and validates the embedded default templates, ordered
// by name.
func WatchTemplates() ([]WatchTemplate, error) {
	entries, err := fs.Glob(watchTemplateFS, "watch-templates/*.json")
	if err != nil {
		return nil, err
	}
	slices.Sort(entries)
	out := make([]WatchTemplate, 0, len(entries))
	for _, path := range entries {
		raw, err := watchTemplateFS.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(raw) > 8<<10 {
			return nil, fmt.Errorf("watch template %s is too large", path)
		}
		var t WatchTemplate
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("watch template %s: %w", path, err)
		}
		if err := t.validate(); err != nil {
			return nil, fmt.Errorf("watch template %s: %w", path, err)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, errors.New("no watch templates")
	}
	return out, nil
}

// validate checks what a template can check without a graph or a clock: the
// create input is well formed for a responsibility owner, the lease is
// bounded, and the triage instructions are within bounds. The studio side
// validates the clamped spec again at creation.
func (t WatchTemplate) validate() error {
	if t.Template == "" || len(t.Template) > 64 || t.Rationale == "" || len(t.Rationale) > maxText {
		return errors.New("template needs an id and a 1–4000 byte rationale")
	}
	in := t.Create
	if in.Owner != watch.OwnerResponsibility {
		return errors.New("default templates are responsibility-owned; the orchestrator survives replacement")
	}
	if in.Description == "" || len(in.Description) > 200 {
		return errors.New("description must be 1–200 bytes")
	}
	// The scope is left to the creator's grant: it must stay empty here and
	// be clamped to the creator's graph at creation.
	if in.Condition.Scope != (watch.Scope{}) {
		return errors.New("a template leaves its scope to the creator's graph")
	}
	if in.LeaseMS <= 0 || in.LeaseMS > int64(watch.MaxLease.Milliseconds()) {
		return errors.New("lease_ms must be within 24 hours")
	}
	if in.MaxRenewals < 1 || in.MaxRenewals > 1024 {
		return errors.New("max_renewals must be 1–1024")
	}
	if in.MaxSilenceMS != 0 && (in.MaxSilenceMS < 60000 || in.MaxSilenceMS > 86400000) {
		return errors.New("max_silence_ms must be 0 or 60000–86400000")
	}
	if !in.DefaultLevel.Valid() {
		return errors.New("default_level must be digest, notify, wake or escalate")
	}
	// Validate condition and grouping through the spec's own rules, with the
	// scope a live creation clamps in. The studio side validates the clamped
	// spec again at creation.
	condition := in.Condition
	condition.Scope.Graph = "graph_template"
	if err := condition.Validate(); err != nil {
		return err
	}
	if err := in.Grouping.Validate(); err != nil {
		return err
	}
	if in.Triage != nil {
		return in.Triage.Validate()
	}
	return nil
}
