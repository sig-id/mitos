// Package cooperation implements the portable mitos.cooperation/v1 envelope.
package cooperation

import (
	"errors"
	"strings"
	"time"
)

const Version = "mitos.cooperation/v1"

type Message struct {
	Version   string     `json:"version"`
	ID        string     `json:"id"`
	TaskID    string     `json:"task_id"`
	SessionID string     `json:"session_id,omitempty"`
	RunID     string     `json:"run_id,omitempty"`
	Revision  int        `json:"revision"`
	Kind      string     `json:"kind"`
	InReplyTo string     `json:"in_reply_to,omitempty"`
	Body      string     `json:"body"`
	Artifacts []string   `json:"artifacts,omitempty"`
	Blocking  bool       `json:"blocking,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (m Message) Request() bool {
	return m.Kind == "help" || m.Kind == "question" || m.Kind == "approval"
}
func (m Message) Validate(now time.Time) error {
	if m.Version != Version || m.ID == "" || len(m.ID) > 100 || strings.ContainsAny(m.ID, "/\x00") || m.TaskID == "" || len(m.TaskID) > 160 || (m.SessionID == "") == (m.RunID == "") || m.Revision < 1 || strings.TrimSpace(m.Body) == "" || len(m.Body) > 16000 || strings.ContainsRune(m.Body, 0) || len(m.Artifacts) > 32 {
		return errors.New("invalid cooperation envelope or origin")
	}
	switch m.Kind {
	case "assignment", "help", "question", "approval", "observation", "handoff", "result", "answer":
	default:
		return errors.New("unsupported cooperation kind")
	}
	if (m.Kind == "answer") != (m.InReplyTo != "") {
		return errors.New("only answers must reference an originating request")
	}
	if (m.Blocking || m.ExpiresAt != nil) && !m.Request() {
		return errors.New("only requests have blocking dependencies or expiry")
	}
	if m.ExpiresAt != nil && (!m.ExpiresAt.After(now) || m.ExpiresAt.After(now.Add(30*24*time.Hour))) {
		return errors.New("request expiry is outside its permitted lifetime")
	}
	for _, a := range m.Artifacts {
		if a == "" || len(a) > 1000 || strings.ContainsRune(a, 0) {
			return errors.New("invalid artifact reference")
		}
	}
	return nil
}
