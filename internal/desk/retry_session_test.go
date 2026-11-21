package desk

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"mitos/internal/agentcli"
	"mitos/internal/agentretry"
)

// echoAgents installs two adapters: "echoer" prints its positional prompt and
// the retry policy environment, and "conductor" stays alive as an orchestrator.
func echoAgents(t *testing.T, m *Manager) {
	t.Helper()
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	m.Agents, err = agentcli.New(nil, agentcli.Config{Applications: []agentcli.Definition{
		{
			ID: "echoer", Name: "Echoer", Kind: "agent", Executables: []string{shell},
			Args:   []string{"--noprofile", "--norc", "-c", `printf '%s' "$1"; printf '\nPOLICY=%s' "$MITOS_RETRY_POLICY"`},
			Prompt: "positional",
		},
		{
			ID: "conductor", Name: "Conductor", Kind: "agent", Executables: []string{shell},
			Args:   []string{"--noprofile", "--norc", "-c", `sleep 30`},
			Prompt: "positional",
		},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func startConductor(t *testing.T, m *Manager, p Project) Session {
	t.Helper()
	s, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "conductor", Role: "orchestrator", Prompt: "Direct the audit"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(s.ID) })
	return s
}

func launchEcho(t *testing.T, m *Manager, p Project, prompt string) Session {
	t.Helper()
	s, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "echoer", Prompt: prompt}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { v, _ := m.Session(s.ID); return v.Status == "exited" })
	t.Cleanup(func() { _ = m.Stop(s.ID) })
	return s
}

func readEcho(t *testing.T, m *Manager, id string) string {
	t.Helper()
	// The pty drain can finish after process exit; wait for stable size.
	var text string
	await(t, func() bool {
		out, err := m.ReadOutput(id, 0)
		if err != nil {
			return false
		}
		normalized := strings.ReplaceAll(string(out.Data), "\r\n", "\n")
		if normalized == text {
			return true
		}
		text = normalized
		return false
	})
	return text
}

func TestOperatorObjectiveWithoutOrchestratorAllowsAuthorizedFanOut(t *testing.T) {
	m, p := testManager(t)
	echoAgents(t, m)
	s, err := m.StartAgent(p.ID, agentcli.Request{AgentID: "echoer", Prompt: "Audit the repository"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { v, _ := m.Session(s.ID); return v.Status == "exited" })
	text := readEcho(t, m, s.ID)
	if !strings.Contains(text, "Operator objective:\nAudit the repository") {
		t.Fatal("operator objective was not delivered", text)
	}
	if !strings.Contains(text, "operator started this session directly") {
		t.Fatal("operator-started workers must learn that operator-authorized fan-out is permitted", text)
	}
	if strings.Contains(text, "instead of spawning native CLI sub-agents") {
		t.Fatal("orchestrator-only delegation rule leaked into an operator-started session", text)
	}
	if !strings.Contains(text, "POLICY=") {
		t.Fatal("retry policy environment was not injected", text)
	}
}

func TestOperatorObjectiveUnderOrchestratorKeepsDelegationRule(t *testing.T) {
	m, p := testManager(t)
	echoAgents(t, m)
	startConductor(t, m, p)
	s := launchEcho(t, m, p, "Audit concurrency")
	text := readEcho(t, m, s.ID)
	if !strings.Contains(text, "instead of spawning native CLI sub-agents") {
		t.Fatal("sessions adopted by an orchestrator must keep the delegation rule", text)
	}
}

func TestSlashCommandObjectivePassesThroughUnwrapped(t *testing.T) {
	m, p := testManager(t)
	echoAgents(t, m)
	s := launchEcho(t, m, p, "/goal --workers 3 Audit the repository")
	text := readEcho(t, m, s.ID)
	if strings.TrimSpace(strings.SplitN(text, "\nPOLICY=", 2)[0]) != "/goal --workers 3 Audit the repository" {
		t.Fatal("slash command must reach the CLI verbatim, unwrapped:", text)
	}
}

func TestRetryPolicyEnvironmentReflectsProjectProfile(t *testing.T) {
	m, p := testManager(t)
	echoAgents(t, m)
	custom := agentretry.Policy{MaxAttempts: 4, FallbackModels: []string{"zai/glm-5.3-flash"}, TransientCodes: []int{429}}
	if _, err := m.SaveProfile(p.ID, func() ProjectProfile {
		profile := DefaultProjectProfile()
		profile.RetryPolicy = &custom
		return profile
	}(), 0); err != nil {
		t.Fatal(err)
	}
	s := launchEcho(t, m, p, "Check policy plumbing")
	text := readEcho(t, m, s.ID)
	idx := strings.Index(text, "POLICY=")
	if idx < 0 {
		t.Fatal("missing policy in output")
	}
	var decoded agentretry.Policy
	if err := json.Unmarshal([]byte(strings.TrimSpace(text[idx+len("POLICY="):])), &decoded); err != nil {
		t.Fatal(err)
	}
	effective := decoded.Effective()
	if effective.MaxAttempts != 4 || len(effective.FallbackModels) != 1 || effective.FallbackModels[0] != "zai/glm-5.3-flash" || !effective.IsTransient(429) || effective.IsTransient(500) {
		t.Fatal("project retry policy did not reach the session environment", effective)
	}
}

func TestSessionContextWordingByRole(t *testing.T) {
	project := Project{ID: "p", Name: "P", Directory: "/tmp"}
	if got := sessionContext(project, "agent", ""); !strings.Contains(got, "operator started this session directly") {
		t.Fatal("missing operator-started wording")
	}
	if got := sessionContext(project, "agent", "session_orch"); strings.Contains(got, "operator started this session directly") {
		t.Fatal("orchestrator-assigned worker must not be told operator fan-out wording")
	}
	if got := sessionContext(project, "agent", "session_orch"); !strings.Contains(got, "instead of spawning native CLI sub-agents") {
		t.Fatal("missing delegation wording for orchestrated workers")
	}
}

func TestAgentStatusReportsRecordRetriesErrorsAndUsage(t *testing.T) {
	m, p := testManager(t)
	echoAgents(t, m)
	s := launchEcho(t, m, p, "Status flow")
	now := time.Now().UTC()
	if err := m.AgentStatus(s.ID, AgentStatusReport{State: "retry_scheduled", Error: "429: limit", HTTPCode: 429, Attempt: 2, MaxAttempts: 3, Model: "zai/glm-5.3", RetryInSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	v, _ := m.Session(s.ID)
	if v.Retry == nil || v.Retry.Status != "scheduled" || v.Retry.Attempt != 2 || v.Retry.RetryAt == nil || v.Retry.RetryAt.Before(now) || v.AgentError == nil || v.AgentError.Code != 429 {
		t.Fatal("retry_scheduled report not recorded", v.Retry, v.AgentError)
	}
	if err := m.AgentStatus(s.ID, AgentStatusReport{State: "model_switched", Model: "zai/glm-5.3", NextModel: "zai/glm-5.3-flash", Attempt: 1, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	v, _ = m.Session(s.ID)
	if v.Retry == nil || v.Retry.Status != "switched" || v.Retry.NextModel != "zai/glm-5.3-flash" || v.AgentError == nil {
		t.Fatal("model_switched report must keep the last error and record the switch", v.Retry, v.AgentError)
	}
	usage := map[string]ModelUsage{"zai/glm-5.3": {InputTokens: 1200, OutputTokens: 300}}
	if err := m.AgentStatus(s.ID, AgentStatusReport{State: "retry_exhausted", Error: "429: limit", HTTPCode: 429, Attempt: 3, MaxAttempts: 3, Model: "zai/glm-5.3", Usage: usage}); err != nil {
		t.Fatal(err)
	}
	v, _ = m.Session(s.ID)
	if v.Retry == nil || v.Retry.Status != "exhausted" || v.Usage["zai/glm-5.3"].InputTokens != 1200 {
		t.Fatal("retry_exhausted report not recorded with usage", v.Retry, v.Usage)
	}
	// A later model's usage merges per model instead of relabeling the totals.
	extended := map[string]ModelUsage{"zai/glm-5.3": {InputTokens: 1200, OutputTokens: 300}, "openai/gpt-5.3-codex-spark": {InputTokens: 60, OutputTokens: 12}}
	if err := m.AgentStatus(s.ID, AgentStatusReport{State: "healthy", Usage: extended}); err != nil {
		t.Fatal(err)
	}
	v, _ = m.Session(s.ID)
	if v.AgentError != nil || v.Retry != nil || v.Usage["zai/glm-5.3"].InputTokens != 1200 || v.Usage["openai/gpt-5.3-codex-spark"].InputTokens != 60 {
		t.Fatal("healthy report must clear recovery state and keep per-model usage", v.AgentError, v.Retry, v.Usage)
	}
}

func TestAgentStatusRejectsInvalidReports(t *testing.T) {
	m, p := testManager(t)
	echoAgents(t, m)
	s := launchEcho(t, m, p, "Validation")
	for _, report := range []AgentStatusReport{
		{State: "unknown"},
		{State: "error", Error: strings.Repeat("x", 2001)},
		{State: "retry_scheduled", RetryInSeconds: -5},
		{State: "retry_scheduled", RetryInSeconds: 90000},
		{State: "retry_scheduled", Attempt: 101},
		{State: "error", HTTPCode: 999},
		{State: "error", Model: "two words"},
		{State: "healthy", Usage: map[string]ModelUsage{"m": {InputTokens: -1}}},
		{State: "healthy", Usage: map[string]ModelUsage{"bad model": {InputTokens: 1}}},
		{State: "healthy", Usage: func() map[string]ModelUsage {
			m := map[string]ModelUsage{}
			for i := 0; i < 17; i++ {
				m[fmt.Sprintf("p/m%d", i)] = ModelUsage{}
			}
			return m
		}()},
	} {
		if err := m.AgentStatus(s.ID, report); err == nil {
			t.Fatalf("report %+v accepted", report)
		}
	}
	if err := m.AgentStatus("session_missing", AgentStatusReport{State: "healthy"}); err == nil {
		t.Fatal("unknown session accepted")
	}
}
