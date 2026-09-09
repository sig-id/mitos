// Package autonomy contains replayable checkpoint and restart policy data.
package autonomy

import (
	"errors"
	"math"
	"strings"
	"time"

	"mitos/internal/acceptance"
)

type Supervision struct {
	MaxRestarts *int   `json:"max_restarts,omitempty"`
	MaxSeconds  int    `json:"max_seconds,omitempty"`
	Strategy    string `json:"strategy"`
}

func (s Supervision) Limit() int {
	if s.MaxRestarts == nil {
		return 3
	}
	return *s.MaxRestarts
}
func (s Supervision) Window() time.Duration {
	if s.MaxSeconds == 0 {
		return time.Hour
	}
	return time.Duration(s.MaxSeconds) * time.Second
}
func (s Supervision) Validate() error {
	if s.Limit() < 0 || s.Limit() > 100 || s.MaxSeconds < 0 || s.MaxSeconds > 86400 {
		return errors.New("supervision requires 0–100 restarts and a window of 1–86400 seconds")
	}
	switch s.Strategy {
	case "retry_step", "rewind_to_green", "rewind_tail", "escalate":
		return nil
	default:
		return errors.New("unsupported supervision strategy")
	}
}

type Segment struct {
	ID          string     `json:"segment_id"`
	StepID      string     `json:"step_id"`
	Number      int        `json:"number"`
	InputCommit string     `json:"input_commit"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	Signal      string     `json:"signal,omitempty"`
	Invalidated bool       `json:"invalidated,omitempty"`
}

type Checkpoint struct {
	ID              string             `json:"checkpoint_id"`
	RunID           string             `json:"run_id"`
	Seq             int64              `json:"seq"`
	QuietSeq        int64              `json:"quiescent_seq"`
	StepID          string             `json:"step_id"`
	SegmentID       string             `json:"segment_id,omitempty"`
	SegmentNumber   int                `json:"segment_number"`
	WorkspaceCommit string             `json:"workspace_commit"`
	Fingerprint     string             `json:"fingerprint"`
	SandboxSnapshot string             `json:"sandbox_snapshot"`
	Predicate       acceptance.Verdict `json:"predicate"`
	Green           bool               `json:"green"`
	Invalidated     bool               `json:"invalidated,omitempty"`
}

type Rewind struct {
	ID            string     `json:"rewind_id"`
	CheckpointID  string     `json:"checkpoint_id"`
	Scope         string     `json:"scope"`
	Memo          string     `json:"memo"`
	ReasonSignal  string     `json:"reason_signal"`
	AllowNonGreen bool       `json:"allow_non_green,omitempty"`
	StepID        string     `json:"step_id"`
	FromSegment   int        `json:"from_segment"`
	NextSegment   int        `json:"next_segment"`
	CausedBy      int64      `json:"caused_by"`
	RequestedAt   time.Time  `json:"requested_at"`
	AppliedAt     *time.Time `json:"applied_at,omitempty"`
}

func (r Rewind) Validate() error {
	if r.Scope != "step" && r.Scope != "tail" {
		return errors.New("rewind scope must be step or tail")
	}
	if strings.TrimSpace(r.Memo) == "" || len(r.Memo) > 10000 || strings.TrimSpace(r.ReasonSignal) == "" {
		return errors.New("rewind requires a failure memo and reason_signal")
	}
	return nil
}

func Intensity(s Supervision, restarts []Rewind, step string, at time.Time) (runCount, stepCount int, exhausted bool) {
	for _, restart := range restarts {
		if !restart.RequestedAt.After(at) && at.Sub(restart.RequestedAt) > s.Window() {
			continue
		}
		runCount++
		if restart.StepID == step {
			stepCount++
		}
	}
	return runCount, stepCount, runCount >= s.Limit() || stepCount >= s.Limit()
}

// PredicateStall uses numeric predicate changes, never diff volume or narration.
// Only consecutive completed segment checkpoints with compatible evidence count.
func PredicateStall(checkpoints []Checkpoint, step string) bool {
	count := 0
	measured := false
	verdict := ""
	for i := len(checkpoints) - 1; i >= 0; i-- {
		c := checkpoints[i]
		if c.StepID != step || c.SegmentNumber == 0 {
			continue
		}
		hasMeasure := len(c.Predicate.Delta) > 0
		if count == 0 {
			measured = hasMeasure
			verdict = c.Predicate.Verdict
		}
		if hasMeasure != measured {
			return false
		}
		if measured {
			for _, delta := range c.Predicate.Delta {
				if math.IsNaN(delta) || math.IsInf(delta, 0) || math.Abs(delta) > 1e-9 {
					return false
				}
			}
		} else {
			// A machine-checkable binary verdict is still a predicate when the optional
			// numeric measure is absent. Three identical known verdicts show a plateau.
			if verdict != "pass" && verdict != "fail" {
				return false
			}
			if c.Predicate.Verdict != verdict {
				return false
			}
		}
		count++
		if count == 3 {
			return true
		}
	}
	return false
}
