package agentcli

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestModelCatalogPerRunnableEnvironment(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "calls")
	fakeCLI(t, dir, "codex", `
[ "$1" = app-server ] || exit 1
echo called >> "$CALLS"
read -r init
echo '{"id":1,"result":{}}'
read -r ready
read -r list
echo '{"id":2,"result":{"data":[{"model":"hidden","hidden":true},{"model":"model-a","isDefault":true,"supportedReasoningEfforts":[{"reasoningEffort":"low"}]}],"nextCursor":"more"}}'
read -r page
case "$page" in *'"cursor":"more"'*) echo '{"id":3,"result":{"data":[{"model":"model-a"},{"model":"model-b","supportedReasoningEfforts":[]}]}}';; esac
`)
	claude := fakeCLI(t, dir, "claude-wrapper", `
read -r init
echo '{"type":"system","secrets":"ignored"}'
echo '{"type":"control_response","response":{"subtype":"success","request_id":"other","response":{"models":[{"value":"wrong"}]}}}'
echo '{"type":"control_response","response":{"subtype":"success","request_id":"models","response":{"models":[{"value":"default"},{"value":"'$ALIAS'","displayName":"Work model","supportsEffort":false},{"value":"--bad-option"}]}}}'
`)
	r, err := New(Builtins(), Config{Profiles: []Profile{
		{ID: "work", Name: "Work", AgentID: "claude", Executable: claude, Env: map[string]string{"ALIAS": "WORK_MODEL"}},
		{ID: "broken", Name: "Broken", AgentID: "claude", Executable: claude, Env: map[string]string{"KEY": "MISSING"}},
		{ID: "shared", Name: "Shared", AgentID: "codex"},
	}}, []string{"PATH=" + dir, "CALLS=" + count, "WORK_MODEL=claude-custom"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		for _, a := range r.ListWithModels(t.Context(), dir) {
			switch a.ID {
			case "codex":
				want := []HarnessModel{{ID: "model-a", Default: true, Efforts: []string{"low"}}, {ID: "model-b", Efforts: []string{}}}
				if len(a.HarnessEnvironments) != 2 || a.HarnessEnvironments[0].ProfileID != "" || a.HarnessEnvironments[1].ProfileID != "shared" {
					t.Fatal(a.HarnessEnvironments)
				}
				for _, env := range a.HarnessEnvironments {
					if !reflect.DeepEqual(env.Models, want) {
						t.Fatalf("profile %q models: %+v", env.ProfileID, env.Models)
					}
				}
			case "claude":
				want := []HarnessModel{{ID: "claude-custom", Name: "Work model", Efforts: []string{}}}
				if a.Installed || len(a.HarnessEnvironments) != 1 || a.HarnessEnvironments[0].ProfileID != "work" || !reflect.DeepEqual(a.HarnessEnvironments[0].Models, want) {
					t.Fatalf("profile-only installation: %+v", a)
				}
			default:
				if len(a.HarnessEnvironments) != 0 {
					t.Fatalf("unexpected coordinated runtime %s", a.ID)
				}
			}
		}
	}
	b, err := os.ReadFile(count)
	if err != nil || string(b) != "called\ncalled\n" {
		t.Fatalf("expected one probe per environment: %q, %v", b, err)
	}
}

func TestUnavailableCatalogKeepsRunnableHarness(t *testing.T) {
	dir := t.TempDir()
	fakeCLI(t, dir, "codex", "read -r init\nwhile :; do :; done")
	r, err := New(Builtins(), Config{}, []string{"PATH=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	for _, agent := range r.ListWithModels(ctx, dir) {
		if agent.ID == "codex" && (len(agent.HarnessEnvironments) != 1 || len(agent.HarnessEnvironments[0].Models) != 0) {
			t.Fatal(agent.HarnessEnvironments)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("catalog deadline exceeded")
	}
}
