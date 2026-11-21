package watch

import (
	"context"
	"errors"
	"slices"
	"strings"

	"mitos/internal/decisionmodel"
)

// Triage asks a decision model which level to deliver a group at. The provider
// and its thresholds are the owner's configured decision provider; the watch
// holds only the author's grading instructions and its own call cap.
type Triage struct {
	// Instructions are the author's grading guidance, sent as data in the
	// projection. They never change the offered levels or thresholds.
	Instructions string `json:"instructions"`
	// MaxCalls narrows the watch's share of its owner's allowance over the
	// watch's whole life; renewal and replacement never replenish it. Zero
	// leaves only the owner's allowance.
	MaxCalls int `json:"max_calls,omitempty"`
	// AllowEscalate offers escalate. Without it triage offers digest, notify
	// and wake (and escalate only when that is the default).
	AllowEscalate bool `json:"allow_escalate,omitempty"`
}

// MaxTriageTimeoutMS bounds one triage request, whatever the provider allows.
const MaxTriageTimeoutMS = 30000

func (t Triage) validate() error {
	if strings.TrimSpace(t.Instructions) == "" || len(t.Instructions) > 4000 {
		return errors.New("triage.instructions must be 1–4000 bytes")
	}
	if t.MaxCalls < 0 || t.MaxCalls > 1024 {
		return errors.New("triage.max_calls must be 0 or 1–1024")
	}
	return nil
}

// Offered lists the levels triage may answer for a watch with default d.
func (t Triage) Offered(d Level) []Level {
	out := []Level{Digest, Notify, Wake}
	if t.AllowEscalate || d == Escalate {
		out = append(out, Escalate)
	}
	return out
}

// Level sources recorded on every delivery.
const (
	SourceRule     = "rule"     // a must-deliver rule or runtime notice
	SourceDefault  = "default"  // the watch default, untriaged
	SourceTriage   = "triage"   // an accepted triage answer
	SourceFallback = "fallback" // triage ran or was due but the default stands
)

// Fallback reasons. Every one delivers at the default level.
const (
	FallbackRefused        = "refused"
	FallbackTimeout        = "timeout"
	FallbackInvalidAnswer  = "invalid_answer"
	FallbackProviderError  = "provider_error"
	FallbackCredential     = "credential_unavailable"
	FallbackBelowThreshold = "below_threshold"
	FallbackExhausted      = "allowance_exhausted"
	FallbackUnknown        = "outcome_unknown"
	FallbackUnconfigured   = "provider_unconfigured"
)

// Grade is the level a triaged group is delivered at, and why.
type Grade struct {
	Level  Level  `json:"level"`
	Source string `json:"source"`
	Reason string `json:"reason,omitempty"`
}

// Thresholds gate an answer below the default.
type Thresholds struct {
	MinConfidence  float64 `json:"min_confidence"`
	MinProbability float64 `json:"min_probability"`
}

// Accept grades one triage outcome against the default d. An answer at or
// above d is accepted outright; one below d needs both thresholds. A failed
// call, an invalid answer or a level that was not offered keeps d.
func Accept(answer *decisionmodel.Answer, callErr error, req decisionmodel.Request, d Level, offered []Level, t Thresholds) Grade {
	fallback := func(reason string) Grade { return Grade{Level: d, Source: SourceFallback, Reason: reason} }
	if callErr != nil {
		return fallback(classify(callErr))
	}
	if answer == nil || answer.Validate(req) != nil {
		return fallback(FallbackInvalidAnswer)
	}
	a := Level(answer.Choice)
	if !a.Valid() || !slices.Contains(offered, a) {
		return fallback(FallbackInvalidAnswer)
	}
	if a.Rank() >= d.Rank() {
		return Grade{Level: a, Source: SourceTriage}
	}
	if answer.Confidence >= t.MinConfidence && answer.Probabilities[answer.Choice] >= t.MinProbability {
		return Grade{Level: a, Source: SourceTriage}
	}
	return fallback(FallbackBelowThreshold)
}

// classify maps adapter error codes (fixed strings, never remote bodies) to
// fallback reasons.
func classify(err error) string {
	switch code := err.Error(); {
	case code == "model_refused":
		return FallbackRefused
	case code == "request_interrupted" || code == "deadline_expired" || errors.Is(err, context.DeadlineExceeded):
		return FallbackTimeout
	case code == "credential_unavailable" || strings.HasPrefix(code, "saved API key unavailable"):
		return FallbackCredential
	case strings.HasPrefix(code, "invalid_answer") || strings.HasPrefix(code, "invalid_choice") || strings.HasPrefix(code, "invalid_probabilities") || code == "invalid_response" || code == "invalid_usage":
		return FallbackInvalidAnswer
	}
	return FallbackProviderError
}
