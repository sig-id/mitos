package studio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"mitos/internal/workpool"
)

// GraphProfile versions the composition contract; executor runtimes retain their
// own versions. Capabilities advertise the implemented subset separately.
const GraphProfile = "work-graph/v1"
const graphMaxBytes = 2 << 20

type GraphLimits struct {
	Tasks           int       `json:"tasks"`
	Amendments      int       `json:"amendments"`
	Attempts        int       `json:"attempts"`
	Retries         int       `json:"retries"`
	Expansions      int       `json:"expansions"`
	ControllerCalls int       `json:"controller_calls"`
	Depth           int       `json:"depth"`
	Fanout          int       `json:"fanout"`
	Deadline        time.Time `json:"deadline"`
}
type GraphPolicy struct {
	Mode                  string   `json:"mode"` // defined, adaptive, hybrid
	Applications          []string `json:"applications"`
	AllowArguments        bool     `json:"allow_arguments"`
	ControllerApplication string   `json:"controller_application,omitempty"`
	DecisionApplication   string   `json:"decision_application,omitempty"`
}
type GraphSpec struct {
	ID         string   `json:"id"` // proposal-local reference at creation
	Parent     string   `json:"parent,omitempty"`
	Kind       string   `json:"kind"` // scope or application
	Required   bool     `json:"required"`
	Criteria   string   `json:"criteria"`
	Acceptance string   `json:"acceptance"` // operator or application
	Brief      Brief    `json:"brief"`
	Arguments  []string `json:"arguments,omitempty"` // append to registered argv only by policy
	Context    string   `json:"context,omitempty"`
}
type GraphExecutor struct {
	Application Application `json:"application"`
	Directory   string      `json:"directory"`
	Builtin     bool        `json:"builtin"`
	Digest      string      `json:"digest"`
}
type GraphMember struct {
	ControllerFor   string           `json:"controller_for,omitempty"`
	Spec            GraphSpec        `json:"spec"`
	Revision        int64            `json:"revision"`
	FactRevision    int64            `json:"fact_revision"`
	History         []GraphSpec      `json:"history,omitempty"`
	Executor        *GraphExecutor   `json:"executor,omitempty"`
	Attempts        []string         `json:"attempts,omitempty"`
	CurrentRunID    string           `json:"current_run_id,omitempty"`
	State           string           `json:"state"`
	Sealed          bool             `json:"sealed"`
	CancelRequested bool             `json:"cancel_requested"`
	Replacement     string           `json:"replacement,omitempty"`
	Accepted        *GraphAcceptance `json:"accepted,omitempty"`
}
type GraphAcceptance struct {
	SpecRevision int64     `json:"spec_revision"`
	FactRevision int64     `json:"fact_revision"`
	RunID        string    `json:"run_id,omitempty"`
	Evidence     string    `json:"evidence"`
	Rationale    string    `json:"rationale"`
	At           time.Time `json:"at"`
	Manifest     string    `json:"manifest"`
}
type GraphExpansion struct {
	ID             string               `json:"id"`
	Scope          string               `json:"scope"`
	Generation     int64                `json:"generation"`
	Deadline       time.Time            `json:"deadline"`
	Outcome        string               `json:"outcome,omitempty"`
	ControllerTask string               `json:"controller_task,omitempty"`
	Revision       int64                `json:"revision,omitempty"`
	Reads          map[string]GraphRead `json:"reads,omitempty"`
	TriggerCursor  int64                `json:"trigger_cursor,omitempty"`
	Error          string               `json:"error,omitempty"`
}
type GraphTrigger struct {
	Seq     int64  `json:"seq"`
	TaskID  string `json:"task_id"`
	RunID   string `json:"run_id"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome,omitempty"`
}
type GraphReceipt struct {
	Key      string            `json:"key"`
	Digest   string            `json:"digest"`
	Revision int64             `json:"revision"`
	IDs      map[string]string `json:"ids,omitempty"`
	At       time.Time         `json:"at"`
}
type WorkGraph struct {
	Profile              string                     `json:"profile"`
	ID                   string                     `json:"id"`
	Home                 string                     `json:"home"`
	ProjectID            string                     `json:"project_id"`
	Workspace            string                     `json:"workspace"`
	RootTask             string                     `json:"root_task"`
	GoalRevision         int64                      `json:"goal_revision"`
	Revision             int64                      `json:"revision"`
	PolicyRevision       int64                      `json:"policy_revision"`
	ControllerGeneration int64                      `json:"controller_generation"`
	Activation           string                     `json:"activation"`
	Predecessor          string                     `json:"predecessor,omitempty"`
	Policy               GraphPolicy                `json:"policy"`
	Limits               GraphLimits                `json:"limits"`
	Members              map[string]*GraphMember    `json:"members"`
	Expansions           map[string]*GraphExpansion `json:"expansions"`
	Triggers             []GraphTrigger             `json:"triggers,omitempty"`
	Cursor               int64                      `json:"cursor"`
	Attempts             int                        `json:"attempts"`
	ControllerCalls      int                        `json:"controller_calls"`
	Receipts             map[string]GraphReceipt    `json:"receipts"`
	Status               string                     `json:"status"`
	CreatedAt            time.Time                  `json:"created_at"`
	UpdatedAt            time.Time                  `json:"updated_at"`
	Decisions            map[string]*GraphDecision  `json:"decisions,omitempty"`
}
type CreateGraph struct {
	Profile      string      `json:"profile"`
	Key          string      `json:"key"`
	ProjectID    string      `json:"project_id"`
	GoalRevision int64       `json:"goal_revision"`
	Activation   string      `json:"activation"`
	Predecessor  string      `json:"predecessor,omitempty"`
	Policy       GraphPolicy `json:"policy"`
	Limits       GraphLimits `json:"limits"`
	Tasks        []GraphSpec `json:"tasks"`
	Seal         bool        `json:"seal"`
}
type GraphRead struct {
	Specification int64 `json:"specification"`
	Fact          int64 `json:"fact"`
}
type GraphOperation struct {
	Kind         string                `json:"kind"`
	Task         string                `json:"task,omitempty"`
	Spec         *GraphSpec            `json:"spec,omitempty"`
	Dependencies []workpool.Dependency `json:"dependencies,omitempty"`
	Join         workpool.Join         `json:"join,omitempty"`
	Priority     int                   `json:"priority,omitempty"`
	Expansion    string                `json:"expansion,omitempty"`
	Trigger      int64                 `json:"trigger,omitempty"`
	Cursor       int64                 `json:"cursor,omitempty"`
	Evidence     string                `json:"evidence,omitempty"`
	Rationale    string                `json:"rationale,omitempty"`
}
type GraphProposal struct {
	Profile        string               `json:"profile"`
	Key            string               `json:"key"`
	GraphID        string               `json:"graph_id"`
	Revision       int64                `json:"revision"`
	PolicyRevision int64                `json:"policy_revision"`
	Generation     int64                `json:"generation"`
	Deadline       time.Time            `json:"deadline"`
	Reads          map[string]GraphRead `json:"reads,omitempty"`
	Operations     []GraphOperation     `json:"operations"`
	Explanation    string               `json:"explanation,omitempty"`
}
type GraphAttempt struct {
	GraphID        string             `json:"graph_id"`
	GraphRevision  int64              `json:"graph_revision"`
	Specification  int64              `json:"specification"`
	PolicyRevision int64              `json:"policy_revision"`
	Generation     int64              `json:"generation"`
	Executor       GraphExecutor      `json:"executor"`
	Criteria       string             `json:"criteria"`
	Context        string             `json:"context,omitempty"`
	Witnesses      []workpool.Witness `json:"witnesses,omitempty"`
	AdmittedAt     *time.Time         `json:"admitted_at,omitempty"`
}
type graphChange struct {
	Graph        WorkGraph `json:"graph"`
	Tasks        []Task    `json:"tasks,omitempty"`
	Runs         []Run     `json:"runs,omitempty"`
	CreateKey    string    `json:"create_key,omitempty"`
	CreateDigest string    `json:"create_digest,omitempty"`
}
type graphCreation struct{ ID, Digest string }

func graphClone[T any](v T) T {
	b, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(b, &out)
	return out
}
func graphDigest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// DecodeGraphJSON rejects ambiguous keys, invalid UTF-8, unknown fields and
// trailing values before decoding typed values. Digests use typed Go JSON with
// sorted map keys, typed integer/time values and no Unicode normalization.
func DecodeGraphJSON(b []byte, target any) error {
	if len(b) > graphMaxBytes || !utf8.Valid(b) {
		return errors.New("graph request is too large or is not UTF-8")
	}
	if err := graphUnicode(b); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func() error
	value = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					k, err := d.Token()
					if err != nil {
						return err
					}
					name, ok := k.(string)
					if !ok || seen[name] {
						return errors.New("duplicate JSON key")
					}
					seen[name] = true
					if err = value(); err != nil {
						return err
					}
				}
				_, err = d.Token()
				return err
			case '[':
				for d.More() {
					if err = value(); err != nil {
						return err
					}
				}
				_, err = d.Token()
				return err
			default:
				return errors.New("unexpected JSON delimiter")
			}
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("send exactly one JSON value")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(target)
}
func graphIDs(g *WorkGraph) []string {
	ids := make([]string, 0, len(g.Members))
	for id := range g.Members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func graphError(format string, args ...any) error { return fmt.Errorf("work graph: "+format, args...) }

// Reject irrelevant fields rather than silently ignoring a misspelled operation
// shape (for example dependencies attached to a cancellation).
func validateGraphOperation(op GraphOperation) error {
	allowed := map[string][]string{
		"add": {"spec"}, "supersede": {"task", "spec", "rationale"}, "amend": {"task", "spec"},
		"dependencies": {"task", "dependencies", "join"}, "priority": {"task", "priority"},
		"open_expansion": {"task", "expansion"}, "close_expansion": {"expansion", "rationale"},
		"classify_trigger": {"trigger", "rationale"}, "seal": {"task", "cursor"}, "reopen": {"task"},
		"cancel": {"task", "rationale"}, "replace_controller": {"rationale"}, "retry": {"task"},
		"accept": {"task", "evidence", "rationale"}, "dismiss_decision": {"expansion", "rationale"},
	}
	keys, ok := allowed[op.Kind]
	if !ok {
		return graphError("unsupported operation %q", op.Kind)
	}
	b, _ := json.Marshal(op)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	for key, v := range fields {
		if key == "kind" {
			continue
		}
		if key == "join" && string(v) == `{"rule":""}` {
			continue
		}
		found := false
		for _, k := range keys {
			if k == key {
				found = true
			}
		}
		if !found {
			return graphError("%s is not valid for %s", key, op.Kind)
		}
	}
	if len(op.Evidence) > 4000 || len(op.Rationale) > 4000 {
		return graphError("operation evidence and rationale must be bounded")
	}
	return nil
}

func graphUnicode(b []byte) error {
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			continue
		}
		i++
		if i >= len(b) || b[i] != 'u' {
			continue
		}
		if i+4 >= len(b) {
			return errors.New("invalid Unicode escape")
		}
		v, err := strconv.ParseUint(string(b[i+1:i+5]), 16, 16)
		if err != nil {
			return err
		}
		i += 4
		if v >= 0xDC00 && v <= 0xDFFF {
			return errors.New("unpaired Unicode surrogate")
		}
		if v >= 0xD800 && v <= 0xDBFF {
			if i+6 >= len(b) || b[i+1] != '\\' || b[i+2] != 'u' {
				return errors.New("unpaired Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(b[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return errors.New("unpaired Unicode surrogate")
			}
			i += 6
		}
	}
	return nil
}
