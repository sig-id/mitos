package studio

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"mitos/agents"
)

func TestAgentLibraryRevisionsAndReplay(t *testing.T) {
	e, _ := graphEngine(t, false)
	initial, err := e.AgentLibrary()
	if err != nil || len(initial) != 8 {
		t.Fatalf("library: %v %d", err, len(initial))
	}
	request := AgentEdit{Key: "new-specialist", Name: "Accessibility reviewer", Description: "Review keyboard navigation", Instructions: "Inspect focus order and report evidence."}
	first, err := e.SaveAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := e.SaveAgent(request)
	if err != nil || replay.ID != first.ID {
		t.Fatal("save replay failed", err)
	}
	request.ID = first.ID
	request.Revision = first.Revision
	request.Key = "edit-specialist"
	request.Instructions = "Check focus order and accessible names."
	second, err := e.SaveAgent(request)
	if err != nil || second.Revision != 2 {
		t.Fatal("revision failed", err)
	}
	request.Key = "stale-edit"
	if _, err = e.SaveAgent(request); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	library, err := e.AgentLibrary()
	if err != nil || len(library) != 9 {
		t.Fatal("library persisted", err)
	}
	saved, err := e.readLibrary()
	if err != nil || len(saved.Revisions[first.ID]) != 2 {
		t.Fatal("history not preserved", err)
	}
	if first.Instructions == second.Instructions || first.Digest == second.Digest {
		t.Fatal("definition identity did not change")
	}
	for _, d := range initial {
		if isOrchestratorKind(d.Kind) {
			request.ID = d.ID
			request.Revision = d.Revision
			request.Key = "edit-orchestrator"
			if _, err = e.SaveAgent(request); err == nil {
				t.Fatal("orchestrator authority editable")
			}
		}
	}
}

func TestEditingBundledAgentPreservesManifest(t *testing.T) {
	e, _ := graphEngine(t, false)
	library, err := e.AgentLibrary()
	if err != nil {
		t.Fatal(err)
	}
	var base agents.Definition
	for _, d := range library {
		if d.ID == "mitos.researcher" {
			base = d
		}
	}
	edited, err := e.SaveAgent(AgentEdit{Key: "edit-researcher", ID: base.ID, Revision: base.Revision, Name: base.Name, Description: base.Description, Instructions: "Investigate primary sources first."})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Revision != base.Revision+1 || edited.Manifest.Revision != edited.Revision || edited.Digest == base.Digest || edited.Verify() != nil {
		t.Fatalf("edited identity inconsistent: %+v", edited)
	}
	m, b := edited.Manifest, base.Manifest
	if !slices.Equal(m.Capabilities.Required, b.Capabilities.Required) || !slices.Equal(m.Runtime.Requires, b.Runtime.Requires) || m.Limits != b.Limits || m.Context.MaxCharacters != b.Context.MaxCharacters || m.Provenance.SourceRevision != b.Provenance.SourceRevision || m.Instructions.SHA256 == b.Instructions.SHA256 {
		t.Fatalf("edit lost manifest content: %+v", m)
	}
	reloaded, err := e.AgentLibrary()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range reloaded {
		if d.ID == base.ID && (d.Digest != edited.Digest || d.Manifest.Limits != b.Limits) {
			t.Fatal("reloaded revision differs from saved edit")
		}
	}
	saved, err := e.readLibrary()
	if err != nil || len(saved.Revisions[base.ID]) != 1 {
		t.Fatal("edit history not retained", err)
	}

	created, err := e.SaveAgent(AgentEdit{Key: "new-agent", Name: "Data auditor", Description: "Audit data quality", Instructions: "Check schemas and report anomalies."})
	if err != nil {
		t.Fatal(err)
	}
	if p := created.Manifest.Provenance; p.Origin != "original" || p.Base == nil || p.Base.ID != "mitos.generalist" || created.Manifest.Kind != "worker" || created.Verify() != nil {
		t.Fatalf("new agent not derived from the generalist: %+v", created.Manifest)
	}
}

func TestAgentLibraryRejectsUnversionedOrTamperedStore(t *testing.T) {
	e, _ := graphEngine(t, false)
	if _, err := e.SaveAgent(AgentEdit{Key: "seed", Name: "Seed agent", Description: "Seed", Instructions: "Seed instructions."}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(e.Store.Dir(), "agent-library.json")
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"unversioned": strings.Replace(string(raw), `"profile": "agent-library-store/v1",`, "", 1),
		"tampered":    strings.Replace(string(raw), "Seed instructions.", "Other instructions.", 1),
	} {
		if err = os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = e.AgentLibrary(); err == nil {
			t.Errorf("%s store accepted", name)
		}
	}
}

func TestSpecializationRespectsDefinitionBounds(t *testing.T) {
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range library {
		if d.ID != "mitos.engineer" {
			continue
		}
		b := &AgentBinding{Definition: d}
		limit := d.Manifest.Specialization.MaxInstructionBytes
		if err = specializeAgent(b, &AgentSelection{Role: "Parser engineer", Instructions: strings.Repeat("x", limit)}, "g"); err != nil {
			t.Fatal(err)
		}
		if err = specializeAgent(b, &AgentSelection{Role: "Parser engineer", Instructions: strings.Repeat("x", limit+1)}, "g"); err == nil {
			t.Fatal("oversized specialization accepted")
		}
		d.Manifest.Specialization.Allow = []string{"name"}
		if err = specializeAgent(&AgentBinding{Definition: d}, &AgentSelection{Role: "Parser engineer", Instructions: "extra"}, "g"); err == nil {
			t.Fatal("disallowed instruction specialization accepted")
		}
	}
}
