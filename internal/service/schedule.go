package service

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

type Schedule struct {
	Path         string        `json:"path,omitempty"`
	Source       string        `json:"source,omitempty"`
	RestartDelay *int          `json:"restart_delay,omitempty"`
	Trigger      EventPatterns `json:"trigger,omitempty"`
	Cron         string        `json:"cron,omitempty"`
	Timezone     string        `json:"timezone,omitempty"`
	Type         string        `json:"type"`
	Interval     int           `json:"interval"`
	Jitter       int           `json:"jitter,omitempty"`
	RunOnStartup bool          `json:"run_on_startup,omitempty"`
}
type Concurrency struct {
	Priority     string `json:"priority,omitempty"`
	MaxInstances int    `json:"max_instances,omitempty"`
	IfRunning    string `json:"if_running,omitempty"`
	QueueMax     int    `json:"queue_max,omitempty"`
}

func (c Concurrency) Effective() Concurrency {
	if c.Priority == "" {
		c.Priority = "normal"
	}
	if c.MaxInstances == 0 {
		c.MaxInstances = 1
	}
	if c.IfRunning == "" {
		c.IfRunning = "skip"
	}
	if c.QueueMax == 0 {
		c.QueueMax = 10
	}
	return c
}
func (u Unit) ValidateRuntime() error {
	if len(u.Events) > 0 {
		if _, err := u.EventPatterns(); err != nil {
			return err
		}
	}
	if u.Schedule != nil && u.TriggerKind() != "webhook" && u.TriggerKind() != "external_signal" && (u.Schedule.Path != "" || u.Schedule.Source != "") {
		return errors.New("path/source require an ingress trigger")
	}
	if u.Condition != nil {
		if err := u.Condition.Validate(); err != nil {
			return err
		}
	}
	if u.Health != nil {
		if err := u.Health.Validate(); err != nil {
			return err
		}
	}
	if err := u.RestartPolicy.Effective().Validate(); err != nil {
		return err
	}
	if err := u.CircuitBreaker.Validate(); err != nil {
		return err
	}
	if u.TriggerKind() != "event" && (u.Triggers != nil || u.Schedule != nil && len(u.Schedule.Trigger) > 0) {
		return errors.New("event hooks/patterns require an event service")
	}
	if u.TriggerKind() != "continuous" && u.Schedule != nil && u.Schedule.RestartDelay != nil {
		return errors.New("restart_delay requires a continuous schedule")
	}
	switch u.TriggerKind() {
	case "webhook", "external_signal":
		if err := u.validateIngress(); err != nil {
			return err
		}
	case "manual":
		if u.Schedule != nil {
			return errors.New("manual services cannot have an automatic schedule")
		}
	case "interval":
		if u.Schedule == nil || u.Schedule.Type != "interval" || u.Schedule.Interval < 1 || u.Schedule.Interval > 86400*365 || u.Schedule.Jitter < 0 || u.Schedule.Jitter > u.Schedule.Interval || u.Schedule.Cron != "" || u.Schedule.Timezone != "" {
			return errors.New("interval schedule requires 1–31536000 seconds and jitter between zero and interval")
		}
	case "cron":
		if u.Schedule == nil || u.Schedule.Type != "cron" || u.Schedule.Interval != 0 || u.Schedule.Jitter != 0 {
			return errors.New("cron requires a cron schedule without interval or jitter fields")
		}
		if _, err := CompileCron(u.Schedule.Cron, u.Schedule.Timezone); err != nil {
			return err
		}
	case "continuous":
		if u.Schedule == nil || u.Schedule.Type != "continuous" || u.Schedule.Interval != 0 || u.Schedule.Jitter != 0 || u.Schedule.Cron != "" || u.Schedule.Timezone != "" || u.Schedule.RunOnStartup || u.Schedule.RestartDelay != nil && (*u.Schedule.RestartDelay < 0 || *u.Schedule.RestartDelay > 31536000) {
			return errors.New("continuous requires only restart_delay, from 0 to 31536000 seconds")
		}
	case "event":
		if _, err := u.EventPatterns(); err != nil {
			return err
		}
	default:
		return errors.New("registered local service triggers are manual, interval, cron, event, continuous, webhook, and external_signal")
	}
	c := u.Concurrency.Effective()
	if _, ok := PriorityAdjustment(c.Priority); !ok {
		return errors.New("service priority must be low, normal, high, or critical")
	}
	if c.MaxInstances < 1 || c.MaxInstances > 32 || c.QueueMax < 1 || c.QueueMax > 1000 || (c.IfRunning != "skip" && c.IfRunning != "queue" && c.IfRunning != "kill") {
		return errors.New("service concurrency requires max_instances 1–32, queue_max 1–1000, and if_running skip, queue, or kill")
	}
	return nil
}

// Next coalesces missed intervals: schedule once from observation time, without
// replaying a backlog. Deterministic positive jitter survives event replay.
func (s Schedule) Next(now time.Time, id string, epoch int64) (time.Time, error) {
	if s.Type == "cron" {
		c, err := CompileCron(s.Cron, s.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		return c.Next(now)
	}
	if s.Type != "interval" {
		return time.Time{}, errors.New("schedule has no time clock")
	}
	jitter := int64(0)
	if s.Jitter > 0 {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", id, epoch, now.UnixNano())))
		jitter = int64(binary.BigEndian.Uint64(sum[:8]) % uint64(s.Jitter+1))
	}
	return now.Add(time.Duration(int64(s.Interval)+jitter) * time.Second).UTC(), nil
}

func (s Schedule) ContinuousDelay() time.Duration {
	if s.RestartDelay == nil {
		return 5 * time.Second
	}
	return time.Duration(*s.RestartDelay) * time.Second
}
