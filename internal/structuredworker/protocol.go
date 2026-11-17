// Package structuredworker owns the transport-independent headless worker protocol.
// It has no dependency on the server's store or application handlers.
package structuredworker

import (
	"encoding/json"
	"mitos/agents"
	"time"
)

const Version = "1"
const VersionHeader = "Mitos-Worker-Version"

type Identity struct {
	ID          string `json:"id"`
	Incarnation string `json:"incarnation"`
}
type Register struct {
	RequestID    string   `json:"request_id"`
	Incarnation  string   `json:"incarnation"`
	Capabilities []string `json:"capabilities"`
}
type Claim struct {
	Identity
	RequestID string `json:"request_id"`
	RunID     string `json:"run_id,omitempty"`
}
type Binding struct {
	InputCursor    int64  `json:"input_cursor"`
	InputLogDigest string `json:"input_log_digest"`
	Identity
	TaskID           string `json:"task_id"`
	RunID            string `json:"run_id"`
	Generation       uint64 `json:"generation"`
	Epoch            string `json:"epoch"`
	InputDigest      string `json:"input_digest"`
	DefinitionDigest string `json:"definition_digest"`
	SchemaDigest     string `json:"schema_digest"`
	Acceptance       string `json:"acceptance"`
	LeaseSeconds     int    `json:"lease_seconds"`
	BudgetSeconds    int    `json:"budget_seconds"`
}
type Mutation struct {
	Binding
	RequestID string          `json:"request_id"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data,omitempty"`
}
type Receipt struct {
	OperationID   string          `json:"operation_id,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	WakeAuthority string          `json:"wake_authority,omitempty"`
	RequestID     string          `json:"request_id"`
	Seq           int64           `json:"seq"`
	State         string          `json:"state"`
	SHA256        string          `json:"sha256,omitempty"`
	LeaseSeconds  int             `json:"lease_seconds,omitempty"`
	Binding       *Binding        `json:"binding,omitempty"`
}
type Context struct {
	CheckpointSchema json.RawMessage            `json:"checkpoint_schema,omitempty"`
	Checkpoint       json.RawMessage            `json:"checkpoint,omitempty"`
	Inputs           InputPage                  `json:"inputs"`
	Actions          map[string]json.RawMessage `json:"actions,omitempty"`
	// PendingActions lists operations this Task still holds from earlier
	// attempts, with their original worker keys, so a replacement attempt can
	// reconcile them. Credentials and endpoints are never included.
	PendingActions []PendingAction `json:"pending_actions,omitempty"`
	Binding
	Definition   agents.Definition          `json:"definition"`
	Prompt       string                     `json:"prompt"`
	ResultSchema json.RawMessage            `json:"result_schema"`
	Model        string                     `json:"model,omitempty"`
	MaxTurns     int                        `json:"max_turns"`
	Artifacts    map[string]json.RawMessage `json:"artifacts"`
}
type PendingAction struct {
	OperationID string `json:"operation_id"`
	Target      string `json:"target"`
	Key         string `json:"key"`
	State       string `json:"state"`
}
type Artifact struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}
type Result struct {
	Value     json.RawMessage `json:"value"`
	Artifacts []Artifact      `json:"artifacts,omitempty"`
}

// LeaseRecord is server state. Deadlines never cross the worker boundary.
type LeaseRecord struct {
	Binding
	ExpiresAt    time.Time `json:"expires_at"`
	BudgetEndsAt time.Time `json:"budget_ends_at"`
	Closed       bool      `json:"closed,omitempty"`
}

type ActionRequest struct {
	Target  string          `json:"target"`
	Key     string          `json:"key"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Input struct {
	Seq    int64           `json:"seq"`
	Key    string          `json:"key"`
	Data   json.RawMessage `json:"data"`
	SHA256 string          `json:"sha256"`
}
type InputPage struct {
	Inputs  []Input `json:"inputs"`
	Next    int64   `json:"next"`
	HasMore bool    `json:"has_more"`
}
type WaitRequest struct {
	Cursor     int64           `json:"cursor"`
	Checkpoint json.RawMessage `json:"checkpoint"`
}

type Resume struct {
	Checkpoint json.RawMessage
	Inputs     InputPage
}
