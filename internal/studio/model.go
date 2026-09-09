// Package studio owns the durable work record and local execution lifecycle.
package studio

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrConflict = errors.New("this work has already changed; refresh and try again")
var ErrNotFound = errors.New("work not found")

type Brief struct {
	Title          string `json:"title"`
	Intent         string `json:"intent"`
	Workspace      string `json:"workspace"`
	AppID          string `json:"app_id"`
	TimeoutMinutes int    `json:"timeout_minutes"`
}

func (b *Brief) Validate() error {
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
	if b.TimeoutMinutes < 1 || b.TimeoutMinutes > 120 {
		return errors.New("choose a time limit from 1 to 120 minutes")
	}
	return nil
}

type Run struct {
	ID         string         `json:"id"`
	TaskID     string         `json:"task_id"`
	ParentID   string         `json:"parent_id,omitempty"`
	Brief      Brief          `json:"brief"`
	AppName    string         `json:"app_name"`
	Status     string         `json:"status"`
	Stage      string         `json:"stage"`
	Summary    string         `json:"summary"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	FinishedAt time.Time      `json:"finished_at,omitempty"`
	Verified   bool           `json:"verified"`
	Sample     bool           `json:"sample"`
	Artifacts  []Artifact     `json:"artifacts"`
	Decision   *HumanDecision `json:"decision,omitempty"`
	Events     []Event        `json:"events"`
}

func (r Run) Active() bool { return r.Status == "running" || r.Status == "queued" }
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

type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
	Recommended bool   `json:"recommended,omitempty"`
}

type HumanDecision struct {
	ID            string     `json:"decision_id"`
	Question      string     `json:"question"`
	Context       string     `json:"context"`
	CostOfWrong   string     `json:"cost_of_wrong"`
	Reversibility string     `json:"reversibility"`
	Options       []Option   `json:"options"`
	Choice        string     `json:"choice,omitempty"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
}

// External decisions deliberately have no automatic default or timeout.
func recoveryDecision() *HumanDecision {
	return &HumanDecision{ID: newID("decision"), Question: "Restart this interrupted work?", Context: "The server stopped before execution finished. The previous workspace is preserved; its result has not been verified.", CostOfWrong: "Restarting repeats the work in a fresh copy. Any external effects from the previous attempt may already have happened.", Reversibility: "external", Options: []Option{
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
	Run      *Run           `json:"run,omitempty"`
	Status   string         `json:"status,omitempty"`
	Stage    string         `json:"stage,omitempty"`
	Summary  string         `json:"summary,omitempty"`
	Verified bool           `json:"verified,omitempty"`
	Artifact *Artifact      `json:"artifact,omitempty"`
	Decision *HumanDecision `json:"decision,omitempty"`
	Choice   string         `json:"choice,omitempty"`
}

func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("random ID: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
