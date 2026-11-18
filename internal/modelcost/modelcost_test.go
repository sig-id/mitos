package modelcost

import (
	"math"
	"testing"
)

func TestLookupResolvesFamilies(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"gpt-6.2-sol", "gpt-6.2-sol"},
		{"gpt-5.1-codex-max", "gpt-5.1"},
		{"gpt-5.1-codex", "gpt-5.1"},
		{"openai/gpt-5.2", "gpt-5.2"},
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-6"},
		{"anthropic/claude-haiku-4-5", "claude-haiku-4-5"},
		{"anthropic.claude-opus-4-6-v1:0", "claude-opus-5"},
		{"claude-mythos-5-1", "claude-fable-5-1"},
		{"google/gemini-3.8-flash", "gemini-3.8-flash"},
		{"gemini-2.5-flash-lite", "gemini-2.5-flash-lite"},
	}
	for _, c := range cases {
		price, ok := Lookup(c.model)
		if !ok || price.Model != c.want {
			t.Fatalf("Lookup(%q) = %v, %v; want %s", c.model, price, ok, c.want)
		}
	}
}

func TestLookupPrefersLongestPrefix(t *testing.T) {
	price, ok := Lookup("gpt-5.2-pro")
	if !ok || price.Model != "gpt-5.2-pro" {
		t.Fatalf("gpt-5.2-pro should match its own entry, got %v %v", price, ok)
	}
	price, ok = Lookup("gpt-5.5-pro-preview")
	if !ok || price.Model != "gpt-5.5-pro" {
		t.Fatalf("gpt-5.5-pro-preview should match gpt-5.5-pro, got %v %v", price, ok)
	}
	if _, ok := Lookup("claude-3-opus-20240229"); ok {
		t.Fatal("unpublished model must not resolve")
	}
	if _, ok := Lookup(""); ok {
		t.Fatal("empty model must not resolve")
	}
	if _, ok := Lookup("aider/fixture"); ok {
		t.Fatal("unknown model must not resolve")
	}
}

func TestCostSplitsCacheHitMissAndWrite(t *testing.T) {
	price, _ := Lookup("claude-sonnet-4-5")
	got := price.CostUSD(Usage{InputTokens: 1_000_000, CachedInputTokens: 2_000_000, CacheWriteTokens: 3_000_000, OutputTokens: 1_000_000})
	// 1*3 + 2*0.30 + 3*3.75 + 1*15 = 29.85 USD
	if math.Abs(got-29.85) > 1e-9 {
		t.Fatalf("cost = %v, want 29.85", got)
	}
	openai, _ := Lookup("gpt-6.2-sol")
	got = openai.CostUSD(Usage{InputTokens: 900_000, CachedInputTokens: 100_000, OutputTokens: 1_000_000})
	// 0.9*2 + 0.1*0.10 + 1*10 = 11.81 USD
	if math.Abs(got-11.81) > 1e-9 {
		t.Fatalf("openai cost = %v, want 11.81", got)
	}
	if (Usage{}).IsZero() != true || (Usage{InputTokens: 1}).IsZero() != false {
		t.Fatal("IsZero misreports")
	}
}

func TestTableIntegrity(t *testing.T) {
	prefixes := map[string]string{}
	for _, e := range published {
		if e.price.Input <= 0 || e.price.Output <= 0 || e.price.CachedInput < 0 || e.price.CacheWrite < 0 {
			t.Fatalf("%s has invalid rates %+v", e.price.Model, e.price)
		}
		if e.price.CachedInput > e.price.Input {
			t.Fatalf("%s cache hit costs more than uncached input", e.price.Model)
		}
		for _, prefix := range e.match {
			if prior, dup := prefixes[prefix]; dup {
				t.Fatalf("duplicate match prefix %q (%s, %s)", prefix, prior, e.price.Model)
			}
			prefixes[prefix] = e.price.Model
			if normalize(prefix) != prefix {
				t.Fatalf("prefix %q is not normalized", prefix)
			}
		}
	}
	if len(Sources()) != 3 {
		t.Fatalf("expected three provider sources, got %v", Sources())
	}
	if RetrievedAt().IsZero() {
		t.Fatal("retrieved date must parse")
	}
}
