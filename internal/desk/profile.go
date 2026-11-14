package desk

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ProjectProfile is operator guidance. Only layout and launch limits are runtime
// controls; free-form Git rules do not constitute an OS permission boundary.
type ProjectProfile struct {
	Context     string   `json:"context"`
	Revision    int      `json:"revision"`
	Goal        string   `json:"goal"`
	GitMode     string   `json:"git_mode"`
	GitRules    string   `json:"git_rules"`
	PushRules   string   `json:"push_rules"`
	MergeRules  string   `json:"merge_rules"`
	ReviewRules string   `json:"review_rules"`
	TempPaths   []string `json:"temp_paths"`
	DiskRules   string   `json:"disk_rules"`
	MinFreeMB   int      `json:"min_free_mb"`
	MaxAgents   int      `json:"max_agents"`
	Rules       string   `json:"rules"`
}

func DefaultProjectProfile() ProjectProfile {
	return ProjectProfile{GitMode: "shared", GitRules: "Inspect Git status before editing. Preserve existing work. Coordinate branch, index, and history changes with the orchestrator. Never reset or discard another agent's changes.", PushRules: "Ask the operator before pushing. Never force-push without explicit approval.", MergeRules: "Wait for review and passing checks. Ask the operator before merging. Keep unrelated changes separate.", ReviewRules: "An independent review and relevant tests are required before merge. Report evidence and unresolved risks.", TempPaths: []string{}, DiskRules: "Use MITOS_SCRATCH for temporary files. Inspect disk use before large builds. Reuse shared build jobs. Do not delete other sessions' files or worktrees. Ask before cleanup outside your scratch directory.", MinFreeMB: 512, MaxAgents: 4, Rules: "Delegate through Mitos sessions, not native CLI sub-agents. Publish progress, blockers, evidence, and handoffs on the topic board."}
}
func effectiveProfile(p Project) ProjectProfile {
	if p.Profile.GitMode == "" {
		return DefaultProjectProfile()
	}
	out := p.Profile
	out.TempPaths = append([]string{}, out.TempPaths...)
	return out
}
func validateProjectProfile(profile ProjectProfile) error {
	if profile.GitMode != "shared" && profile.GitMode != "worktree" {
		return errors.New("choose shared checkout or per-agent worktrees")
	}
	if profile.MaxAgents < 1 || profile.MaxAgents > 15 || profile.MinFreeMB < 0 || profile.MinFreeMB > 1048576 || len(profile.TempPaths) > 16 {
		return errors.New("invalid project launch limits")
	}
	text := profile.Context + profile.Goal + profile.GitRules + profile.PushRules + profile.MergeRules + profile.ReviewRules + profile.DiskRules + profile.Rules + strings.Join(profile.TempPaths, "\n")
	if strings.ContainsRune(text, 0) {
		return errors.New("project guidance cannot contain NUL bytes")
	}
	if len(text) > 16000 {
		return errors.New("project guidance exceeds 16000 bytes")
	}
	for _, p := range profile.TempPaths {
		if !filepath.IsAbs(p) || strings.ContainsAny(p, "\x00\r\n") {
			return errors.New("temporary paths must be absolute paths")
		}
	}
	return nil
}

func (m *Manager) SaveProfile(project string, profile ProjectProfile, expected int) (ProjectProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveProfileLocked(project, profile, expected)
}
func (m *Manager) saveProfileLocked(project string, profile ProjectProfile, expected int) (ProjectProfile, error) {
	if err := validateProjectProfile(profile); err != nil {
		return profile, err
	}
	if err := m.writable(); err != nil {
		return profile, err
	}
	for i := range m.state.Projects {
		p := &m.state.Projects[i]
		if p.ID != project {
			continue
		}
		old := effectiveProfile(*p)
		if old.Revision != expected {
			return profile, ErrConflict
		}
		profile.Revision = old.Revision + 1
		profile.TempPaths = append([]string{}, profile.TempPaths...)
		p.Profile = profile
		notice := m.notifyLocked(project, fmt.Sprintf("Project profile updated to revision %d. Read the current policy with desk profile before continuing. Workspace layout changes apply to new sessions; existing checkouts are retained.", profile.Revision), "")
		if err := m.saveLocked(); err != nil {
			return profile, err
		}
		m.deliverNoticeLocked(notice)
		return effectiveProfile(*p), nil
	}
	return profile, ErrNotFound
}
func profileContext(p Project) string {
	v := effectiveProfile(p)
	return fmt.Sprintf("Project: %s\nWorkspace: %s\nProfile revision: %d\nProject context: %s\nGoal: %s\nWorkspace layout: %s\nGit rules: %s\nPush rules: %s\nMerge rules: %s\nReview requirements: %s\nTemporary files: use MITOS_SCRATCH. Additional allowed paths: %s\nDisk: %s\nAdditional rules: %s\n", p.Name, p.Directory, v.Revision, v.Context, v.Goal, v.GitMode, v.GitRules, v.PushRules, v.MergeRules, v.ReviewRules, strings.Join(v.TempPaths, ", "), v.DiskRules, v.Rules)
}
func sessionContext(p Project, role string) string {
	text := "Mitos project instructions (operator-configured):\n" + profileContext(p) + "\nUse the executable in MITOS_BIN with the desk subcommand. MITOS_PROJECT_ID and MITOS_SESSION_ID identify this project and execution. MITOS_WORKSPACE and your current directory identify the actual checkout; use them for all edits and tests. A separate worktree begins at committed HEAD and does not contain uncommitted changes from the project checkout. Managed desk build jobs run in the project checkout, so they cannot verify changes in an agent worktree. Read MITOS_COORDINATION for detailed tool usage. Read `desk inbox` before critical work, acknowledge attention with `desk acknowledge ID`, and respect an active pause until its author or orchestrator releases it. Reading a notice is not acknowledgment. Never infer that all agents stopped until every target has acknowledged.\n"
	if role == "orchestrator" {
		text += "You are the Mitos orchestrator. Your workflow is plan -> delegate -> monitor -> review. Workers carry out assigned research, code, documentation, and tests; you coordinate and verify their results. Before implementation or other deliverable work, publish a concise plan on the topic board and launch at least one Mitos worker with a bounded assignment, acceptance criteria, and required checks. Even a single implementation task goes to a worker. You may inspect the repository to plan and review, and run independent validation, but do not make implementation edits yourself or produce assigned documents yourself unless the operator explicitly asks you to take over. If a worker cannot be launched, report the blocker instead of silently doing its work. Own the project plan and delegate actual tasks using `desk start-session --agent AGENT --prompt TASK`; this creates a visible Mitos agent assigned to you. Do not launch native CLI sub-agents. Discover installed agents with `desk agents`. Inspect `desk sessions` and `desk coordination`. Keep assignments bounded, coordinate shared resources, review results, and publish evidence on the topic board. While delegated work is running, stay in the coordination loop: inspect sessions and evidence, acknowledge attention, and use `desk inbox --wait` between updates. This waits up to 20 seconds for new attention, so hook-based or manual adapters can keep coordinating without idle terminal injection. Do not declare the project complete merely because workers exited. Use `desk broadcast --body MESSAGE --pause` to request a breakpoint before critical shared operations. Wait for acknowledgments; release with `desk release-notice ID` after the critical operation. Respect the saved concurrency limit. Start with the supplied objective; if none is defined, ask the operator to define one before launching workers.\n"
	} else {
		text += "You are a Mitos worker. Complete your assigned task, report progress and evidence on the topic board, then use `desk broadcast --body MESSAGE` to notify the orchestrator of completion or a blocker (include the topic ID). Raise blockers to your orchestrator. Ask the orchestrator for additional agents instead of spawning native CLI sub-agents. Never push or merge without satisfying the project rules.\n"
	}
	text += "Read `desk work --task \"$MITOS_TASK_ID\"` for the durable assignment, completion criteria, coordination inbox and current revision. Report findings with `desk report-work --task \"$MITOS_TASK_ID\" --revision N --body REPORT`. Reports do not establish accepted completion. When delegating, pass --criteria with the required evidence. Replacement must preserve the same task; do not invent new task identities to resume failed work.\n"
	text += "For Codex, if CODEX_THREAD_ID is available in your tool environment, register it once with `desk link-session --native-session \"$CODEX_THREAD_ID\" \"$MITOS_SESSION_ID\"` so urgent context can reach this exact conversation.\n"
	return text
}
