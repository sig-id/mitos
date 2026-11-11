package desk

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/agentcli"
)

func draftManager(t *testing.T, analysis string) (*Manager, Project) {
	t.Helper()
	m, p := testManager(t)
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(p.Directory, "analysis.sh")
	if err = os.WriteFile(fixture, []byte(analysis), 0600); err != nil {
		t.Fatal(err)
	}
	m.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{{ID: "analyst", Name: "Analyst fixture", Kind: "agent", Executables: []string{shell}, Args: []string{"-c", `printf 'GOAL:%s\n' "$@"; while read -r line; do :; done`, "supervisor"}, ContextArgs: []string{fixture}, Prompt: "positional"}}, Profiles: []agentcli.Profile{{ID: "account", Name: "Selected account", AgentID: "analyst", Env: map[string]string{"ANALYSIS_ACCOUNT": "TEST_ANALYSIS_ACCOUNT"}}}}, append(os.Environ(), "TEST_ANALYSIS_ACCOUNT=account-marker", "MITOS_SESSION_ID=parent-must-not-leak"))
	if err != nil {
		t.Fatal(err)
	}
	return m, p
}
func awaitDraft(t *testing.T, m *Manager, p Project, id string) ProfileDraft {
	t.Helper()
	var result ProfileDraft
	await(t, func() bool { result, _ = m.ProfileDraft(p.ID, id); return result.Status != "running" })
	return result
}

func TestProfileDraftReviewBeforeStartingAndRetry(t *testing.T) {
	m, p := draftManager(t, `test "$ANALYSIS_ACCOUNT" = account-marker || exit 7
 test -z "$MITOS_SESSION_ID" || exit 8
 case "$*" in *"analysis only"*) ;; *) exit 9;; esac
 printf '{"context":"A parser library. Evidence: README.md, go.mod. Suggested check: go test ./... (not run).","goal":"Invented implementation task"}'
 `)
	req := ProfileDraftRequest{Key: "setup-one", AgentID: "analyst", ProfileID: "account"}
	first, err := m.StartProfileDraft(p.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := m.StartProfileDraft(p.ID, req)
	if err != nil || duplicate.ID != first.ID {
		t.Fatal("duplicate setup", duplicate, err)
	}
	d := awaitDraft(t, m, p, first.ID)
	if d.Status != "ready" || d.Profile == nil || !strings.Contains(d.Profile.Context, "README.md") {
		t.Fatal(d)
	}
	if d.Profile.Goal != "" {
		t.Fatal("analysis invented a work goal", d.Profile.Goal)
	}
	if len(m.Sessions(p.ID)) != 0 {
		t.Fatal("analysis started a working session")
	}
	saved, _ := m.Project(p.ID)
	if saved.Profile.Revision != 0 || saved.Profile.Context != "" {
		t.Fatal("unreviewed draft replaced profile", saved)
	}
	if _, err = m.ReviewAndStartProfileDraft(p.ID, d.ID, *d.Profile, 0, "http://localhost"); err == nil {
		t.Fatal("supervisor admitted without goal")
	}
	approved := *d.Profile
	approved.Goal = "Add malformed-input tests"
	approved.Context += "\nOperator correction: compatibility is required."
	s, err := m.ReviewAndStartProfileDraft(p.ID, d.ID, approved, 0, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	if s.Role != "coordinator" || s.InitialPrompt != approved.Goal || s.ProfileID != "account" || s.SetupDraftID != d.ID {
		t.Fatal(s)
	}
	await(t, func() bool {
		out, _ := m.ReadOutput(s.ID, 0)
		return strings.Contains(string(out.Data), "Operator correction") && strings.Contains(string(out.Data), approved.Goal)
	})
	replay, err := m.ReviewAndStartProfileDraft(p.ID, d.ID, approved, 0, "http://localhost")
	if err != nil || replay.ID != s.ID || len(m.Sessions(p.ID)) != 1 {
		t.Fatal("review replay launched twice", replay, err)
	}
	approved.Goal = "Unapproved change"
	if _, err = m.ReviewAndStartProfileDraft(p.ID, d.ID, approved, 0, "http://localhost"); !errors.Is(err, ErrConflict) {
		t.Fatal("review replay changed goal", err)
	}
}

func TestProfileDraftPersistsAndRejectsStaleProfile(t *testing.T) {
	m, p := draftManager(t, `printf '{"context":"Observed project context"}'`)
	d, err := m.StartProfileDraft(p.ID, ProfileDraftRequest{Key: "persist", AgentID: "analyst"})
	if err != nil {
		t.Fatal(err)
	}
	d = awaitDraft(t, m, p, d.ID)
	registry := m.Agents
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.Agents = registry
	loaded, err := reopened.ProfileDraft(p.ID, d.ID)
	if err != nil || loaded.Status != "ready" || loaded.Profile.Context != d.Profile.Context {
		t.Fatal(loaded, err)
	}
	profile := DefaultProjectProfile()
	profile.GitRules = "Keep the operator's newly edited rules."
	if _, err = reopened.SaveProfile(p.ID, profile, 0); err != nil {
		t.Fatal(err)
	}
	proposed := *loaded.Profile
	proposed.Goal = "Implement parser tests"
	if _, err = reopened.ReviewAndStartProfileDraft(p.ID, d.ID, proposed, 0, "http://localhost"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale draft overwrote profile", err)
	}
	saved, _ := reopened.Project(p.ID)
	if saved.Profile.GitRules != profile.GitRules || len(reopened.Sessions(p.ID)) != 0 {
		t.Fatal("stale review caused effects", saved)
	}
}
func TestProfileDraftCancelFailureAndRestart(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		m, p := draftManager(t, "sleep 60\n")
		d, err := m.StartProfileDraft(p.ID, ProfileDraftRequest{Key: "cancel", AgentID: "analyst"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = m.StartProfileDraft(p.ID, ProfileDraftRequest{Key: "another", AgentID: "analyst"}); !errors.Is(err, ErrConflict) {
			t.Fatal("parallel project analyses admitted", err)
		}
		if err = m.CancelProfileDraft(p.ID, d.ID); err != nil {
			t.Fatal(err)
		}
		m.Close()
		d, _ = m.ProfileDraft(p.ID, d.ID)
		if d.Status != "cancelled" || d.Profile != nil || len(m.Sessions(p.ID)) != 0 {
			t.Fatal(d)
		}
	})
	for _, body := range []string{`printf 'not a profile'`, `printf '{"context":"invented"}'; exit 2`} {
		t.Run("failure", func(t *testing.T) {
			m, p := draftManager(t, body)
			d, err := m.StartProfileDraft(p.ID, ProfileDraftRequest{Key: "bad", AgentID: "analyst"})
			if err != nil {
				t.Fatal(err)
			}
			d = awaitDraft(t, m, p, d.ID)
			if d.Status != "failed" || d.Profile != nil {
				t.Fatal(d)
			}
			saved, _ := m.Project(p.ID)
			if saved.Profile.Revision != 0 {
				t.Fatal(saved)
			}
		})
	}
	t.Run("restart", func(t *testing.T) {
		m, p := testManager(t)
		m.mu.Lock()
		m.state.ProfileDrafts = append(m.state.ProfileDrafts, ProfileDraft{ID: "setup_interrupted", ProjectID: p.ID, Status: "running"})
		err := m.saveLocked()
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		m.Close()
		reopened, err := Open(m.dir, m.root)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		d, err := reopened.ProfileDraft(p.ID, "setup_interrupted")
		if err != nil || d.Status != "interrupted" {
			t.Fatal(d, err)
		}
	})
}
func TestAgentRequiresGoal(t *testing.T) {
	m, p := draftManager(t, "")
	for _, role := range []string{"agent", "coordinator"} {
		if _, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "analyst", Role: role, Prompt: " \n "}, "http://localhost"); err == nil {
			t.Fatal("empty goal accepted", role)
		}
	}
	if len(m.Sessions(p.ID)) != 0 {
		t.Fatal("empty goal launched processes")
	}
}
func TestDraftParserRejectsInvalidPolicies(t *testing.T) {
	for _, raw := range []string{`{}`, `{"context":""}`, `{"context":"x","max_agents":99}`, `{"context":"x","temp_paths":["../relative"]}`, `{"context":"x","unknown":true}`} {
		if _, err := parseDraftProfile([]byte(raw), DefaultProjectProfile()); err == nil {
			t.Fatal("accepted invalid draft", raw)
		}
	}
	profile := DefaultProjectProfile()
	profile.Context = "Observed architecture"
	raw, _ := json.Marshal(profile)
	if _, err := parseDraftProfile([]byte("```json\n"+string(raw)+"\n```"), DefaultProjectProfile()); err != nil {
		t.Fatal(err)
	}
}

func TestProfileDraftVerboseProgressIsVisibleAndDoesNotCancel(t *testing.T) {
	m, p := draftManager(t, `head -c 200000 /dev/zero | tr '\000' x >&2
printf '\nReading README.md\n' >&2
while [ ! -f release-analysis ]; do sleep 0.01; done
printf '{"context":"Inspected README.md"}'
`)
	d, err := m.StartProfileDraft(p.ID, ProfileDraftRequest{Key: "verbose", AgentID: "analyst"})
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		out, err := m.ReadProfileDraftOutput(p.ID, d.ID)
		return err == nil && strings.Contains(out.Text, "Reading README.md")
	})
	current, _ := m.ProfileDraft(p.ID, d.ID)
	out, err := m.ReadProfileDraftOutput(p.ID, d.ID)
	if err != nil || current.Status != "running" || !out.Truncated || len(out.Text) > draftLogLimit {
		t.Fatal("verbose analysis did not remain running with bounded output", current, len(out.Text), err)
	}
	if _, err := m.ReadProfileDraftOutput("another-project", d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-project output exposed", err)
	}
	if err := os.WriteFile(filepath.Join(p.Directory, "release-analysis"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	d = awaitDraft(t, m, p, d.ID)
	if d.Status != "ready" || d.Profile.Context != "Inspected README.md" {
		t.Fatal(d)
	}
	out, err = m.ReadProfileDraftOutput(p.ID, d.ID)
	if err != nil || !strings.Contains(out.Text, "Reading README.md") {
		t.Fatal("saved output missing", err)
	}
	m.Close()
	reopened, err := Open(m.dir, m.root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	out, err = reopened.ReadProfileDraftOutput(p.ID, d.ID)
	if err != nil || !strings.Contains(out.Text, "Reading README.md") {
		t.Fatal("output did not survive restart", err)
	}
}
