package studio

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"mitos/agents"
)

func TestAuthorityPoliciesForBundledAgents(t *testing.T) {
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	refused := map[string][]string{
		EnforcementAdvisory:       nil,
		EnforcementStrictRequired: {"mitos.engineer", "mitos.tester"}, // both require workspace.read
	}
	for policy, want := range refused {
		var got []string
		for _, d := range library {
			_, err := resolveAuthority(d, "codex", policy, 0)
			var prerequisite *agentPrerequisiteError
			if err != nil {
				if !errors.As(err, &prerequisite) {
					t.Fatalf("%s/%s: refusal must be a setup prerequisite: %v", policy, d.ID, err)
				}
				got = append(got, d.ID)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s refused %v, want %v", policy, got, want)
		}
	}
	for _, d := range library {
		if _, err := resolveAuthority(d, "codex", EnforcementStrict, 0); err == nil {
			t.Fatalf("strict accepted %s although reads and commands cannot be confined", d.ID)
		}
	}
	if _, err := resolveAuthority(library[0], "codex", "", 0); err == nil {
		t.Fatal("missing policy accepted")
	}
	if _, err := resolveAuthority(library[0], "unsupported", EnforcementAdvisory, 0); err == nil {
		t.Fatal("runtime without a profile accepted")
	}
}

func TestAuthorityReceiptStatesAndLimits(t *testing.T) {
	library, _ := agents.Library()
	var engineer, architect agents.Definition
	for _, d := range library {
		switch d.ID {
		case "mitos.engineer":
			engineer = d
		case "mitos.architect":
			architect = d
		}
	}
	r, err := resolveAuthority(engineer, "codex", EnforcementAdvisory, 600)
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]CapabilityReceipt{}
	for _, c := range r.Capabilities {
		status[c.Name] = c
	}
	if c := status["workspace.write"]; !c.Granted || c.Status != CapabilityEnforced || !c.Required {
		t.Fatalf("workspace.write: %+v", c)
	}
	if c := status["workspace.read"]; c.Status != CapabilityAdvisory {
		t.Fatalf("unconfined reads must be advisory: %+v", c)
	}
	if c := status["graph.propose"]; c.Granted || c.Status != CapabilityEnforced {
		t.Fatalf("withheld kernel capability: %+v", c)
	}
	if r.TimeoutSeconds != 600 || r.MaxTurns != engineer.Manifest.Limits.MaxTurns || r.Digest != engineer.Digest {
		t.Fatalf("limits: %+v", r)
	}
	if r, _ = resolveAuthority(engineer, "codex", EnforcementAdvisory, 0); r.TimeoutSeconds != engineer.Manifest.Limits.TimeoutSeconds {
		t.Fatal("definition timeout not applied")
	}

	// Unsupported required capabilities and features always refuse.
	m := architect.Manifest
	m.Capabilities.Required = []string{"gpu.use"}
	m.Capabilities.Optional = []string{"workspace.read"}
	if _, err = resolveAuthority(agents.Definition{ID: m.ID, Manifest: m}, "codex", EnforcementAdvisory, 0); err == nil {
		t.Fatal("unsupported required capability accepted")
	}
	m = architect.Manifest
	m.Runtime.Requires = []string{"native_output", "vision"}
	if _, err = resolveAuthority(agents.Definition{ID: m.ID, Manifest: m}, "codex", EnforcementAdvisory, 0); err == nil {
		t.Fatal("missing runtime feature accepted")
	}
}

func TestHandoffToolsFollowArtifactGrants(t *testing.T) {
	library, _ := agents.Library()
	for _, d := range library {
		if d.ID != "mitos.orchestrator" {
			continue
		}
		r, err := resolveAuthority(d, "codex", EnforcementAdvisory, 0)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, tool := range grantedHandoffTools(r) {
			names = append(names, tool.Name)
		}
		if slices.Contains(names, "artifact_register") || !slices.Contains(names, "artifact_read") || !slices.Contains(names, "result_get") {
			t.Fatalf("orchestrator tools: %v", names)
		}
		if handoffToolGranted(r, "artifact_register") {
			t.Fatal("withheld tool callable")
		}
	}
}

func TestFrozenSetupWithoutPolicyIsRefusedNotReinterpreted(t *testing.T) {
	e, g := nativeAuditGraph(t, "", 1)
	var run string
	for _, m := range g.Members {
		if m.Spec.Kind == "application" {
			run = m.CurrentRunID
		}
	}
	// A setup frozen before enforcement policies existed has no policy value.
	e.Store.mu.Lock()
	e.Store.graphs[g.ID].Setup = &FrozenSetup{Runtime: "codex"}
	e.Store.mu.Unlock()
	if ok, err := e.Store.AcquireClaims(run); err != nil || !ok {
		t.Fatal(err)
	}
	r, _ := e.Store.Get(run)
	e.execute(context.Background(), r)
	r, _ = e.Store.Get(run)
	if r.Status != "failed" || !strings.Contains(r.Summary, "predate permission enforcement") {
		t.Fatalf("pre-policy setup: %s %s", r.Status, r.Summary)
	}
	if _, err := e.Store.Artifact(run, "authority.json"); err == nil {
		t.Fatal("authority resolved under an unknown policy")
	}
}
