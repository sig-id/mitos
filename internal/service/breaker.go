package service

import (
	"errors"
	"time"
)

type BreakerConfig struct {
	Enabled          bool `json:"enabled"`
	Threshold        int  `json:"threshold,omitempty"`
	Cooldown         int  `json:"cooldown,omitempty"`
	HalfOpenRequests int  `json:"half_open_requests,omitempty"`
}

func (c BreakerConfig) Effective() BreakerConfig {
	if c.Threshold == 0 {
		c.Threshold = 5
	}
	if c.Cooldown == 0 {
		c.Cooldown = 300
	}
	if c.HalfOpenRequests == 0 {
		c.HalfOpenRequests = 1
	}
	return c
}
func (c BreakerConfig) Validate() error {
	c = c.Effective()
	if c.Threshold < 1 || c.Threshold > 1000 || c.Cooldown < 1 || c.Cooldown > 86400*365 || c.HalfOpenRequests < 1 || c.HalfOpenRequests > 32 {
		return errors.New("circuit breaker requires threshold 1–1000, cooldown 1–31536000 seconds, and half_open_requests 1–32")
	}
	return nil
}

type BreakerBinding struct {
	Epoch      int64 `json:"epoch"`
	Generation int64 `json:"generation"`
	Probe      bool  `json:"probe"`
}
type Circuit struct {
	Config     BreakerConfig `json:"config"`
	Epoch      int64         `json:"epoch"`
	Generation int64         `json:"generation"`
	State      string        `json:"state"`
	Failures   int           `json:"consecutive_failures"`
	OpenUntil  *time.Time    `json:"open_until,omitempty"`
	Issued     int           `json:"probes_issued"`
	Succeeded  int           `json:"probes_succeeded"`
}

func NewCircuit(c BreakerConfig, epoch int64) Circuit {
	return Circuit{Config: c.Effective(), Epoch: epoch, Generation: 1, State: "closed"}
}
func (c Circuit) Available(now time.Time) bool {
	switch c.State {
	case "closed":
		return true
	case "open":
		return c.OpenUntil != nil && !now.Before(*c.OpenUntil)
	case "half-open":
		return c.Issued < c.Config.HalfOpenRequests
	}
	return false
}
func (c Circuit) Admit(now time.Time) (Circuit, BreakerBinding, error) {
	if !c.Available(now) {
		return c, BreakerBinding{}, errors.New("service circuit breaker is withholding new execution")
	}
	if c.State == "open" {
		c.State = "half-open"
		c.OpenUntil = nil
	}
	binding := BreakerBinding{Epoch: c.Epoch, Generation: c.Generation, Probe: c.State == "half-open"}
	if binding.Probe {
		c.Issued++
	}
	return c, binding, nil
}
func (c Circuit) open(now time.Time) Circuit {
	c.State = "open"
	c.Generation++
	c.Issued = 0
	c.Succeeded = 0
	until := now.Add(time.Duration(c.Config.Cooldown) * time.Second).UTC()
	c.OpenUntil = &until
	return c
}

// Only an outcome from this policy epoch and recovery generation can affect it.
// This prevents late successes from in-flight work closing a newly opened circuit.
func (c Circuit) Outcome(b BreakerBinding, status string, now time.Time) (Circuit, bool) {
	if b.Epoch != c.Epoch || b.Generation != c.Generation {
		return c, false
	}
	if b.Probe {
		if c.State != "half-open" {
			return c, false
		}
		switch status {
		case "failed":
			c.Failures++
			return c.open(now), true
		case "cancelled":
			c.Issued--
			return c, true
		case "done":
			c.Succeeded++
			if c.Succeeded == c.Config.HalfOpenRequests {
				c.State = "closed"
				c.Generation++
				c.Failures = 0
				c.Issued = 0
				c.Succeeded = 0
			}
			return c, true
		}
	} else if c.State == "closed" {
		switch status {
		case "failed":
			c.Failures++
			if c.Failures >= c.Config.Threshold {
				c = c.open(now)
			}
			return c, true
		case "done":
			c.Failures = 0
			return c, true
		case "cancelled":
			return c, true
		}
	}
	return c, false
}
func (c Circuit) Validate() error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	if !c.Config.Enabled || c.Epoch < 1 || c.Generation < 1 || c.Failures < 0 || c.Issued < 0 || c.Succeeded < 0 || c.Succeeded > c.Issued || c.Issued > c.Config.HalfOpenRequests {
		return errors.New("invalid circuit state")
	}
	switch c.State {
	case "closed":
		if c.OpenUntil != nil || c.Issued != 0 || c.Succeeded != 0 {
			return errors.New("invalid closed circuit")
		}
	case "open":
		if c.OpenUntil == nil || c.OpenUntil.IsZero() || c.Issued != 0 || c.Succeeded != 0 {
			return errors.New("invalid open circuit")
		}
	case "half-open":
		if c.OpenUntil != nil {
			return errors.New("invalid half-open circuit")
		}
	default:
		return errors.New("unknown circuit state")
	}
	return nil
}
