// Package workpool owns deterministic ready-set and scheduling policy.
package workpool

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const Capacity = 2

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Pool struct {
	ID          string `json:"id"`
	Discipline  string `json:"discipline"`
	Concurrency int    `json:"concurrency"`
	Reserved    int    `json:"reserved"`
}

func (p Pool) Validate() error {
	if !namePattern.MatchString(p.ID) || p.Concurrency < 1 || p.Concurrency > Capacity || p.Reserved < 0 || p.Reserved > p.Concurrency {
		return errors.New("pool requires a bounded identifier, concurrency 1–2, and reserved capacity within concurrency")
	}
	switch p.Discipline {
	case "fifo", "lifo", "priority", "deadline_edf":
		return nil
	default:
		return errors.New("unsupported pool discipline; registered policies are fifo, lifo, priority, deadline_edf")
	}
}
func Defaults() map[string]Pool {
	return map[string]Pool{"interactive": {ID: "interactive", Discipline: "priority", Concurrency: 2}, "batch": {ID: "batch", Discipline: "fifo", Concurrency: 2}, "calls": {ID: "calls", Discipline: "lifo", Concurrency: 2}}
}

type Dependency struct {
	Inbox     *Inbox   `json:"inbox,omitempty"`
	Artifacts []string `json:"artifacts,omitempty"`
	From      string   `json:"from"`
	Relation  string   `json:"relation"`
	Payload   string   `json:"payload"`
	Strength  string   `json:"strength"`
	Guard     string   `json:"guard,omitempty"`
}

// Inbox selects an admitted MQTT envelope, not a broker subscription. From on
// its dependency is a local source name, not a task ID. All selectors are exact.
type Inbox struct {
	Project       string `json:"project"`
	Mailbox       string `json:"mailbox"`
	Sender        string `json:"sender,omitempty"`
	Kind          string `json:"kind"`
	Type          string `json:"type"`
	Correlation   string `json:"correlation,omitempty"`
	Generation    int    `json:"generation"`
	AfterSequence int64  `json:"after_sequence"`
}

type InboxWitness struct {
	Key     string          `json:"key"`
	Message json.RawMessage `json:"message"`
}
type Join struct {
	Rule string `json:"rule"`
	K    int    `json:"k,omitempty"`
}
type Request struct {
	Pool     string       `json:"pool"`
	Priority int          `json:"priority"`
	Deadline *time.Time   `json:"deadline,omitempty"`
	Deps     []Dependency `json:"deps,omitempty"`
	Join     Join         `json:"join"`
}

func (r *Request) Normalize() {
	if r.Pool == "" {
		r.Pool = "interactive"
	}
	if r.Join.Rule == "" {
		r.Join.Rule = "all"
	}
	for i := range r.Deps {
		d := &r.Deps[i]
		if d.Relation == "" {
			d.Relation = "FS"
		}
		if d.Payload == "" {
			d.Payload = "control"
		}
		if d.Strength == "" {
			d.Strength = "hard"
		}
	}
}
func (r Request) Validate() error {
	r.Normalize()
	if !namePattern.MatchString(r.Pool) || r.Priority < -1000 || r.Priority > 1000 || len(r.Deps) > 128 {
		return errors.New("invalid pool, priority outside -1000..1000, or more than 128 dependencies")
	}
	if r.Deadline != nil && r.Deadline.IsZero() {
		return errors.New("deadline must be a nonzero timestamp")
	}
	seen := map[string]bool{}
	hard := 0
	for _, d := range r.Deps {
		if d.From == "" || len(d.From) > 128 || seen[d.From] {
			return errors.New("dependency sources must be unique task IDs")
		}
		seen[d.From] = true
		if d.Inbox != nil {
			in := d.Inbox
			if !namePattern.MatchString(d.From) || in.Project == "" || in.Mailbox == "" || in.Type == "" || len(in.Project) > 100 || len(in.Mailbox) > 100 || len(in.Sender) > 100 || len(in.Type) > 100 || len(in.Correlation) > 100 || in.Generation < 0 || in.AfterSequence < 0 || (in.Kind != "note" && in.Kind != "event" && in.Kind != "command") || d.Guard != "inbox_received" {
				return errors.New("inbox dependencies require a source name, exact mailbox/type/kind, nonnegative generation/cursor and inbox_received guard")
			}
			if len(d.Artifacts) != 0 {
				return errors.New("inbox data is delivered as message.json; artifact selection is only for task sources")
			}
		} else if d.Guard == "inbox_received" {
			return errors.New("inbox_received requires an inbox source")
		}
		if d.Relation != "FS" || (d.Payload != "control" && d.Payload != "data") {
			return errors.New("this drainer currently admits finish-to-start control or data dependencies")
		}
		if d.Payload == "data" && d.Inbox == nil {
			if len(d.Artifacts) == 0 || len(d.Artifacts) > 32 {
				return errors.New("data dependencies require 1–32 explicit artifact names")
			}
			seenNames := map[string]bool{}
			for _, name := range d.Artifacts {
				if !ArtifactName(name) || seenNames[name] {
					return errors.New("invalid or duplicate dependency artifact name")
				}
				seenNames[name] = true
			}
		} else if len(d.Artifacts) > 0 {
			return errors.New("artifact selection requires a data dependency")
		}
		if d.Strength != "hard" && d.Strength != "soft" {
			return errors.New("dependency strength must be hard or soft")
		}
		switch d.Guard {
		case "", "done", "failed", "cancelled", "candidate_available", "inbox_received":
		default:
			return errors.New("dependency guard must be a terminal task status")
		}
		if d.Strength == "hard" {
			hard++
		}
	}
	switch r.Join.Rule {
	case "all":
		if r.Join.K != 0 {
			return errors.New("all join does not take k")
		}
	case "any":
		if hard == 0 || r.Join.K != 0 {
			return errors.New("any join requires hard dependencies and no k")
		}
	case "k_of_n":
		if r.Join.K < 1 || r.Join.K > hard {
			return errors.New("quorum must be between one and the number of hard dependencies")
		}
	default:
		return errors.New("join must be all, any, or k_of_n")
	}
	return nil
}

type State struct {
	Inbox                   *InboxWitness
	ID, Status, RunID, Hash string
	Seq                     int64
	Sample                  bool
}
type Witness struct {
	Inbox        *InboxWitness `json:"inbox,omitempty"`
	Artifacts    []Artifact    `json:"artifacts,omitempty"`
	InputOmitted string        `json:"input_omitted,omitempty"`
	Seq          int64         `json:"event_seq"`
	TaskID       string        `json:"task_id"`
	RunID        string        `json:"run_id"`
	Status       string        `json:"status"`
	Hash         string        `json:"event_hash"`
}
type Artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

func ArtifactName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 255 && !strings.ContainsAny(name, "/\\\x00\n\r")
}

type Readiness struct {
	Ready     bool
	Waiting   []string
	Matched   []Witness
	SoftScore int
}

func Evaluate(r Request, states map[string]State) Readiness {
	r.Normalize()
	out := Readiness{Waiting: []string{}, Matched: []Witness{}}
	hard, matched := 0, 0
	optional := []Witness{}
	for _, dep := range r.Deps {
		state, exists := states[dep.From]
		terminal := state.Status == "done" || state.Status == "failed" || state.Status == "cancelled" || state.Status == "inbox_received" && state.Inbox != nil && dep.Inbox != nil
		ok := exists && !state.Sample && terminal && (dep.Guard == "" || dep.Guard == state.Status)
		if dep.Strength == "soft" {
			if ok {
				out.SoftScore++
				if dep.Payload == "data" {
					optional = append(optional, Witness{Inbox: state.Inbox, Seq: state.Seq, TaskID: dep.From, RunID: state.RunID, Status: state.Status, Hash: state.Hash})
				}
			}
			continue
		}
		hard++
		if ok {
			matched++
			out.Matched = append(out.Matched, Witness{Inbox: state.Inbox, Seq: state.Seq, TaskID: dep.From, RunID: state.RunID, Status: state.Status, Hash: state.Hash})
		} else {
			out.Waiting = append(out.Waiting, dep.From)
		}
	}
	needed := hard
	if r.Join.Rule == "any" {
		needed = 1
	}
	if r.Join.Rule == "k_of_n" {
		needed = r.Join.K
	}
	out.Ready = matched >= needed
	if out.Ready && r.Join.Rule != "all" {
		sort.Slice(out.Matched, func(i, j int) bool {
			a, b := out.Matched[i], out.Matched[j]
			if a.Seq != b.Seq {
				return a.Seq < b.Seq
			}
			return a.TaskID < b.TaskID
		})
		out.Matched = out.Matched[:needed]
	}
	out.Matched = append(out.Matched, optional...)
	return out
}

// Graph validates all enabling edges, including soft hints. Dependencies may
// cross pools; caller enforces the tenant boundary before supplying this graph.
func Graph(graph map[string]Request) error {
	color := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if color[id] == 1 {
			return fmt.Errorf("dependency cycle at task %s", id)
		}
		if color[id] == 2 {
			return nil
		}
		r, ok := graph[id]
		if !ok {
			return fmt.Errorf("dependency refers to missing task %s", id)
		}
		if err := r.Validate(); err != nil {
			return err
		}
		color[id] = 1
		for _, dep := range r.Deps {
			if dep.Inbox != nil {
				continue
			}
			if err := visit(dep.From); err != nil {
				return err
			}
		}
		color[id] = 2
		return nil
	}
	ids := make([]string, 0, len(graph))
	for id := range graph {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

type Candidate struct {
	PriorityAdjustment int
	ID                 string
	Request            Request
	ReadyAt            time.Time
	SoftScore          int
}

func Order(pool Pool, candidates []Candidate, at time.Time) []Candidate {
	out := append([]Candidate(nil), candidates...)
	score := func(c Candidate) int64 {
		return int64(c.Request.Priority) + int64(c.PriorityAdjustment) + max(0, int64(at.Sub(c.ReadyAt)/time.Minute)) + int64(c.SoftScore)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch pool.Discipline {
		case "lifo":
			if !a.ReadyAt.Equal(b.ReadyAt) {
				return a.ReadyAt.After(b.ReadyAt)
			}
		case "priority":
			if score(a) != score(b) {
				return score(a) > score(b)
			}
		case "deadline_edf":
			da, db := a.Request.Deadline, b.Request.Deadline
			if da == nil && db != nil {
				return false
			}
			if da != nil && db == nil {
				return true
			}
			if da != nil && db != nil && !da.Equal(*db) {
				return da.Before(*db)
			}
		}
		if !a.ReadyAt.Equal(b.ReadyAt) {
			return a.ReadyAt.Before(b.ReadyAt)
		}
		return a.ID < b.ID
	})
	return out
}
func Available(pool string, pools map[string]Pool, active map[string]int) bool {
	p, ok := pools[pool]
	if !ok || active[pool] >= p.Concurrency {
		return false
	}
	used, reserved := 0, 0
	for id, p := range pools {
		used += active[id]
		if id != pool {
			reserved += max(0, p.Reserved-active[id])
		}
	}
	return used < Capacity && used+reserved < Capacity
}
