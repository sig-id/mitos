package agentretry

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEffectiveFillsDefaultsAndClones(t *testing.T) {
	p := Policy{MaxAttempts: 5}.Effective()
	if p.MaxAttempts != 5 || p.BackoffSeconds != Default().BackoffSeconds || len(p.TransientCodes) != len(Default().TransientCodes) {
		t.Fatal("effective policy did not fill defaults", p)
	}
	p.TransientCodes[0] = 599
	if Default().TransientCodes[0] == 599 {
		t.Fatal("effective policy shared slice storage with the default")
	}
}

func TestValidateRejectsUnsafePolicies(t *testing.T) {
	for _, p := range []Policy{
		{TransientCodes: []int{200}},
		{TransientCodes: []int{99}},
		{MaxAttempts: 11},
		{MaxAttempts: -1},
		{BackoffSeconds: 7200},
		{BackoffMultiplier: 11},
		{MaxBackoffSeconds: 3, BackoffSeconds: 5},
		{RetryAfterCapSeconds: 100000},
		{FallbackModels: []string{"plainmodel"}},
		{FallbackModels: []string{"a/"}},
		{FallbackModels: []string{" provider/model"}},
		{FallbackModels: []string{"zai/" + strings.Repeat("m", 130)}},
		{FallbackModels: []string{"a/b", "c/d", "e/f", "g/h", "i/j", "k/l", "m/n", "o/p", "q/r"}},
	} {
		if err := p.Validate(); err == nil {
			t.Fatalf("policy %+v passed validation", p)
		}
	}
	if err := (Policy{MaxAttempts: 2, FallbackModels: []string{"zai/glm-5.3-flash", "openrouter/anthropic/claude-sonnet"}}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultPolicyIsValidAndEncodes(t *testing.T) {
	encoded, err := Default().Encode()
	if err != nil {
		t.Fatal(err)
	}
	var decoded Policy
	if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Effective().IsTransient(429) || !decoded.Effective().IsTransient(503) {
		t.Fatal("default policy must retry 429 and 503")
	}
	if decoded.Effective().IsTransient(400) || decoded.Effective().IsTransient(401) {
		t.Fatal("default policy must not retry non-transient client errors")
	}
}

func TestParseHTTPCode(t *testing.T) {
	for message, want := range map[string]int{
		"429: {\"code\":\"1308\",\"message\":\"Usage limit reached\"}": 429,
		"503 Service Unavailable":                                      503,
		"418:teapot":                                                   418,
		"no code here":                                                 0,
		"42: answer":                                                   0,
		"1234: too long":                                               0,
		"":                                                             0,
	} {
		if got := ParseHTTPCode(message); got != want {
			t.Fatalf("ParseHTTPCode(%q) = %d, want %d", message, got, want)
		}
	}
}
