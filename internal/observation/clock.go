// Package observation contains evidence readers and the shared quiescence clock.
// It does not infer progress or authority from terminal text.
package observation

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Thresholds are durations rather than wall-clock deadlines, so the same
// samples produce the same edges during replay.
type Thresholds struct{ Snap, Steer, Block time.Duration }

func (t Thresholds) Validate() error {
	if t.Snap <= 0 || t.Snap >= t.Steer || t.Steer >= t.Block {
		return errors.New("observation requires 0 < snap < steer < block")
	}
	return nil
}

// FromThinkTime uses a measured harness p95, with a two-times margin and the
// specification's 45-second floor. No unattended grant follows from this value.
func FromThinkTime(p95 time.Duration) (Thresholds, error) {
	if p95 <= 0 || p95 > 24*time.Hour {
		return Thresholds{}, errors.New("think-time p95 must be measured and between zero and 24 hours")
	}
	return Thresholds{200 * time.Millisecond, 750 * time.Millisecond, max(45*time.Second, 2*p95)}, nil
}

type Sample struct {
	PTYOffset       uint64 `json:"pty_offset"`
	OutputOffset    uint64 `json:"output_offset,omitempty"`
	WorkspaceWrites uint64 `json:"workspace_writes"`
	// Execs are hashes of exact argv bytes, never command text or credentials.
	Execs    []string `json:"execs,omitempty"`
	TreeSize int      `json:"tree_size"`
}

func ArgvDigest(argv []byte) string { sum := sha256.Sum256(argv); return hex.EncodeToString(sum[:]) }

type Edge struct {
	Threshold            string `json:"threshold"`
	ForMS                int64  `json:"for_ms"`
	PTYOffset            uint64 `json:"pty_offset"`
	OutputOffset         uint64 `json:"output_offset,omitempty"`
	WorkspaceWritesSince uint64 `json:"workspace_writes_since"`
	DistinctExecsSince   uint64 `json:"distinct_execs_since"`
	TreeSize             int    `json:"tree_size"`
}

type Clock struct {
	thresholds     Thresholds
	last, activity time.Time
	previous       Sample
	seen           map[string]time.Time
	emitted        int
	writes, execs  uint64
}

func NewClock(start time.Time, t Thresholds) (*Clock, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &Clock{thresholds: t, last: start, activity: start, seen: map[string]time.Time{}}, nil
}

// Observe emits each threshold once per quiet period. Repeated argv refreshes
// membership in the rolling block window but does not reset activity; a polling
// process cannot keep the clock alive by executing the same command forever.
func (c *Clock) Observe(at time.Time, s Sample) ([]Edge, error) {
	if at.Before(c.last) || s.PTYOffset < c.previous.PTYOffset || s.OutputOffset < c.previous.OutputOffset || s.WorkspaceWrites < c.previous.WorkspaceWrites || s.TreeSize < 0 {
		return nil, errors.New("observation time or counters moved backwards")
	}
	active := s.PTYOffset != c.previous.PTYOffset || s.OutputOffset != c.previous.OutputOffset || s.WorkspaceWrites != c.previous.WorkspaceWrites
	c.writes += s.WorkspaceWrites - c.previous.WorkspaceWrites
	for argv, last := range c.seen {
		if at.Sub(last) > c.thresholds.Block {
			delete(c.seen, argv)
		}
	}
	for _, argv := range s.Execs {
		if _, ok := c.seen[argv]; !ok {
			active = true
			c.execs++
		}
		c.seen[argv] = at
	}
	if active {
		c.activity = at
		c.emitted = 0
	}
	c.previous, c.last = s, at
	quiet := at.Sub(c.activity)
	var edges []Edge
	thresholds := []time.Duration{c.thresholds.Snap, c.thresholds.Steer, c.thresholds.Block}
	names := []string{"snap", "steer", "block"}
	for c.emitted < len(thresholds) && quiet >= thresholds[c.emitted] {
		edges = append(edges, Edge{names[c.emitted], quiet.Milliseconds(), s.PTYOffset, s.OutputOffset, c.writes, c.execs, s.TreeSize})
		c.emitted++
		c.writes, c.execs = 0, 0
	}
	return edges, nil
}
