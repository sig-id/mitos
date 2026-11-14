// Package studio owns the durable work record and local execution lifecycle.
package studio

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/acceptance"
	"mitos/internal/autonomy"
	"mitos/internal/decision"
	"mitos/internal/fleet"
	"mitos/internal/workpool"
	"strings"
	"time"
)

var ErrConflict = errors.New("this work has already changed; refresh and try again")
var ErrNotFound = errors.New("work not found")

type Brief struct {
	ProjectID      string           `json:"project_id,omitempty"`
	Scheduling     workpool.Request `json:"scheduling"`
	Ownership      fleet.Ownership  `json:"ownership,omitempty"`
	Title          string           `json:"title"`
	Intent         string           `json:"intent"`
	Workspace      string           `json:"workspace"`
	AppID          string           `json:"app_id"`
	TimeoutMinutes int              `json:"timeout_minutes"`
	ProjectLimits  bool             `json:"project_limits,omitempty"` // Workflow occurrences inherit the current project settings.
}

func (b *Brief) Validate() error {
	b.Scheduling.Normalize()
	if err := b.Scheduling.Validate(); err != nil {
		return err
	}
	if err := b.Ownership.Validate(); err != nil {
		return err
	}
	b.Title = strings.TrimSpace(b.Title)
	b.Intent = strings.TrimSpace(b.Intent)
	b.Workspace = strings.TrimSpace(b.Workspace)
	if len(b.Title) < 3 || len(b.Title) > 160 {
		return errors.New("give the work a title between 3 and 160 characters")
	}
	if len(b.Intent) > 10000 {
		return errors.New("keep the brief under 10,000 characters")
	}
	if b.Workspace == "" {
		return errors.New("choose a local workspace directory")
	}
	if b.ProjectLimits && b.AppID != WorkflowAppID {
		return errors.New("project execution limits require an agent workflow")
	}
	maxMinutes := 120
	if b.ProjectLimits {
		maxMinutes = 43200
	}
	if b.TimeoutMinutes < 1 || b.TimeoutMinutes > maxMinutes {
		return fmt.Errorf("choose a time limit from 1 to %d minutes", maxMinutes)
	}
	return nil
}

type Run struct {
	GraphAttempt       *GraphAttempt          `json:"graph_attempt,omitempty"`
	ModelDecisions     []ModelDecision        `json:"model_decisions,omitempty"`
	RecordRefs         []RecordRef            `json:"record_refs,omitempty"`
	VerificationMethod string                 `json:"verification_method,omitempty"`
	ExecutionKind      string                 `json:"execution_kind,omitempty"`
	SessionID          string                 `json:"session_id,omitempty"`
	ProcessID          string                 `json:"process_id,omitempty"`
	ProjectID          string                 `json:"project_id,omitempty"`
	Condition          *ServiceConditionState `json:"condition,omitempty"`
	Health             *ServiceHealthState    `json:"health,omitempty"`
	Claim              *fleet.Claim           `json:"claim,omitempty"`
	ClaimConflicts     []fleet.Conflict       `json:"claim_conflicts,omitempty"`
	UnattendedUntil    *time.Time             `json:"unattended_until,omitempty"`
	PendingDecision    *HumanDecision         `json:"pending_decision,omitempty"`
	Tenant             decision.Tenant        `json:"tenant"`
	DecisionRewind     *autonomy.Rewind       `json:"decision_rewind,omitempty"`
	Execution          *ExecutionPlan         `json:"execution,omitempty"`
	Segments           []autonomy.Segment     `json:"segments,omitempty"`
	Checkpoints        []autonomy.Checkpoint  `json:"checkpoints,omitempty"`
	Rewinds            []autonomy.Rewind      `json:"rewinds,omitempty"`
	PendingRewind      *autonomy.Rewind       `json:"pending_rewind,omitempty"`
	ReadyRewind        *autonomy.Rewind       `json:"ready_rewind,omitempty"`
	Workspace          *WorkspaceRecord       `json:"workspace_record,omitempty"`
	Acceptance         *acceptance.Verdict    `json:"acceptance,omitempty"`
	ID                 string                 `json:"id"`
	TaskID             string                 `json:"task_id"`
	ParentID           string                 `json:"parent_id,omitempty"`
	Brief              Brief                  `json:"brief"`
	AppName            string                 `json:"app_name"`
	Status             string                 `json:"status"`
	Stage              string                 `json:"stage"`
	Summary            string                 `json:"summary"`
	CreatedAt          time.Time              `json:"created_at"`
	UpdatedAt          time.Time              `json:"updated_at"`
	FinishedAt         time.Time              `json:"finished_at,omitempty"`
	Verified           bool                   `json:"verified"`
	Sample             bool                   `json:"sample"`
	Artifacts          []Artifact             `json:"artifacts"`
	Decision           *HumanDecision         `json:"decision,omitempty"`
	Events             []Event                `json:"events"`
}

// RecordRef identifies a canonical shared journal event without copying its
// project-wide payload into each interactive attempt's read model.
type RecordRef struct {
	Seq  int64     `json:"seq"`
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
}

func (r Run) Active() bool {
	return r.Status == "running" || r.Status == "queued" || r.Status == "stopping"
}
func (r Run) Terminal() bool {
	return r.Status == "done" || r.Status == "failed" || r.Status == "cancelled"
}
func (r Run) Duration() time.Duration {
	end := r.FinishedAt
	if end.IsZero() {
		end = time.Now()
	}
	return max(0, end.Sub(r.CreatedAt))
}

type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type Option = decision.Option
type HumanDecision = decision.Card

// External decisions deliberately have no automatic default or timeout.
func recoveryDecision() *HumanDecision {
	return &HumanDecision{ID: newID("decision"), ReasonSignal: "execution_interrupted", InterruptClass: "rescue", Question: "Restart this interrupted work?", Kind: "choose", Context: decision.Context{BlockedBy: "The server stopped before execution finished. The previous workspace is preserved; its result has not been verified."}, CostOfWrong: decision.Cost{Detail: "Restarting repeats the work in a fresh copy. Any external effects from the previous attempt may already have happened."}, Reversibility: "external", Options: []Option{
		{ID: "retry", Label: "Restart in a fresh workspace", Consequence: "Create another attempt with the same brief and time limit."},
		{ID: "cancel", Label: "Close this work", Consequence: "Keep the record and do not execute another attempt.", Recommended: true},
	}}
}

// Event is the sole persistent source of run state. Hashes cover the exact
// encoded record (with Hash empty), including its predecessor.
type Event struct {
	Seq      int64           `json:"seq"`
	RunID    string          `json:"run_id"`
	Type     string          `json:"type"`
	Kind     string          `json:"kind"`
	At       time.Time       `json:"at"`
	Data     json.RawMessage `json:"data"`
	Previous string          `json:"previous"`
	Hash     string          `json:"hash"`
}

type change struct {
	SupervisorVerdict   *SupervisorVerdict         `json:"supervisor_verdict,omitempty"`
	SupervisionIncident *SupervisionIncident       `json:"supervision_incident,omitempty"`
	AgentRoute          *AgentRoute                `json:"agent_route,omitempty"`
	ModelEvaluation     *ModelEvaluation           `json:"model_evaluation,omitempty"`
	Cooperation         *CooperationRecord         `json:"cooperation,omitempty"`
	AgentPolicy         *ProjectAgentPolicy        `json:"agent_policy,omitempty"`
	Graph               *graphChange               `json:"graph,omitempty"`
	GraphAttempt        *GraphAttempt              `json:"graph_attempt,omitempty"`
	ModelDecision       *ModelDecision             `json:"model_decision,omitempty"`
	Message             *messageChange             `json:"message,omitempty"`
	DeskState           json.RawMessage            `json:"desk_state,omitempty"`
	Home                *HomeServer                `json:"home_server,omitempty"`
	AgentSession        *AgentSession              `json:"agent_session,omitempty"`
	AgentRequest        *AgentRequest              `json:"agent_request,omitempty"`
	AgentAnswer         *AgentAnswer               `json:"agent_answer,omitempty"`
	Device              *Device                    `json:"device,omitempty"`
	DeviceCommand       *DeviceCommandRecord       `json:"device_command,omitempty"`
	ServiceLifecycle    *ServiceLifecycleOperation `json:"service_lifecycle,omitempty"`
	CustomEvent         *CustomEventRecord         `json:"custom_event,omitempty"`
	ServiceCondition    *ServiceConditionState     `json:"service_condition,omitempty"`
	ServiceHealth       *ServiceHealthState        `json:"service_health,omitempty"`
	ServiceContinuous   *ServiceContinuous         `json:"service_continuous,omitempty"`
	ServiceEmission     *ServiceEmission           `json:"service_emission,omitempty"`
	ServiceDelivery     *ServiceEventDelivery      `json:"service_delivery,omitempty"`
	ServiceClocks       []ServiceClock             `json:"service_clocks,omitempty"`
	ServiceRestart      *ServiceRestartBinding     `json:"service_restart,omitempty"`
	ServiceRetry        *ServiceRetryPlan          `json:"service_retry,omitempty"`
	ServiceBreaker      *ServiceBreakerEffect      `json:"service_breaker,omitempty"`
	ServiceTick         *ServiceTick               `json:"service_tick,omitempty"`
	Services            *ServiceCatalog            `json:"services,omitempty"`
	ServiceCall         *ServiceCall               `json:"service_call,omitempty"`
	Task                *Task                      `json:"task,omitempty"`
	TaskEffect          string                     `json:"task_effect,omitempty"`
	Pool                *workpool.Pool             `json:"pool,omitempty"`
	Claim               *fleet.Claim               `json:"claim,omitempty"`
	ClaimConflicts      []fleet.Conflict           `json:"claim_conflicts,omitempty"`
	DecisionRequest     *HumanDecision             `json:"decision_request,omitempty"`
	DecisionError       string                     `json:"decision_error,omitempty"`
	Resolution          *decision.Resolution       `json:"resolution,omitempty"`
	ResolutionRewind    *autonomy.Rewind           `json:"resolution_rewind,omitempty"`
	DecisionApplied     bool                       `json:"decision_applied,omitempty"`
	ResultRunID         string                     `json:"result_run_id,omitempty"`
	Execution           *ExecutionPlan             `json:"execution,omitempty"`
	Segment             *autonomy.Segment          `json:"segment,omitempty"`
	SegmentFinished     bool                       `json:"segment_finished,omitempty"`
	Checkpoint          *autonomy.Checkpoint       `json:"checkpoint,omitempty"`
	Rewind              *autonomy.Rewind           `json:"rewind,omitempty"`
	RewindApplied       bool                       `json:"rewind_applied,omitempty"`
	Workspace           *WorkspaceRecord           `json:"workspace_record,omitempty"`
	Acceptance          *acceptance.Verdict        `json:"acceptance,omitempty"`
	Evidence            json.RawMessage            `json:"evidence,omitempty"`
	Run                 *Run                       `json:"run,omitempty"`
	Status              string                     `json:"status,omitempty"`
	Stage               string                     `json:"stage,omitempty"`
	Summary             string                     `json:"summary,omitempty"`
	Verified            bool                       `json:"verified,omitempty"`
	Artifact            *Artifact                  `json:"artifact,omitempty"`
	Decision            *HumanDecision             `json:"decision,omitempty"`
	Choice              string                     `json:"choice,omitempty"`
}

func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("random ID: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
