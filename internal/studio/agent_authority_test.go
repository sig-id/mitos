package studio

import (
	"context"
	"errors"
	"mitos/internal/compute"
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
			_, err := resolveAuthority(d, "codex", policy, 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}})
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
		if _, err := resolveAuthority(d, "codex", EnforcementStrict, 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}}); err == nil {
			t.Fatalf("strict accepted %s although reads and commands cannot be confined", d.ID)
		}
	}
	if _, err := resolveAuthority(library[0], "codex", "", 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}}); err == nil {
		t.Fatal("missing policy accepted")
	}
	if _, err := resolveAuthority(library[0], "unsupported", EnforcementAdvisory, 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}}); err == nil {
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
	r, err := resolveAuthority(engineer, "codex", EnforcementAdvisory, 600, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}})
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
	if r, _ = resolveAuthority(engineer, "codex", EnforcementAdvisory, 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}}); r.TimeoutSeconds != engineer.Manifest.Limits.TimeoutSeconds {
		t.Fatal("definition timeout not applied")
	}

	// Unsupported required capabilities and features always refuse.
	m := architect.Manifest
	m.Capabilities.Required = []string{"gpu.use"}
	m.Capabilities.Optional = []string{"workspace.read"}
	if _, err = resolveAuthority(agents.Definition{ID: m.ID, Manifest: m}, "codex", EnforcementAdvisory, 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}}); err == nil {
		t.Fatal("unsupported required capability accepted")
	}
	m = architect.Manifest
	m.Runtime.Requires = []string{"native_output", "vision"}
	if _, err = resolveAuthority(agents.Definition{ID: m.ID, Manifest: m}, "codex", EnforcementAdvisory, 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}}); err == nil {
		t.Fatal("missing runtime feature accepted")
	}
}

func TestHandoffToolsFollowArtifactGrants(t *testing.T) {
	library, _ := agents.Library()
	for _, d := range library {
		if d.ID != "mitos.orchestrator" {
			continue
		}
		r, err := resolveAuthority(d, "codex", EnforcementAdvisory, 0, compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}})
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

// Under a confining provider, strict_required admits the bundled agents that
// require workspace.read, and receipts mark that capability enforced. Receipts
// never claim enforcement the provider does not provide.
func TestConfiningProviderEnablesStrictRequired(t *testing.T) {
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	confining := (&compute.Local{Confinement: compute.Confinement{Tool: compute.ConfinementBwrap, NetMode: compute.NetOff}}).Capabilities()
	advisory := (&compute.Local{}).Capabilities()
	find := func(id string) agents.Definition {
		for _, d := range library {
			if d.ID == id {
				return d
			}
		}
		t.Fatalf("missing %s", id)
		return agents.Definition{}
	}
	for _, id := range []string{"mitos.engineer", "mitos.tester"} {
		receipt, err := resolveAuthority(find(id), "codex", EnforcementStrictRequired, 60, confining)
		if err != nil {
			t.Fatalf("%s refused under confining provider: %v", id, err)
		}
		if !receipt.Granted("workspace.read") || receipt.capability("workspace.read").Status != CapabilityEnforced {
			t.Fatalf("%s workspace.read not enforced: %+v", id, receipt.Capabilities)
		}
		if receipt.Granted("network.read") {
			t.Fatalf("%s granted withheld network", id)
		}
		if receipt.capability("network.read").Status != CapabilityEnforced {
			t.Fatalf("%s withheld network not marked enforced: %+v", id, receipt.capability("network.read"))
		}
		// The same definitions stay refused under the advisory provider, and
		// its receipts never claim read enforcement.
		if _, err = resolveAuthority(find(id), "codex", EnforcementStrictRequired, 60, advisory); err == nil {
			t.Fatalf("%s admitted under advisory provider", id)
		}
		advisoryReceipt, err := resolveAuthority(find(id), "codex", EnforcementAdvisory, 60, advisory)
		if err != nil || advisoryReceipt.capability("workspace.read").Status != CapabilityAdvisory {
			t.Fatalf("advisory provider claimed enforcement: %+v %v", advisoryReceipt.Capabilities, err)
		}
		// Claude gains the same OS confinement instead of advisory notes.
		claude, err := resolveAuthority(find(id), "claude", EnforcementStrictRequired, 60, confining)
		if err != nil || claude.capability("workspace.read").Status != CapabilityEnforced {
			t.Fatalf("claude not confined: %+v %v", claude.Capabilities, err)
		}
	}
	if _, err := resolveAuthority(find("mitos.orchestrator"), "codex", EnforcementStrict, 60, confining); err == nil {
		t.Fatal("strict policy accepted an orchestrator whose optional workspace.read is advisory")
	}
	profiles := RuntimeCapabilityProfilesFor(confining)
	codex := profiles["codex"].(map[string]any)["capabilities"].(map[string]any)["workspace.read"].(map[string]any)
	if codex["bounded_grant"] != true || codex["blocked_denial"] != true {
		t.Fatalf("published profile not provider-derived: %+v", codex)
	}
}
