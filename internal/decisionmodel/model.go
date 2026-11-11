// Package decisionmodel adapts typed inference APIs. It never executes actions.
package decisionmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const MaxBytes = 64 << 10

// Config is frozen with the execution plan. APIKeyEnv is a reference, never a key.
type Config struct {
	Protocol       string   `json:"protocol"`
	Endpoint       string   `json:"endpoint"`
	Model          string   `json:"model"`
	APIKeyEnv      string   `json:"api_key_env,omitempty"`
	Hooks          []string `json:"hooks"`
	Instructions   string   `json:"instructions"`
	TimeoutMS      int      `json:"timeout_ms"`
	MaxCalls       int      `json:"max_calls"`
	MinConfidence  float64  `json:"min_confidence"`
	MinProbability float64  `json:"min_probability"`
}

// UnmarshalJSON requires explicit thresholds: an omitted confidence policy must
// not silently become a zero threshold. Programmatic callers use Validate too.
func (c *Config) UnmarshalJSON(data []byte) error {
	type plain Config
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range []string{"min_confidence", "min_probability"} {
		value, ok := fields[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("decision_model: %s must be explicit", field)
		}
	}
	*c = Config(decoded)
	return c.Validate()
}

func (c Config) Validate() error {
	if c.Protocol != "systemone" && c.Protocol != "openai_decisions" {
		return errors.New("decision_model: unsupported protocol")
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("decision_model: endpoint must be an absolute URL without credentials, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && ip != nil && ip.IsLoopback()) {
		return errors.New("decision_model: endpoint requires HTTPS or a loopback IP")
	}
	if strings.TrimSpace(c.Model) == "" || len(c.Model) > 200 || len(c.Endpoint) > 2048 {
		return errors.New("decision_model: bounded model and endpoint are required")
	}
	if c.APIKeyEnv != "" && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(c.APIKeyEnv) {
		return errors.New("decision_model: invalid credential environment name")
	}
	if c.TimeoutMS < 1 || c.TimeoutMS > 120000 || c.MaxCalls < 1 || c.MaxCalls > 64 {
		return errors.New("decision_model: timeout_ms must be 1..120000 and max_calls 1..64")
	}
	if !probability(c.MinConfidence) || !probability(c.MinProbability) {
		return errors.New("decision_model: thresholds must be finite numbers in [0,1]")
	}
	if strings.TrimSpace(c.Instructions) == "" || len(c.Instructions) > 4000 {
		return errors.New("decision_model: instructions must contain 1..4000 bytes")
	}
	seen := map[string]bool{}
	for _, hook := range c.Hooks {
		if (hook != "before_segment" && hook != "before_recovery") || seen[hook] {
			return errors.New("decision_model: unsupported or duplicate hook")
		}
		seen[hook] = true
	}
	if len(seen) == 0 {
		return errors.New("decision_model: at least one hook is required")
	}
	return nil
}
func (c Config) HasHook(hook string) bool {
	for _, h := range c.Hooks {
		if h == hook {
			return true
		}
	}
	return false
}

type Choice struct {
	Value       string `json:"value"`
	Description string `json:"description"`
}
type Request struct {
	ID      string          `json:"id"`
	State   json.RawMessage `json:"state"`
	Choices []Choice        `json:"choices"`
}
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}
type Answer struct {
	Model         string             `json:"model"`
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Usage         *Usage             `json:"usage,omitempty"`
}

func probability(p float64) bool { return !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= 1 }
func (a Answer) Validate(r Request) error {
	if a.Model == "" || len(a.Model) > 200 || !probability(a.Confidence) || len(a.Probabilities) != len(r.Choices) {
		return errors.New("invalid_answer")
	}
	selected, ok := a.Probabilities[a.Choice]
	if !ok {
		return errors.New("invalid_choice")
	}
	sum := 0.0
	for _, option := range r.Choices {
		p, ok := a.Probabilities[option.Value]
		if !ok || !probability(p) || p > selected+1e-6 {
			return errors.New("invalid_probabilities")
		}
		sum += p
	}
	if math.Abs(sum-1) > 0.001 {
		return errors.New("invalid_probabilities")
	}
	if a.Usage != nil && (a.Usage.InputTokens < 0 || a.Usage.OutputTokens < 0) {
		return errors.New("invalid_usage")
	}
	return nil
}
func (a Answer) Meets(c Config) bool {
	return a.Confidence >= c.MinConfidence && a.Probabilities[a.Choice] >= c.MinProbability
}

// Decide makes one bounded request. Transport failures have unknown remote cost;
// no automatic retry or redirect can repeat inference or forward credentials.
func Decide(ctx context.Context, c Config, r Request) (Answer, error) {
	if err := c.Validate(); err != nil {
		return Answer{}, err
	}
	if len(r.Choices) < 2 || len(r.Choices) > 24 || !json.Valid(r.State) || r.ID == "" {
		return Answer{}, errors.New("invalid_request")
	}
	criteria := map[string]string{}
	for _, option := range r.Choices {
		if option.Value == "" || len(option.Value) > 100 || len(option.Description) > 1000 {
			return Answer{}, errors.New("invalid_choices")
		}
		if _, ok := criteria[option.Value]; ok {
			return Answer{}, errors.New("duplicate_choices")
		}
		criteria[option.Value] = option.Description
	}
	var body any
	if c.Protocol == "systemone" {
		body = map[string]any{"model": c.Model, "state": r.State, "questions": map[string]any{"action": map[string]any{"type": "choice", "instructions": c.Instructions, "criteria": criteria}}}
	} else {
		body = map[string]any{"model": c.Model, "input": string(r.State), "questions": []any{map[string]any{"type": "choice", "name": "action", "instructions": c.Instructions, "choices": r.Choices}}}
	}
	data, err := json.Marshal(body)
	if err != nil || len(data) > MaxBytes {
		return Answer{}, errors.New("request_too_large")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(data))
	if err != nil {
		return Answer{}, errors.New("invalid_endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-Request-Id", r.ID)
	if c.APIKeyEnv != "" {
		key := os.Getenv(c.APIKeyEnv)
		if key == "" {
			return Answer{}, errors.New("credential_unavailable")
		}
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Answer{}, errors.New("request_interrupted")
		}
		return Answer{}, errors.New("transport_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Answer{}, fmt.Errorf("http_%d", resp.StatusCode)
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return Answer{}, errors.New("response_interrupted")
	}
	if len(data) > MaxBytes {
		return Answer{}, errors.New("response_too_large")
	}
	return decode(c.Protocol, r, data)
}

func decode(protocol string, r Request, data []byte) (Answer, error) {
	var envelope struct {
		Model   string          `json:"model"`
		Answers json.RawMessage `json:"answers"`
		Usage   *Usage          `json:"usage"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return Answer{}, errors.New("invalid_response")
	}
	var raw struct {
		Type          string          `json:"type"`
		Name          string          `json:"name"`
		Choice        string          `json:"choice"`
		Confidence    *float64        `json:"confidence"`
		Probabilities json.RawMessage `json:"probabilities"`
	}
	if protocol == "systemone" {
		var answers map[string]json.RawMessage
		if json.Unmarshal(envelope.Answers, &answers) != nil || len(answers) != 1 || json.Unmarshal(answers["action"], &raw) != nil {
			return Answer{}, errors.New("invalid_answers")
		}
	} else {
		var answers []json.RawMessage
		if json.Unmarshal(envelope.Answers, &answers) != nil || len(answers) != 1 || json.Unmarshal(answers[0], &raw) != nil || raw.Name != "action" {
			return Answer{}, errors.New("invalid_answers")
		}
	}
	if raw.Type == "refusal" {
		return Answer{}, errors.New("model_refused")
	}
	if raw.Type != "choice" || raw.Confidence == nil {
		return Answer{}, errors.New("invalid_answer")
	}
	a := Answer{Model: envelope.Model, Choice: raw.Choice, Confidence: *raw.Confidence, Probabilities: map[string]float64{}, Usage: envelope.Usage}
	if protocol == "systemone" {
		// Pointers distinguish a missing/null probability from a legitimate zero.
		var ps map[string]*float64
		if json.Unmarshal(raw.Probabilities, &ps) != nil {
			return Answer{}, errors.New("invalid_probabilities")
		}
		for k, p := range ps {
			if p == nil {
				return Answer{}, errors.New("invalid_probabilities")
			}
			a.Probabilities[k] = *p
		}
	} else {
		var ps []struct {
			Value       string   `json:"value"`
			Probability *float64 `json:"probability"`
		}
		if json.Unmarshal(raw.Probabilities, &ps) != nil {
			return Answer{}, errors.New("invalid_probabilities")
		}
		for _, p := range ps {
			if _, duplicate := a.Probabilities[p.Value]; duplicate || p.Probability == nil {
				return Answer{}, errors.New("invalid_probabilities")
			}
			a.Probabilities[p.Value] = *p.Probability
		}
	}
	if err := a.Validate(r); err != nil {
		return Answer{}, err
	}
	return a, nil
}
