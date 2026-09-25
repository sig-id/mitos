// Package agentcli describes native agent CLIs and local applications without
// imposing a shared configuration, credential store, or memory format.
package agentcli

import "time"

type Capabilities struct {
	ProjectContext   bool   `json:"project_context"`
	Transport        string `json:"transport"`
	InitialPrompt    bool   `json:"initial_prompt"`
	StructuredEvents bool   `json:"structured_events"`
	Fork             bool   `json:"fork"`
	Resume           bool   `json:"resume"`
	Usage            bool   `json:"usage"`
}

type Definition struct {
	ContextArgs     []string `json:"context_args,omitempty"`     // explicit noninteractive custom adapter; JSON profile on stdout
	SessionProtocol string   `json:"session_protocol,omitempty"` // codex, claude, opencode, or pi
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Kind            string   `json:"kind"` // agent, application, or shell
	Executables     []string `json:"executables"`
	Args            []string `json:"args,omitempty"`
	VersionArgs     []string `json:"version_args,omitempty"`
	Prompt          string   `json:"prompt"` // positional, none, or a flag
	EnvNames        []string `json:"environment_names,omitempty"`
	Docs            string   `json:"docs,omitempty"`
}

type Environment struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Present bool   `json:"present"`
}

type Resource struct {
	Kind    string `json:"kind"`
	Scope   string `json:"scope"`
	Path    string `json:"path"`
	Present bool   `json:"present"`
	Status  string `json:"status"`
}

// Profile values are environment variable NAMES, never credentials. Each launch
// gets an independent environment; profiles cannot mutate another agent's setup.
type Profile struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	AgentID    string            `json:"agent_id"`
	Executable string            `json:"executable,omitempty"`
	Env        map[string]string `json:"env,omitempty"` // target -> server variable
	Unset      []string          `json:"unset,omitempty"`
}

type Config struct {
	Applications []Definition `json:"applications,omitempty"`
	Profiles     []Profile    `json:"profiles,omitempty"`
}

type Installation struct {
	Definition
	Executable   string        `json:"executable,omitempty"`
	Installed    bool          `json:"installed"`
	Version      string        `json:"version,omitempty"`
	Probe        string        `json:"probe"`
	Capabilities Capabilities  `json:"capabilities"`
	Environment  []Environment `json:"environment"`
	Resources    []Resource    `json:"resources"`
	Profiles     []Profile     `json:"profiles"`
	Auth         string        `json:"auth"`
	CheckedAt    time.Time     `json:"checked_at"`
}

type Request struct {
	AgentID    string      `json:"agent_id"`
	ProfileID  string      `json:"profile_id,omitempty"`
	Prompt     string      `json:"prompt,omitempty"`
	Role       string      `json:"role,omitempty"`
	Assignment *Assignment `json:"assignment,omitempty"`
}

// Assignment carries Mitos fleet lineage, independently of any native CLI
// sub-agents. Goal/task IDs are references for the controller, not completion claims.
type Assignment struct {
	GoalID         string `json:"goal_id,omitempty"`
	TaskID         string `json:"task_id,omitempty"`
	RequestedBy    string `json:"requested_by,omitempty"`
	HandoffTopicID string `json:"handoff_topic_id,omitempty"`
}

// Launch is internal process input. Environment values are deliberately excluded
// from JSON and must never be persisted with the session or sent to the browser.
type Launch struct {
	NativeSessionID string
	AgentID         string
	AgentName       string
	ProfileID       string
	Kind            string
	Executable      string
	Args            []string
	Env             []string `json:"-"`
}

// Adapter is the boundary for future native RPC/ACP implementations. PTY is the
// implemented transport; native capabilities are not advertised as implemented.
type Adapter interface {
	Definition() Definition
	Capabilities() Capabilities
	Resources(workspace string, environment map[string]string) []Resource
	Arguments(prompt string) ([]string, error)
	SessionArguments(mode, reference, prompt string) ([]string, error)
}
