// Package modelcost estimates model spend from harness-reported token usage
// against published provider list prices. Estimates are telemetry, never a
// spending guarantee: subscription plans, batch/flex discounts, priority
// speeds, long-context tiers, and regional uplifts are out of scope, and
// unpriced models are reported as unpriced rather than free.
package modelcost

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Retrieved is the date the embedded list prices were published or fetched
// from the cited pricing pages. Prices change; the date travels with every
// estimate so stale tables cannot masquerade as current ones.
const Retrieved = "2026-11-18"

const (
	openaiSource    = "OpenAI API pricing, developers.openai.com/api/docs/pricing (standard tier)"
	anthropicSource = "Claude Platform docs, prompt caching pricing, platform.claude.com (base tier, 5-minute cache writes)"
	geminiSource    = "Gemini API pricing, ai.google.dev/gemini-api/docs/pricing (standard tier, current promotional rates)"
)

// Price is a per-million-token USD list price. CachedInput is the cache-hit
// input rate; CacheWrite is the explicit cache-write rate where the provider
// charges one (Anthropic 5-minute writes, OpenAI 30-minute writes). A zero
// CacheWrite means the provider bills no write premium for that model.
type Price struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	Input       float64 `json:"input_per_mtok"`
	CachedInput float64 `json:"cached_input_per_mtok"`
	CacheWrite  float64 `json:"cache_write_per_mtok"`
	Output      float64 `json:"output_per_mtok"`
	Source      string  `json:"source"`
	Note        string  `json:"note,omitempty"`
}

// Usage is normalized token consumption. Input excludes cached and cache-write
// tokens regardless of provider accounting: OpenAI-style totals report input
// inclusive of cache hits, Anthropic-style usage reports it exclusive, and the
// harness parsers reconcile both into these fields before costing.
type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	CacheWriteTokens  int64 `json:"cache_write_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

func (u Usage) IsZero() bool {
	return u == Usage{}
}

// Add sums usage from independent reports.
func (u Usage) Add(other Usage) Usage {
	u.InputTokens += other.InputTokens
	u.CachedInputTokens += other.CachedInputTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.OutputTokens += other.OutputTokens
	return u
}

// CostUSD applies the price to the usage split. Cache hits cost the cached
// rate, cache writes the write premium, and remaining input the base rate.
func (p Price) CostUSD(u Usage) float64 {
	const perMillion = 1e6
	return float64(u.InputTokens)*p.Input/perMillion +
		float64(u.CachedInputTokens)*p.CachedInput/perMillion +
		float64(u.CacheWriteTokens)*p.CacheWrite/perMillion +
		float64(u.OutputTokens)*p.Output/perMillion
}

type entry struct {
	match []string // lowercase prefixes, most specific first within the entry
	price Price
}

// published holds the embedded list prices. Match prefixes are unique across
// entries; Lookup takes the longest matching prefix so dated or suffixed
// variants (claude-sonnet-4-5-20250929, gpt-5.1-codex-max) resolve to their
// family entry.
var published = []entry{
	// OpenAI, standard tier. Auto-cached requests bill cache hits at the
	// cached rate; gpt-6/5.6 add explicit-cache write rates (30-minute tier).
	{[]string{"gpt-6.2-astra"}, Price{"openai", "gpt-6.2-astra", 8, 0.40, 10, 40, openaiSource, ""}},
	{[]string{"gpt-6.2-sol"}, Price{"openai", "gpt-6.2-sol", 2, 0.10, 2.50, 10, openaiSource, ""}},
	{[]string{"gpt-6-astra"}, Price{"openai", "gpt-6-astra", 10, 1, 12.50, 50, openaiSource, ""}},
	{[]string{"gpt-6.1-sol"}, Price{"openai", "gpt-6.1-sol", 2, 0.10, 2.50, 10, openaiSource, ""}},
	{[]string{"gpt-6.1-terra"}, Price{"openai", "gpt-6.1-terra", 1, 0.05, 1.25, 5, openaiSource, ""}},
	{[]string{"gpt-6-luna"}, Price{"openai", "gpt-6-luna", 0.10, 0.01, 0.125, 0.50, openaiSource, ""}},
	{[]string{"gpt-6-sol"}, Price{"openai", "gpt-6-sol", 2, 0.20, 2.50, 10, openaiSource, ""}},
	{[]string{"gpt-5.6-sol"}, Price{"openai", "gpt-5.6-sol", 4, 0.40, 5, 20, openaiSource, "Promotional rates published through November 30, 2026."}},
	{[]string{"gpt-5.6-terra"}, Price{"openai", "gpt-5.6-terra", 2, 0.20, 2.50, 12, openaiSource, ""}},
	{[]string{"gpt-5.6-luna"}, Price{"openai", "gpt-5.6-luna", 0.20, 0.02, 0.25, 1.20, openaiSource, ""}},
	{[]string{"gpt-5.5-pro"}, Price{"openai", "gpt-5.5-pro", 30, 0, 0, 180, openaiSource, "No published cached-input rate."}},
	{[]string{"gpt-5.5"}, Price{"openai", "gpt-5.5", 5, 0.50, 0, 30, openaiSource, "Rate for prompts up to 272K tokens."}},
	{[]string{"gpt-5.4-pro"}, Price{"openai", "gpt-5.4-pro", 30, 0, 0, 180, openaiSource, "No published cached-input rate."}},
	{[]string{"gpt-5.4-mini"}, Price{"openai", "gpt-5.4-mini", 0.75, 0.075, 0, 4.50, openaiSource, ""}},
	{[]string{"gpt-5.4"}, Price{"openai", "gpt-5.4", 2.50, 0.25, 0, 15, openaiSource, "Rate for prompts up to 272K tokens."}},
	{[]string{"gpt-5.2-pro"}, Price{"openai", "gpt-5.2-pro", 21, 0, 0, 168, openaiSource, "No published cached-input rate."}},
	{[]string{"gpt-5.2"}, Price{"openai", "gpt-5.2", 1.75, 0.175, 0, 14, openaiSource, ""}},
	{[]string{"gpt-5.1", "gpt-5-codex"}, Price{"openai", "gpt-5.1", 1.25, 0.125, 0, 10, openaiSource, "Deprecated by the provider."}},
	{[]string{"gpt-5-pro"}, Price{"openai", "gpt-5-pro", 15, 0, 0, 120, openaiSource, "Deprecated; no published cached-input rate."}},
	{[]string{"gpt-5-mini"}, Price{"openai", "gpt-5-mini", 0.25, 0.025, 0, 2, openaiSource, "Deprecated by the provider."}},
	{[]string{"gpt-5-nano"}, Price{"openai", "gpt-5-nano", 0.05, 0.005, 0, 0.40, openaiSource, "Deprecated by the provider."}},
	{[]string{"gpt-5"}, Price{"openai", "gpt-5", 1.25, 0.125, 0, 10, openaiSource, "Deprecated by the provider."}},
	{[]string{"gpt-4.1-mini"}, Price{"openai", "gpt-4.1-mini", 0.40, 0.10, 0, 1.60, openaiSource, "Deprecated by the provider."}},
	{[]string{"gpt-4.1"}, Price{"openai", "gpt-4.1", 2, 0.50, 0, 8, openaiSource, "Deprecated by the provider."}},

	// Anthropic, base tier. Cache writes are 5-minute TTL rates; 1-hour writes
	// cost twice the base input rate. Claude Code writes 5-minute entries.
	{[]string{"claude-fable-5-2", "claude-mythos-5-2"}, Price{"anthropic", "claude-fable-5-2", 8, 0.20, 10, 40, anthropicSource, "Cache reads at 0.025x input. Mythos is verification-gated with identical pricing."}},
	{[]string{"claude-opus-5-5"}, Price{"anthropic", "claude-opus-5-5", 4, 0.20, 5, 20, anthropicSource, "Cache reads at 0.05x input."}},
	{[]string{"claude-sonnet-5-5"}, Price{"anthropic", "claude-sonnet-5-5", 2, 0.10, 2.50, 10, anthropicSource, "Cache reads at 0.05x input."}},
	{[]string{"claude-haiku-5-5"}, Price{"anthropic", "claude-haiku-5-5", 0.10, 0.01, 0.125, 0.50, anthropicSource, "Rates for prompts up to 100K tokens; longer prompts use a higher tier."}},
	{[]string{"claude-fable-5-1", "claude-mythos-5-1"}, Price{"anthropic", "claude-fable-5-1", 10, 0.25, 12.50, 50, anthropicSource, "Cache reads at 0.025x input. Mythos is verification-gated with identical pricing."}},
	{[]string{"claude-fable-5", "claude-mythos-5"}, Price{"anthropic", "claude-fable-5", 10, 1, 12.50, 50, anthropicSource, ""}},
	{[]string{"claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5"}, Price{"anthropic", "claude-opus-5", 5, 0.50, 6.25, 25, anthropicSource, "Shared Opus rate for versions 4.5 through 5."}},
	{[]string{"claude-opus-4-1", "claude-opus-4"}, Price{"anthropic", "claude-opus-4", 15, 1.50, 18.75, 75, anthropicSource, "Retired by the provider."}},
	{[]string{"claude-sonnet-5"}, Price{"anthropic", "claude-sonnet-5", 2, 0.20, 2.50, 10, anthropicSource, ""}},
	{[]string{"claude-sonnet-4-6", "claude-sonnet-4-5", "claude-sonnet-4"}, Price{"anthropic", "claude-sonnet-4-6", 3, 0.30, 3.75, 15, anthropicSource, "Shared Sonnet rate for versions 4 through 4.6."}},
	{[]string{"claude-haiku-4-5"}, Price{"anthropic", "claude-haiku-4-5", 1, 0.10, 1.25, 5, anthropicSource, ""}},

	// Google, standard tier. Cached price replaces the input price for cache
	// hits; explicit cache storage is billed per token-hour and is not modeled.
	{[]string{"gemini-4-carbon", "gemini-4-argon"}, Price{"google", "gemini-4-carbon", 2, 0.10, 0, 10, geminiSource, "Promotional rates published through November 30, 2026; higher from December 1, 2026."}},
	{[]string{"gemini-4-hydrogen"}, Price{"google", "gemini-4-hydrogen", 1.50, 0.075, 0, 7.50, geminiSource, ""}},
	{[]string{"gemini-3.8-flash"}, Price{"google", "gemini-3.8-flash", 0.75, 0.075, 0, 3.75, geminiSource, "Promotional rates published through December 31, 2026; higher from January 1, 2027."}},
	{[]string{"gemini-3.1-pro"}, Price{"google", "gemini-3.1-pro", 2, 0.20, 0, 12, geminiSource, "Rates for prompts up to 200K tokens; longer prompts use a higher tier."}},
	{[]string{"gemini-3-flash"}, Price{"google", "gemini-3-flash", 0.50, 0.05, 0, 3, geminiSource, ""}},
	{[]string{"gemini-2.5-pro"}, Price{"google", "gemini-2.5-pro", 1.25, 0.125, 0, 10, geminiSource, "Rates for prompts up to 200K tokens; longer prompts use a higher tier."}},
	{[]string{"gemini-2.5-flash-lite"}, Price{"google", "gemini-2.5-flash-lite", 0.10, 0.01, 0, 0.40, geminiSource, ""}},
	{[]string{"gemini-2.5-flash"}, Price{"google", "gemini-2.5-flash", 0.30, 0.03, 0, 2.50, geminiSource, ""}},
}

var providerPrefix = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,32}/`)

// normalize lowercases a harness-reported model ID and strips routing prefixes
// (openai/gpt-5.2, anthropic.claude-opus-5, google/gemini-3-flash) and Bedrock
// style infixes so published families match what the CLIs actually report.
func normalize(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	for {
		trimmed := providerPrefix.ReplaceAllString(id, "")
		if trimmed == id {
			break
		}
		id = trimmed
	}
	id = strings.TrimPrefix(id, "anthropic.")
	id = strings.TrimPrefix(id, "amazon.")
	if suffix := regexp.MustCompile(`-(20\d{6})$`); suffix.MatchString(id) {
		id = suffix.ReplaceAllString(id, "")
	}
	return id
}

// Lookup resolves a harness-reported model ID to its published list price.
// ok is false for unknown models; callers must report them as unpriced.
func Lookup(model string) (Price, bool) {
	id := normalize(model)
	if id == "" {
		return Price{}, false
	}
	var best entry
	bestLen := -1
	for _, e := range published {
		for _, prefix := range e.match {
			if strings.HasPrefix(id, prefix) && len(prefix) > bestLen {
				best, bestLen = e, len(prefix)
			}
		}
	}
	if bestLen < 0 {
		return Price{}, false
	}
	return best.price, true
}

// RetrievedAt exposes the table's retrieval date for reports.
func RetrievedAt() time.Time { t, _ := time.Parse("2006-01-02", Retrieved); return t.UTC() }

// Sources lists the distinct pricing pages the table cites.
func Sources() []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range published {
		if !seen[e.price.Source] {
			seen[e.price.Source] = true
			out = append(out, e.price.Source)
		}
	}
	sort.Strings(out)
	return out
}
