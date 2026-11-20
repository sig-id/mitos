// Package watch defines mitos.watch/v1 records and the pure rules the runtime
// uses to match, group and grade journal events for an owner's attention. It
// performs no I/O and holds no clock; callers supply event facts and times, so
// the same journal always produces the same groups during replay.
package watch

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

const Version = "mitos.watch/v1"

// Level is a delivery level. Levels are totally ordered and none discards events.
type Level string

const (
	Digest   Level = "digest"
	Notify   Level = "notify"
	Wake     Level = "wake"
	Escalate Level = "escalate"
)

func (l Level) Rank() int {
	switch l {
	case Digest:
		return 1
	case Notify:
		return 2
	case Wake:
		return 3
	case Escalate:
		return 4
	}
	return 0
}
func (l Level) Valid() bool { return l.Rank() > 0 }

// Max returns the more urgent of two levels; an invalid level never wins.
func Max(a, b Level) Level {
	if b.Rank() > a.Rank() {
		return b
	}
	return a
}

// Owner receives deliveries: the operator, a responsibility (an adaptive
// graph's supervisory responsibility, addressed by the graph ID like its
// mailbox), or one attempt (a run). Watches never belong to a session.
type Owner struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

const (
	OwnerOperator       = "operator"
	OwnerResponsibility = "responsibility"
	OwnerAttempt        = "attempt"
)

func (o Owner) Key() string { return o.Kind + ":" + o.ID }

func (o Owner) validate() error {
	switch o.Kind {
	case OwnerOperator:
		if o.ID != "" {
			return errors.New("the operator owner takes no id")
		}
	case OwnerResponsibility, OwnerAttempt:
		if !idPattern.MatchString(o.ID) {
			return errors.New("responsibility and attempt owners need a valid id")
		}
	default:
		return errors.New("owner.kind must be operator, responsibility or attempt")
	}
	return nil
}

// MaxActive bounds an owner's active watches.
func (o Owner) MaxActive() int {
	if o.Kind == OwnerOperator {
		return 256
	}
	return 32
}

type Scope struct {
	Project string `json:"project,omitempty"`
	Graph   string `json:"graph,omitempty"`
	Task    string `json:"task,omitempty"`
	Run     string `json:"run,omitempty"`
}

func (s Scope) empty() bool { return s.Project == "" && s.Graph == "" && s.Task == "" && s.Run == "" }

// Within reports whether s is no wider than grant: every field the grant sets
// is set to the same value in s.
func (s Scope) Within(grant Scope) bool {
	return (grant.Project == "" || s.Project == grant.Project) && (grant.Graph == "" || s.Graph == grant.Graph) &&
		(grant.Task == "" || s.Task == grant.Task) && (grant.Run == "" || s.Run == grant.Run)
}

// Condition is a closed filter. There are no expressions or text matches.
type Condition struct {
	Scope Scope    `json:"scope"`
	Types []string `json:"types,omitempty"`
	Kinds []string `json:"kinds,omitempty"`
}

type Lease struct {
	ExpiresAt   time.Time `json:"expires_at"`
	MaxRenewals int       `json:"max_renewals"`
}

type Grouping struct {
	By               []string `json:"by"`
	WaitMS           int64    `json:"wait_ms"`
	IntervalMS       int64    `json:"interval_ms"`
	MaxEvents        int      `json:"max_events"`
	RepeatIntervalMS int64    `json:"repeat_interval_ms"`
}

type Spec struct {
	Version      string    `json:"version"`
	Owner        Owner     `json:"owner"`
	Description  string    `json:"description"`
	Condition    Condition `json:"condition"`
	Lease        Lease     `json:"lease"`
	Grouping     Grouping  `json:"grouping"`
	MaxSilenceMS int64     `json:"max_silence_ms,omitempty"`
	DefaultLevel Level     `json:"default_level"`
	// Triage is accepted from W3. Its presence is refused rather than ignored.
	Triage any `json:"triage,omitempty"`
}

// MaxLease bounds a lease from creation or renewal.
const MaxLease = 24 * time.Hour

var (
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	eventTypes  = []string{"effect", "decision"}
	groupFields = []string{"run_id", "task_id", "kind"}
)

// Validate checks a watch submitted at now. Unknown versions are refused.
func (s Spec) Validate(now time.Time) error {
	if s.Version != Version {
		return fmt.Errorf("watch version must be %s", Version)
	}
	if err := s.Owner.validate(); err != nil {
		return err
	}
	if s.Description == "" || len(s.Description) > 200 {
		return errors.New("description must be 1–200 bytes")
	}
	if s.Triage != nil {
		return errors.New("triage is not available yet; omit triage")
	}
	if err := s.Condition.validate(); err != nil {
		return err
	}
	if err := ValidateExpiry(s.Lease.ExpiresAt, now); err != nil {
		return err
	}
	if s.Lease.MaxRenewals < 1 || s.Lease.MaxRenewals > 1024 {
		return errors.New("lease.max_renewals must be 1–1024")
	}
	if err := s.Grouping.validate(); err != nil {
		return err
	}
	if s.MaxSilenceMS != 0 && (s.MaxSilenceMS < 60000 || s.MaxSilenceMS > 86400000) {
		return errors.New("max_silence_ms must be 0 or 60000–86400000")
	}
	if !s.DefaultLevel.Valid() {
		return errors.New("default_level must be digest, notify, wake or escalate")
	}
	return nil
}

// ValidateExpiry bounds an absolute lease expiry for creation or renewal at now.
func ValidateExpiry(at, now time.Time) error {
	if !at.After(now) || at.After(now.Add(MaxLease)) {
		return errors.New("lease.expires_at must be in the future and at most 24 hours away")
	}
	return nil
}

func (c Condition) validate() error {
	if c.Scope.empty() {
		return errors.New("condition.scope needs at least one of project, graph, task or run")
	}
	for _, id := range []string{c.Scope.Project, c.Scope.Graph, c.Scope.Task, c.Scope.Run} {
		if id != "" && !idPattern.MatchString(id) {
			return errors.New("condition.scope contains an invalid identifier")
		}
	}
	if len(c.Types) > len(eventTypes) || len(c.Kinds) > 32 {
		return errors.New("condition lists too many types or kinds")
	}
	for i, t := range c.Types {
		if !slices.Contains(eventTypes, t) || slices.Index(c.Types, t) != i {
			return errors.New("condition.types must be distinct values of effect or decision")
		}
	}
	for i, k := range c.Kinds {
		if !kindPattern.MatchString(k) || slices.Index(c.Kinds, k) != i {
			return errors.New("condition.kinds must be distinct lowercase event kinds")
		}
	}
	return nil
}

func (g Grouping) validate() error {
	if len(g.By) > 3 {
		return errors.New("grouping.by takes at most three fields")
	}
	for i, f := range g.By {
		if !slices.Contains(groupFields, f) || slices.Index(g.By, f) != i {
			return errors.New("grouping.by must be distinct values of run_id, task_id or kind")
		}
	}
	switch {
	case g.WaitMS < 0 || g.WaitMS > 60000:
		return errors.New("grouping.wait_ms must be 0–60000")
	case g.IntervalMS < 1000 || g.IntervalMS > 3600000:
		return errors.New("grouping.interval_ms must be 1000–3600000")
	case g.MaxEvents < 1 || g.MaxEvents > 256:
		return errors.New("grouping.max_events must be 1–256")
	case g.RepeatIntervalMS < 60000 || g.RepeatIntervalMS > 86400000:
		return errors.New("grouping.repeat_interval_ms must be 60000–86400000")
	}
	return nil
}

// Fact is the runtime's projection of one journal event. Rule is the
// must-deliver level the runtime assigned, or empty.
type Fact struct {
	Seq     int64     `json:"seq"`
	At      time.Time `json:"at"`
	Project string    `json:"project,omitempty"`
	Graph   string    `json:"graph,omitempty"`
	Task    string    `json:"task,omitempty"`
	Run     string    `json:"run,omitempty"`
	Type    string    `json:"type"`
	Kind    string    `json:"kind"`
	Rule    Level     `json:"rule,omitempty"`
	Reason  string    `json:"reason,omitempty"`
}

// InScope reports whether every set scope field matches the fact.
func (c Condition) InScope(f Fact) bool {
	s := c.Scope
	return (s.Project == "" || s.Project == f.Project) && (s.Graph == "" || s.Graph == f.Graph) &&
		(s.Task == "" || s.Task == f.Task) && (s.Run == "" || s.Run == f.Run)
}

// Matches applies the author's filter. Must-deliver facts bypass it; see Admit.
func (c Condition) Matches(f Fact) bool {
	return c.InScope(f) && (len(c.Types) == 0 || slices.Contains(c.Types, f.Type)) &&
		(len(c.Kinds) == 0 || slices.Contains(c.Kinds, f.Kind))
}

// Admit returns the level at which a watch takes a fact, or false. A
// must-deliver fact in scope is admitted whatever the author's condition, at
// its rule level or the watch default if that is higher.
func (s Spec) Admit(f Fact) (Level, bool) {
	if f.Rule.Valid() && s.Condition.InScope(f) {
		return Max(f.Rule, s.DefaultLevel), true
	}
	if s.Condition.Matches(f) {
		return s.DefaultLevel, true
	}
	return "", false
}

// GroupKey derives the group a fact joins.
func (g Grouping) GroupKey(f Fact) string {
	key := ""
	for _, field := range g.By {
		switch field {
		case "run_id":
			key += "run=" + f.Run + ";"
		case "task_id":
			key += "task=" + f.Task + ";"
		case "kind":
			key += "kind=" + f.Kind + ";"
		}
	}
	return key
}

// Entry is one admitted fact waiting in a group.
type Entry struct {
	Seq    int64     `json:"seq"`
	At     time.Time `json:"at"`
	Run    string    `json:"run,omitempty"`
	Kind   string    `json:"kind"`
	Level  Level     `json:"level"`
	Reason string    `json:"reason,omitempty"`
}

// Group holds admitted facts for one group key until they are delivered.
type Group struct {
	Key     string  `json:"key"`
	Entries []Entry `json:"entries"`
}

// Due reports whether a group should be delivered at now. A group is first
// delivered wait_ms after its oldest entry, then at most once per interval_ms
// while events continue; wait_ms never postpones a continuing group past
// interval_ms (spec 09: no starvation). A full group, or one holding a
// wake/escalate rule, is due immediately.
func (g Group) Due(now, lastDelivered time.Time, p Grouping) bool {
	if len(g.Entries) == 0 {
		return false
	}
	if len(g.Entries) >= p.MaxEvents {
		return true
	}
	for _, e := range g.Entries {
		if e.Reason != "" && e.Level.Rank() >= Wake.Rank() {
			return true
		}
	}
	oldest := g.Entries[0].At
	if lastDelivered.IsZero() {
		return !now.Before(oldest.Add(time.Duration(p.WaitMS) * time.Millisecond))
	}
	return !now.Before(lastDelivered.Add(time.Duration(p.IntervalMS) * time.Millisecond))
}
