package fleet

import "time"

// Evidence deliberately has no raw event payload, narration, rationale, command
// output, brief, or decision-card text. Adding a field requires a trust review.
// This schema is an input boundary, not a same-user process isolation boundary.
type Evidence struct {
	Schema      string            `json:"schema"`
	Run         Summary           `json:"run"`
	Digest      string            `json:"digest"`
	Workspace   *Workspace        `json:"workspace,omitempty"`
	Predicate   *Predicate        `json:"predicate,omitempty"`
	Segments    []Segment         `json:"segments"`
	Checkpoints []Checkpoint      `json:"checkpoints"`
	Controls    []Control         `json:"controls"`
	Artifacts   []ArtifactRef     `json:"artifacts"`
	Windows     map[string]Window `json:"windows"`
}
type Summary struct {
	TaskID        string   `json:"task_id"`
	Pool          string   `json:"pool"`
	RunID         string   `json:"run_id"`
	CapabilityRef string   `json:"capability_ref"`
	Status        string   `json:"status"`
	Sample        bool     `json:"sample"`
	Verified      bool     `json:"verified"`
	Head          Head     `json:"head"`
	ClaimID       string   `json:"claim_id,omitempty"`
	ClaimHeld     bool     `json:"claim_held"`
	BlockedBy     []string `json:"blocked_by,omitempty"`
	BlockerWindow Window   `json:"blocker_window"`
}
type Head struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}
type Window struct {
	Total   int `json:"total"`
	Omitted int `json:"omitted"`
}
type Index struct {
	Schema string    `json:"schema"`
	Runs   []Summary `json:"runs"`
	Window Window    `json:"window"`
}
type Workspace struct {
	BaselineCommit string `json:"baseline_commit"`
	ResultCommit   string `json:"result_commit"`
	Fingerprint    string `json:"fingerprint"`
	ChangedFiles   int    `json:"changed_files"`
}
type Predicate struct {
	Verdict         string       `json:"verdict"`
	WorkspaceCommit string       `json:"workspace_commit"`
	Evidence        *ArtifactRef `json:"evidence,omitempty"`
	BaselineMeasure []float64    `json:"baseline_measure,omitempty"`
	Measure         []float64    `json:"measure,omitempty"`
	Delta           []float64    `json:"delta,omitempty"`
	OracleIsolation string       `json:"oracle_isolation"`
}
type Segment struct {
	ID          string     `json:"segment_id"`
	Number      int        `json:"number"`
	InputCommit string     `json:"input_commit"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Invalidated bool       `json:"invalidated"`
}
type Checkpoint struct {
	ID              string    `json:"checkpoint_id"`
	Seq             int64     `json:"seq"`
	QuietSeq        int64     `json:"quiescent_seq"`
	SegmentNumber   int       `json:"segment_number"`
	WorkspaceCommit string    `json:"workspace_commit"`
	Fingerprint     string    `json:"fingerprint"`
	Predicate       Predicate `json:"predicate"`
	Green           bool      `json:"green"`
	Invalidated     bool      `json:"invalidated"`
}

// The legacy event schema has no authenticated actor; do not invent one here.
type Control struct {
	Seq  int64     `json:"seq"`
	Type string    `json:"type"`
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
}
type ArtifactRef struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}
