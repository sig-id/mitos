// Package agentretry defines the operator-configured policy for retrying
// transient model-provider failures reported by native agent sessions. The
// policy is evidence-driven guidance executed inside the agent harness, not an
// OS-level guarantee: providers can still fail in ways the harness cannot
// observe, and exhausted policies surface the failure instead of looping.
package agentretry

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Policy bounds automatic recovery for transient model-provider errors. Zero
// fields fall back to Default; a nil policy behaves like Default.
type Policy struct {
	// TransientCodes are HTTP status codes that are always retried while
	// attempts remain. Other codes surface to the operator unretried.
	TransientCodes []int `json:"transient_codes,omitempty"`
	// MaxAttempts bounds attempts per failure burst, including the original
	// failed call. When attempts on one model are exhausted and fallback
	// models remain, the next model starts a fresh burst.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// BackoffSeconds is the delay before the first retry of a burst.
	BackoffSeconds int `json:"backoff_seconds,omitempty"`
	// BackoffMultiplier grows the delay for each further retry. One keeps
	// the delay fixed.
	BackoffMultiplier float64 `json:"backoff_multiplier,omitempty"`
	// MaxBackoffSeconds caps the computed backoff delay.
	MaxBackoffSeconds int `json:"max_backoff_seconds,omitempty"`
	// RetryAfterCapSeconds caps delays requested by the provider through
	// Retry-After style hints. Larger requests are clamped, never extended.
	RetryAfterCapSeconds int `json:"retry_after_cap_seconds,omitempty"`
	// FallbackModels are provider/model identifiers tried, in order, after
	// the current model exhausts its attempts. Missing models or providers
	// without credentials are skipped and reported.
	FallbackModels []string `json:"fallback_models,omitempty"`
}

// Default returns the built-in policy: always retry transient HTTP failures
// (408, 425, 429, 5xx) up to three attempts per burst with exponential
// backoff, honor provider retry hints up to ten minutes, and no fallback
// models unless the operator configures them.
func Default() Policy {
	return Policy{
		TransientCodes:       []int{408, 425, 429, 500, 502, 503, 504},
		MaxAttempts:          3,
		BackoffSeconds:       5,
		BackoffMultiplier:    2,
		MaxBackoffSeconds:    60,
		RetryAfterCapSeconds: 600,
	}
}

// Effective fills zero fields with the defaults and returns a policy with
// normalized slices safe for mutation by the caller.
func (p Policy) Effective() Policy {
	d := Default()
	if len(p.TransientCodes) == 0 {
		p.TransientCodes = d.TransientCodes
	} else {
		p.TransientCodes = slices.Clone(p.TransientCodes)
	}
	if p.MaxAttempts == 0 {
		p.MaxAttempts = d.MaxAttempts
	}
	if p.BackoffSeconds == 0 {
		p.BackoffSeconds = d.BackoffSeconds
	}
	if p.BackoffMultiplier == 0 {
		p.BackoffMultiplier = d.BackoffMultiplier
	}
	if p.MaxBackoffSeconds == 0 {
		p.MaxBackoffSeconds = d.MaxBackoffSeconds
	}
	if p.RetryAfterCapSeconds == 0 {
		p.RetryAfterCapSeconds = d.RetryAfterCapSeconds
	}
	p.FallbackModels = slices.Clone(p.FallbackModels)
	return p
}

// Validate rejects policies that could not execute safely: unknown HTTP
// ranges, attempt or delay bounds that would spin or stall a session, and
// malformed fallback model identifiers. Zero fields mean "use the default"
// and validate through the effective values.
func (p Policy) Validate() error {
	effective := p.Effective()
	if len(p.TransientCodes) > 32 {
		return errors.New("retry policy lists too many transient codes")
	}
	for _, code := range p.TransientCodes {
		if code < 400 || code > 599 {
			return fmt.Errorf("transient code %d is outside the HTTP error range", code)
		}
	}
	if p.MaxAttempts != 0 && (p.MaxAttempts < 1 || p.MaxAttempts > 10) {
		return errors.New("retry attempts must be between 1 and 10")
	}
	if p.BackoffSeconds != 0 && (p.BackoffSeconds < 1 || p.BackoffSeconds > 3600) {
		return errors.New("retry backoff must be between 1 second and 1 hour")
	}
	if p.BackoffMultiplier != 0 && (p.BackoffMultiplier < 1 || p.BackoffMultiplier > 10) {
		return errors.New("retry backoff multiplier must be between 1 and 10")
	}
	if p.MaxBackoffSeconds != 0 && (p.MaxBackoffSeconds < 1 || p.MaxBackoffSeconds > 86400) {
		return errors.New("maximum retry backoff must be between 1 second and 1 day")
	}
	if p.RetryAfterCapSeconds != 0 && (p.RetryAfterCapSeconds < 1 || p.RetryAfterCapSeconds > 86400) {
		return errors.New("retry-after cap must be between 1 second and 1 day")
	}
	if effective.MaxBackoffSeconds < effective.BackoffSeconds {
		return errors.New("maximum retry backoff is below the initial backoff")
	}
	if len(p.FallbackModels) > 8 {
		return errors.New("retry policy lists too many fallback models")
	}
	for _, model := range p.FallbackModels {
		provider, id, ok := strings.Cut(model, "/")
		if !ok || provider == "" || id == "" || strings.ContainsAny(model, " \t\r\n\x00") || len(model) > 128 {
			return fmt.Errorf("fallback model %q must be a provider/model identifier", model)
		}
	}
	return nil
}

// IsTransient reports whether the policy retries the HTTP status code.
func (p Policy) IsTransient(code int) bool {
	return slices.Contains(p.Effective().TransientCodes, code)
}

// Encode returns the compact policy JSON passed to agent harnesses through
// the environment. The encoded form is bounded so it stays safe for argv-less
// transport regardless of configuration source.
func (p Policy) Encode() (string, error) {
	effective := p.Effective()
	if err := effective.Validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(effective)
	if err != nil {
		return "", err
	}
	if len(encoded) > 4096 {
		return "", errors.New("retry policy exceeds 4096 bytes")
	}
	return string(encoded), nil
}

var httpCodePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\s*(\d{3})\b`),                   // "429: usage limit"
	regexp.MustCompile(`(?i)\berror\s*\(\s*(\d{3})\s*\)`), // "OpenAI API error (401): ..."
	regexp.MustCompile(`(?i)\b(?:http|status)[\s_-]*(\d{3})\b`),
}

// ParseHTTPCode extracts the HTTP status code from a harness-reported error
// message. Providers format codes differently ("429: ...", "error (401): ..."),
// so only the message head is searched. Zero means no code was identified.
func ParseHTTPCode(message string) int {
	head := message
	if len(head) > 120 {
		head = head[:120]
	}
	for _, pattern := range httpCodePatterns {
		if m := pattern.FindStringSubmatch(head); m != nil {
			code, err := strconv.Atoi(m[1])
			if err == nil && code >= 100 && code <= 599 {
				return code
			}
		}
	}
	return 0
}
