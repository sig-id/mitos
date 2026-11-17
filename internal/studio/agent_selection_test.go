package studio

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mitos/agents"
	"mitos/internal/agentcli"
)

func selectionRegistry(t *testing.T, e *Engine, fakes ...string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	bin := t.TempDir()
	for _, id := range fakes {
		if err = os.WriteFile(filepath.Join(bin, id), []byte("#!"+python+"\nimport sys\nsys.stdin.read()\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	registry, err := agentcli.New(agentcli.Builtins(), agentcli.Config{}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents = registry
}

func selectionDefinition(t *testing.T, requires, prefers []string) agents.Definition {
	t.Helper()
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range library {
		if d.ID == "mitos.generalist" {
			m := d.Manifest
			m.Runtime.Requires = requires
			m.Runtime.Preferences = prefers
			return agents.Definition{ID: d.ID, Revision: d.Revision, Name: d.Name, Description: d.Description, Kind: d.Kind, Digest: d.Digest, Instructions: d.Instructions, Manifest: m}
		}
	}
	t.Fatal("mitos.generalist missing")
	return agents.Definition{}
}

// Runtime selection is recorded with every considered candidate, refuses
// incompatible selected runtimes instead of dropping requirements, and falls
// back only for the default worker preset.
func TestRuntimeSelectionRecordsCandidatesAndRefusesIncompatible(t *testing.T) {
	e, _ := graphEngine(t, false)
	selectionRegistry(t, e, "codex", "claude")

	bound := &AgentBinding{Definition: selectionDefinition(t, []string{"native_output", "tool_use"}, nil), Runtime: "codex", Executable: "/usr/bin/codex"}
	if err := e.selectAgentRuntime(bound); err != nil {
		t.Fatal(err)
	}
	if bound.Selection == nil || bound.Selection.Runtime != "codex" || len(bound.Selection.Candidates) != 2 {
		t.Fatalf("selection receipt missing: %+v", bound.Selection)
	}
	for _, c := range bound.Selection.Candidates {
		if c.Runtime == "codex" && (!c.Available || !c.Compatible) {
			t.Fatalf("codex candidate wrong: %+v", c)
		}
	}

	// A required feature no runtime provides refuses with the reasons recorded.
	vision := &AgentBinding{Definition: selectionDefinition(t, []string{"native_output", "vision"}, nil), Runtime: "codex", Executable: "/usr/bin/codex"}
	err := e.selectAgentRuntime(vision)
	if err == nil || !strings.Contains(err.Error(), "missing features: vision") {
		t.Fatalf("unsupported requirement accepted: %v", err)
	}
	if vision.Selection != nil {
		t.Fatal("refused selection left a receipt")
	}

	// Preferences order the considered candidates but cannot force an
	// incompatible runtime.
	preferred := &AgentBinding{Definition: selectionDefinition(t, []string{"native_output"}, []string{"claude"}), Runtime: "codex", Executable: "/usr/bin/codex"}
	if err = e.selectAgentRuntime(preferred); err != nil {
		t.Fatal(err)
	}
	if preferred.Selection.Candidates[0].Runtime != "claude" || preferred.Selection.Runtime != "codex" {
		t.Fatalf("preferences not recorded: %+v", preferred.Selection)
	}

	// An explicitly selected incompatible runtime is refused even when a
	// compatible one exists; the default worker runtime falls back.
	pinned := &AgentBinding{Definition: selectionDefinition(t, []string{"native_output", "proposal_tools"}, nil), Runtime: "claude", Executable: "/usr/bin/claude", SelectionSource: "role rule"}
	if err = e.selectAgentRuntime(pinned); err != nil {
		t.Fatal(err)
	}
	if pinned.Selection.Runtime != "claude" {
		t.Fatalf("compatible pinned runtime refused: %+v", pinned.Selection)
	}
	defaults := &AgentBinding{Definition: selectionDefinition(t, []string{"native_output"}, nil), Runtime: "claude", SelectionSource: "worker default"}
	if err = e.selectAgentRuntime(defaults); err != nil || defaults.Selection.Runtime != "claude" {
		t.Fatalf("default selection failed: %v %+v", err, defaults.Selection)
	}
}

// With no installed runtime, selection fails closed with the requirement list.
func TestRuntimeSelectionFailsWithoutInstalledRuntimes(t *testing.T) {
	e, _ := graphEngine(t, false)
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	registry, err := agentcli.New(agentcli.Builtins(), agentcli.Config{}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents = registry
	b := &AgentBinding{Definition: selectionDefinition(t, []string{"native_output", "tool_use"}, nil), Runtime: "codex"}
	err = e.selectAgentRuntime(b)
	if err == nil || !strings.Contains(err.Error(), "No installed runtime provides") {
		t.Fatalf("missing runtime accepted: %v", err)
	}
}
