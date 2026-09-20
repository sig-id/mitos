// Package decision defines human requests independently of their transport.
package decision

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

type Tenant struct {
	Org     string `json:"org"`
	Project string `json:"project"`
}

func (t Tenant) Valid() bool {
	return strings.TrimSpace(t.Org) != "" && strings.TrimSpace(t.Project) != ""
}

var LocalTenant = Tenant{Org: "local", Project: "studio"}

type Context struct {
	Attempted    string   `json:"attempted"`
	BlockedBy    string   `json:"blocked_by"`
	EvidenceRefs []string `json:"evidence_refs"`
}

func (c Context) String() string { return c.BlockedBy }
func (c *Context) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &c.BlockedBy)
	}
	type plain Context
	return json.Unmarshal(b, (*plain)(c))
}

type Cost struct {
	WastedWallMS int64   `json:"wasted_wall_ms"`
	WastedUSD    float64 `json:"wasted_usd"`
	Recoverable  bool    `json:"recoverable"`
	Detail       string  `json:"detail,omitempty"`
}

func (c Cost) String() string { return c.Detail }
func (c *Cost) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &c.Detail)
	}
	type plain Cost
	return json.Unmarshal(b, (*plain)(c))
}

type BlastRadius struct {
	Paths        []string `json:"paths"`
	Destinations []string `json:"destinations"`
	Spend        float64  `json:"spend"`
}
type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
	Recommended bool   `json:"recommended,omitempty"`
}
type Resolution struct {
	DeviceID   string          `json:"device_id,omitempty"`
	CommandID  string          `json:"command_id,omitempty"`
	DecisionID string          `json:"decision_id"`
	Resolver   string          `json:"resolver"`
	Choice     string          `json:"choice"`
	Value      json.RawMessage `json:"value,omitempty"`
	Note       string          `json:"note,omitempty"`
	ResolvedAt time.Time       `json:"resolved_at"`
	Via        string          `json:"via"`
}
type Source struct {
	Adapter   string `json:"adapter"`
	SessionID string `json:"session_id"`
	RequestID string `json:"request_id"`
}

type Card struct {
	Source           *Source         `json:"source,omitempty"`
	SupersededAt     *time.Time      `json:"superseded_at,omitempty"`
	ApplicationError string          `json:"application_error,omitempty"`
	ID               string          `json:"decision_id"`
	RunID            string          `json:"run_id"`
	CausedBy         int64           `json:"caused_by"`
	Tenant           Tenant          `json:"tenant"`
	Kind             string          `json:"kind"`
	Reversibility    string          `json:"reversibility"`
	Question         string          `json:"question"`
	Context          Context         `json:"context"`
	Options          []Option        `json:"options"`
	Default          *string         `json:"default"`
	TTLMS            *int64          `json:"ttl_ms"`
	BlastRadius      BlastRadius     `json:"blast_radius"`
	CostOfWrong      Cost            `json:"cost_of_wrong"`
	ValueSchema      json.RawMessage `json:"value_schema"`
	DedupeKey        string          `json:"dedupe_key"`
	// Timing and resolution are projections of the durable log, never client clocks.
	RequestedAt    time.Time   `json:"requested_at"`
	Resolution     *Resolution `json:"resolution,omitempty"`
	AppliedAt      *time.Time  `json:"applied_at,omitempty"`
	ResultRunID    string      `json:"result_run_id,omitempty"`
	ReasonSignal   string      `json:"reason_signal,omitempty"`
	InterruptClass string      `json:"interrupt_class,omitempty"`
	Choice         string      `json:"choice,omitempty"`
	ResolvedAt     *time.Time  `json:"resolved_at,omitempty"`
}

func (c Card) Validate(market bool) error {
	if c.ID == "" || c.RunID == "" || c.CausedBy <= 0 || !c.Tenant.Valid() || c.RequestedAt.IsZero() || c.DedupeKey == "" {
		return errors.New("decision requires identity, tenant, causality, timestamp, and dedupe key")
	}
	switch c.Kind {
	case "approve", "choose", "supply_value", "amend_brief", "rewind", "abort":
	default:
		return errors.New("invalid decision kind")
	}
	if strings.TrimSpace(c.Question) == "" || utf8.RuneCountInString(c.Question) > 200 || strings.TrimSpace(c.Context.Attempted) == "" || strings.TrimSpace(c.Context.BlockedBy) == "" {
		return errors.New("decision requires a short question and folded attempted/blocked_by context")
	}
	if len(c.Options) < 2 || len(c.Options) > 3 {
		return errors.New("decision requires two or three executable options")
	}
	seen := map[string]bool{}
	recommended := 0
	for _, o := range c.Options {
		if o.ID == "" || seen[o.ID] || strings.TrimSpace(o.Label) == "" || strings.TrimSpace(o.Consequence) == "" {
			return errors.New("decision options require unique IDs, labels, and consequences")
		}
		seen[o.ID] = true
		if o.Recommended {
			recommended++
		}
	}
	if recommended > 1 {
		return errors.New("decision has multiple recommended options")
	}
	if (c.Default == nil) != (c.TTLMS == nil) {
		return errors.New("default and ttl_ms must both be null or both specified")
	}
	switch c.Reversibility {
	case "external":
		if c.Default != nil {
			return errors.New("external decisions cannot default")
		}
	case "recoverable":
		if !market && c.Default == nil {
			return errors.New("recoverable Studio decisions require a default and ttl_ms")
		}
	case "internal":
		return errors.New("internal acts must not raise a decision card")
	default:
		return errors.New("unclassified decisions are not authorized")
	}
	if market && c.Default != nil {
		return errors.New("Market decisions cannot default")
	}
	if c.Default != nil && (!seen[*c.Default] || *c.TTLMS <= 0 || *c.TTLMS > int64((24*time.Hour)/time.Millisecond)) {
		return errors.New("default must select an option with a positive ttl_ms up to 24 hours")
	}
	if c.CostOfWrong.WastedWallMS < 0 || !finiteNonnegative(c.CostOfWrong.WastedUSD) || !finiteNonnegative(c.BlastRadius.Spend) {
		return errors.New("invalid decision cost or spend")
	}
	if c.Kind == "supply_value" {
		if len(c.ValueSchema) == 0 || string(c.ValueSchema) == "null" || !json.Valid(c.ValueSchema) {
			return errors.New("supply_value requires a value schema")
		}
	} else if len(c.ValueSchema) > 0 && string(c.ValueSchema) != "null" {
		return errors.New("value_schema is only valid for supply_value")
	}
	return nil
}
func finiteNonnegative(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 }
func (c Card) Due(at time.Time) bool {
	return c.Default != nil && c.TTLMS != nil && c.Resolution == nil && c.Choice == "" && !at.Before(c.RequestedAt.Add(time.Duration(*c.TTLMS)*time.Millisecond))
}
func (c Card) HasOption(id string) bool {
	for _, o := range c.Options {
		if o.ID == id {
			return true
		}
	}
	return false
}
func (c Card) Expiry() string {
	if c.Default == nil || c.TTLMS == nil {
		return ""
	}
	return fmt.Sprintf("%s", c.RequestedAt.Add(time.Duration(*c.TTLMS)*time.Millisecond).Format(time.RFC3339))
}

type Principal struct {
	ID            string
	Tenant        Tenant
	Authenticated bool
}

// LocalOperator is asserted only by the loopback console boundary. It is not a
// hosted/sigid identity and must never be used by a remote connector.
func LocalOperator() Principal {
	return Principal{ID: "local-operator", Tenant: LocalTenant, Authenticated: true}
}
