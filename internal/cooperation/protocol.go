// Package cooperation implements the portable mitos.cooperation/v2 envelope.
package cooperation

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Version = "mitos.cooperation/v2"

type Resolver struct {
	TaskID   string `json:"task_id"`
	RunID    string `json:"run_id"`
	Revision int    `json:"revision"`
}
type BlockedAction struct {
	TaskID   string `json:"task_id"`
	RunID    string `json:"run_id"`
	Revision int    `json:"revision"`
	Action   string `json:"action"`
}
type Message struct {
	PermittedResolvers []Resolver      `json:"permitted_resolvers,omitempty"`
	BlockedActions     []BlockedAction `json:"blocked_actions,omitempty"`
	DecisionID         string          `json:"decision_id,omitempty"`
	Version            string          `json:"version"`
	ID                 string          `json:"id"`
	TaskID             string          `json:"task_id"`
	SessionID          string          `json:"session_id,omitempty"`
	RunID              string          `json:"run_id,omitempty"`
	Revision           int             `json:"revision"`
	Kind               string          `json:"kind"`
	InReplyTo          string          `json:"in_reply_to,omitempty"`
	Body               string          `json:"body"`
	Artifacts          []string        `json:"artifacts,omitempty"`
	ExpiresAt          *time.Time      `json:"expires_at,omitempty"`
}

func (m Message) Request() bool {
	return m.Kind == "help" || m.Kind == "question" || m.Kind == "approval"
}
func (m Message) Validate(now time.Time) error {
	raw, _ := json.Marshal(m)
	if len(raw) > 24<<10 {
		return errors.New("encoded cooperation envelope exceeds 24 KiB")
	}
	if m.Version != Version || m.ID == "" || len(m.ID) > 100 || strings.ContainsAny(m.ID, "/\x00") || m.TaskID == "" || len(m.TaskID) > 160 || (m.SessionID == "") == (m.RunID == "") || m.Revision < 1 || strings.TrimSpace(m.Body) == "" || len(m.Body) > 16000 || strings.ContainsRune(m.Body, 0) || len(m.Artifacts) > 32 {
		return errors.New("invalid cooperation envelope or origin")
	}
	switch m.Kind {
	case "assignment", "help", "question", "approval", "observation", "handoff", "result", "answer", "cancel":
	default:
		return errors.New("unsupported cooperation kind")
	}
	if (m.Kind == "answer" || m.Kind == "cancel") != (m.InReplyTo != "") {
		return errors.New("only answers must reference an originating request")
	}
	if (len(m.BlockedActions) > 0 || len(m.PermittedResolvers) > 0 || m.ExpiresAt != nil) && !m.Request() {
		return errors.New("only requests have blocking dependencies or expiry")
	}
	if m.ExpiresAt != nil && (!m.ExpiresAt.After(now) || m.ExpiresAt.After(now.Add(30*24*time.Hour))) {
		return errors.New("request expiry is outside its permitted lifetime")
	}
	if m.Request() && (len(m.PermittedResolvers) == 0 || len(m.PermittedResolvers) > 16 || len(m.BlockedActions) > 16) {
		return errors.New("requests require 1..16 explicit resolvers and at most 16 blocked actions")
	}
	for _, r := range m.PermittedResolvers {
		if r.TaskID == "" || len(r.TaskID) > 160 || r.RunID == "" || len(r.RunID) > 160 || r.Revision < 1 {
			return errors.New("invalid resolver")
		}
	}
	for _, b := range m.BlockedActions {
		if b.TaskID == "" || len(b.TaskID) > 160 || b.RunID == "" || len(b.RunID) > 160 || b.Revision < 1 || b.Action != "acceptance" {
			return errors.New("unsupported blocked action; only exact attempt acceptance is supported")
		}
	}
	if (m.Kind == "approval") != (m.DecisionID != "") {
		return errors.New("approval evidence must reference a canonical decision")
	}
	for _, a := range m.Artifacts {
		if a == "" || len(a) > 1000 || strings.ContainsRune(a, 0) {
			return errors.New("invalid artifact reference")
		}
	}
	return nil
}
