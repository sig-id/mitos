// Package desk hosts trusted local interactive sessions and project coordination.
// It does not confer sandbox isolation or an unattended execution grant.
package desk

import (
	"mitos/internal/agentcli"
	"time"
)

type Project struct {
	Profile   ProjectProfile `json:"profile"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Directory string         `json:"directory"`
	CreatedAt time.Time      `json:"created_at"`
}
type Session struct {
	SetupDraftID       string               `json:"setup_draft_id,omitempty"`
	SupervisorID       string               `json:"supervisor_id,omitempty"`
	Workspace          string               `json:"workspace,omitempty"`
	WorktreeBranch     string               `json:"worktree_branch,omitempty"`
	PolicyRevision     int                  `json:"policy_revision,omitempty"`
	AttentionTransport string               `json:"attention_transport,omitempty"`
	NativeSessionID    string               `json:"native_session_id,omitempty"`
	InitialPrompt      string               `json:"initial_prompt,omitempty"`
	Continuation       *Continuation        `json:"continuation,omitempty"`
	Assignment         *agentcli.Assignment `json:"assignment,omitempty"`
	AgentID            string               `json:"agent_id,omitempty"`
	ProfileID          string               `json:"profile_id,omitempty"`
	NamePending        bool                 `json:"name_pending,omitempty"`
	Scratch            string               `json:"scratch,omitempty"`
	ID                 string               `json:"id"`
	ProjectID          string               `json:"project_id"`
	Name               string               `json:"name"`
	Role               string               `json:"role"`
	Command            string               `json:"command"`
	Status             string               `json:"status"`
	PID                int                  `json:"pid,omitempty"`
	Rows               uint16               `json:"rows"`
	Cols               uint16               `json:"cols"`
	CreatedAt          time.Time            `json:"created_at"`
	EndedAt            *time.Time           `json:"ended_at,omitempty"`
	ExitCode           *int                 `json:"exit_code,omitempty"`
	Detail             string               `json:"detail,omitempty"`
}
type Post struct {
	Sequence int       `json:"sequence"`
	Author   string    `json:"author"`
	Body     string    `json:"body"`
	At       time.Time `json:"at"`
}
type Topic struct {
	ID        string         `json:"id"`
	ProjectID string         `json:"project_id"`
	Title     string         `json:"title"`
	Summary   string         `json:"summary"`
	Paths     []string       `json:"paths"`
	Revision  int            `json:"revision"`
	UpdatedAt time.Time      `json:"updated_at"`
	Posts     []Post         `json:"posts,omitempty"`
	Followers map[string]int `json:"followers,omitempty"`
}
type Job struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Initiator string     `json:"initiator"`
	Consumers []string   `json:"consumers"`
	Command   []string   `json:"command"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
	Detail    string     `json:"detail"`
}
type state struct {
	ProfileDrafts []ProfileDraft `json:"profile_drafts,omitempty"`
	Notices       []Notice       `json:"notices,omitempty"`
	Version       int            `json:"version"`
	Projects      []Project      `json:"projects"`
	Sessions      []Session      `json:"sessions"`
	Topics        []Topic        `json:"topics"`
	Jobs          []Job          `json:"jobs"`
}
type Output struct {
	Data   []byte `json:"data"` // JSON base64 preserves arbitrary terminal bytes.
	Next   int64  `json:"next"`
	Status string `json:"status"`
}
