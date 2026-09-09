package service

import (
	"errors"
	"math"
	"time"
)

type Backoff struct {
	Initial    int     `json:"initial,omitempty"`
	Multiplier float64 `json:"multiplier,omitempty"`
	Max        int     `json:"max,omitempty"`
}
type RestartPolicy struct {
	OnFailure  string  `json:"on_failure,omitempty"`
	MaxRetries *int    `json:"max_retries,omitempty"`
	Backoff    Backoff `json:"backoff,omitempty"`
}
type RestartRules struct {
	OnFailure  string  `json:"on_failure"`
	MaxRetries int     `json:"max_retries"`
	Backoff    Backoff `json:"backoff"`
}

func (p RestartPolicy) Effective() RestartRules {
	r := RestartRules{OnFailure: p.OnFailure, MaxRetries: 2, Backoff: p.Backoff}
	if r.OnFailure == "" {
		r.OnFailure = "skip"
	}
	if p.MaxRetries != nil {
		r.MaxRetries = *p.MaxRetries
	}
	if r.Backoff.Initial == 0 {
		r.Backoff.Initial = 5
	}
	if r.Backoff.Multiplier == 0 {
		r.Backoff.Multiplier = 2
	}
	if r.Backoff.Max == 0 {
		r.Backoff.Max = 300
	}
	return r
}
func (r RestartRules) Validate() error {
	if r.OnFailure != "skip" && r.OnFailure != "retry" && r.OnFailure != "abort" {
		return errors.New("service on_failure must be retry, skip, or abort")
	}
	if r.MaxRetries < 0 || r.MaxRetries > 32 || r.Backoff.Initial < 1 || r.Backoff.Initial > 86400 || r.Backoff.Max < r.Backoff.Initial || r.Backoff.Max > 86400*365 || math.IsNaN(r.Backoff.Multiplier) || math.IsInf(r.Backoff.Multiplier, 0) || r.Backoff.Multiplier < 1 || r.Backoff.Multiplier > 10 {
		return errors.New("restart policy requires 0–32 retries, initial backoff 1–86400 seconds, multiplier 1–10, and max between initial and 31536000 seconds")
	}
	return nil
}

// retry is one-based; the first retry waits the initial delay.
func (r RestartRules) Delay(retry int) time.Duration {
	seconds := math.Min(float64(r.Backoff.Max), float64(r.Backoff.Initial)*math.Pow(r.Backoff.Multiplier, float64(max(retry-1, 0))))
	return time.Duration(seconds * float64(time.Second))
}
