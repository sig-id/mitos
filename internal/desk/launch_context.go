package desk

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/agentcli"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed pi_attention.mjs
var piAttention []byte

func (m *Manager) prepareSessionContextLocked(p Project, s *Session, launch *agentcli.Launch) error {
	profile := effectiveProfile(p)
	s.PolicyRevision = profile.Revision
	s.Workspace = p.Directory
	s.AttentionTransport = "manual"
	if s.Role != "coordinator" && s.Role != "shell" {
		active := 0
		for _, old := range m.state.Sessions {
			if s.Continuation != nil && s.Continuation.Kind == "handover" && old.ID == s.Continuation.SourceID {
				continue
			}
			if old.ID != s.ID && old.ProjectID == p.ID && old.Role == "agent" && activeSession(old) {
				active++
			}
		}
		if active >= profile.MaxAgents {
			return errors.New("project agent limit reached; finish or stop an agent before launching another")
		}
	}
	if s.Assignment != nil && s.Assignment.RequestedBy != "" && s.Assignment.RequestedBy != "operator" {
		parent, err := m.sessionLocked(s.Assignment.RequestedBy)
		if err == nil {
			if parent.Role == "coordinator" {
				s.SupervisorID = parent.ID
			} else {
				s.SupervisorID = parent.SupervisorID
			}
		}
	}
	if s.Role == "agent" && s.SupervisorID == "" {
		for _, old := range m.state.Sessions {
			if s.Continuation != nil && s.Continuation.Kind == "handover" && old.ID == s.Continuation.SourceID {
				continue
			}
			if old.ID != s.ID && old.ProjectID == p.ID && old.Role == "coordinator" && activeSession(old) {
				s.SupervisorID = old.ID
				break
			}
		}
	}
	if s.Role == "coordinator" {
		s.SupervisorID = ""
	}
	for _, directory := range []string{m.dir, p.Directory} {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(directory, &stat); err != nil {
			return err
		}
		if uint64(stat.Bavail)*uint64(stat.Bsize)/(1<<20) < uint64(profile.MinFreeMB) {
			return fmt.Errorf("free disk space at %s is below the project reserve; inspect storage before launching", directory)
		}
	}
	// Continuations retain the source checkout, including uncommitted changes.
	// A fork shares that checkout explicitly; it is not a filesystem snapshot.
	if s.Continuation != nil {
		source, err := m.sessionLocked(s.Continuation.SourceID)
		if err != nil {
			return err
		}
		if source.Workspace != "" {
			s.Workspace, s.WorktreeBranch = source.Workspace, source.WorktreeBranch
		}
	}
	if profile.GitMode == "worktree" && s.Role == "agent" && s.Continuation == nil {
		s.Workspace = filepath.Join(m.dir, "worktrees", s.ID)
		s.WorktreeBranch = "mitos/" + s.ID

	}
	// User settings remain untouched. These are invocation-scoped native adapters.
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	switch s.AgentID {
	case "pi":
		path := filepath.Join(m.dir, s.ID+"-attention.mjs")
		if err := os.WriteFile(path, piAttention, 0600); err != nil {
			return err
		}
		launch.Args = append([]string{"--extension", path}, launch.Args...)
		s.AttentionTransport = "native-push"
	case "claude":
		command := "'" + strings.ReplaceAll(binary, "'", "'\\''") + "' desk attention-hook"
		hooks := map[string]any{}
		for _, event := range []string{"PreToolUse", "PostToolUse", "UserPromptSubmit"} {
			hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command, "timeout": 8}}}}
		}
		settings, _ := json.Marshal(map[string]any{"hooks": hooks})
		launch.Args = append([]string{"--settings", string(settings)}, launch.Args...)
		s.AttentionTransport = "native-hook"
	case "codex":
		s.AttentionTransport = "native-queue"
	}
	return nil
}

// The session and worktree intent are persisted before Git changes the filesystem.
// Failed creation remains inspectable; never remove a checkout with possible work.
func (m *Manager) createSessionWorktreeLocked(p Project, s Session) error {
	if s.WorktreeBranch == "" || s.Continuation != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.Workspace), 0700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", p.Directory, "worktree", "add", "-b", s.WorktreeBranch, s.Workspace, "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create project worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
