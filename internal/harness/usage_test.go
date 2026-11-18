package harness

import (
	"encoding/json"
	"testing"
)

func TestCodexUsageCurrentNotification(t *testing.T) {
	params := json.RawMessage(`{"threadId":"th_1","turnId":"tu_1","tokenUsage":{
		"total":{"totalTokens":4230,"inputTokens":3000,"cachedInputTokens":2000,"cacheWriteInputTokens":500,"outputTokens":1230,"reasoningOutputTokens":300},
		"last":{"totalTokens":4230,"inputTokens":3000,"cachedInputTokens":2000,"cacheWriteInputTokens":500,"outputTokens":1230,"reasoningOutputTokens":300},
		"modelContextWindow":272000}}`)
	usage, ok := CodexUsage("thread/tokenUsage/updated", params)
	if !ok {
		t.Fatal("usage notification not recognized")
	}
	if !usage.Cumulative || usage.InputTokens != 1000 || usage.CachedInputTokens != 2000 || usage.CacheWriteTokens != 500 || usage.OutputTokens != 1230 {
		t.Fatalf("unexpected usage %+v", usage)
	}
	if usage.Model != "" {
		t.Fatal("codex notifications carry no model; the caller resolves it")
	}
	if _, ok := CodexUsage("thread/tokenUsage/updated", json.RawMessage(`{"tokenUsage":{"total":{"totalTokens":0}}}`)); ok {
		t.Fatal("an all-zero report is not usage evidence")
	}
	if _, ok := CodexUsage("item/started", params); ok {
		t.Fatal("unrelated methods must not parse as usage")
	}
	if _, ok := CodexUsage("thread/tokenUsage/updated", json.RawMessage(`{`)); ok {
		t.Fatal("malformed params must not parse")
	}
}

func TestCodexUsageLegacyTokenCount(t *testing.T) {
	params := json.RawMessage(`{"info":{"last_token_usage":{"input_tokens":1500,"cached_input_tokens":1000,"output_tokens":40},
		"total_token_usage":{"input_tokens":3500,"cached_input_tokens":2000,"cache_creation_input_tokens":250,"output_tokens":900,"reasoning_output_tokens":100,"total_tokens":4400}}}`)
	usage, ok := CodexUsage("tokenCount", params)
	if !ok {
		t.Fatal("legacy tokenCount not recognized")
	}
	if !usage.Cumulative || usage.InputTokens != 1500 || usage.CachedInputTokens != 2000 || usage.CacheWriteTokens != 250 || usage.OutputTokens != 900 {
		t.Fatalf("unexpected usage %+v", usage)
	}
	if _, ok := CodexUsage("tokenCount", json.RawMessage(`{"info":{}}`)); ok {
		t.Fatal("missing totals must not parse")
	}
}

func TestUsageTrackerBaselineSubtractsOnlyCumulativeReports(t *testing.T) {
	tracker := NewUsageTracker("gpt-6.2-sol")
	// An earlier attempt on the same native thread recorded these totals.
	tracker.Baseline(Usage{InputTokens: 8_000, CachedInputTokens: 20_000, OutputTokens: 8_000})
	first, ok := CodexUsage("thread/tokenUsage/updated", json.RawMessage(`{"tokenUsage":{"total":{"inputTokens":30200,"cachedInputTokens":21000,"outputTokens":9500,"totalTokens":39700}}}`))
	if !ok {
		t.Fatal("cumulative report must parse")
	}
	tracker.Observe(first)
	model, usage, ok := tracker.Snapshot()
	if !ok || model != "gpt-6.2-sol" || usage.InputTokens != 1_200 || usage.CachedInputTokens != 1_000 || usage.OutputTokens != 1_500 {
		t.Fatalf("resumed thread re-billed prior consumption: %+v", usage)
	}
	// A later cumulative report that still fits inside the baseline clamps at
	// zero instead of going negative.
	tracker.Observe(Usage{Cumulative: true, InputTokens: 500, CachedInputTokens: 100, OutputTokens: 10})
	_, usage, _ = tracker.Snapshot()
	if usage.InputTokens != 0 || usage.CachedInputTokens != 0 || usage.OutputTokens != 0 {
		t.Fatalf("baseline subtraction went negative: %+v", usage)
	}
	// Per-call reports are deltas already and must never be adjusted.
	fresh := NewUsageTracker("claude-sonnet-4-5")
	fresh.Baseline(Usage{InputTokens: 100_000})
	fresh.Observe(Usage{Model: "claude-sonnet-4-5", InputTokens: 413, OutputTokens: 50})
	_, usage, ok = fresh.Snapshot()
	if !ok || usage.InputTokens != 413 || usage.OutputTokens != 50 {
		t.Fatalf("per-call report was wrongly baselined: %+v", usage)
	}
}

func TestClaudeUsageAssistantFrame(t *testing.T) {
	frame := json.RawMessage(`{"type":"assistant","session_id":"s1","message":{"id":"msg_1","model":"claude-sonnet-4-5-20250929",
		"content":[{"type":"text","text":"hi"}],
		"usage":{"input_tokens":413,"cache_creation":{"ephemeral_5m_input_tokens":18521,"ephemeral_1h_input_tokens":0},"cache_read_input_tokens":38892,"output_tokens":298,"server_tool_use":{"web_search_requests":1}}}}`)
	usage, ok := ClaudeUsage(frame)
	if !ok {
		t.Fatal("assistant usage frame not recognized")
	}
	if usage.Cumulative {
		t.Fatal("claude reports per-call deltas")
	}
	if usage.Model != "claude-sonnet-4-5-20250929" || usage.InputTokens != 413 || usage.CacheWriteTokens != 18521 || usage.CachedInputTokens != 38892 || usage.OutputTokens != 298 {
		t.Fatalf("unexpected usage %+v", usage)
	}
	legacy := json.RawMessage(`{"type":"assistant","message":{"id":"msg_2","model":"claude-haiku-4-5","content":[],"usage":{"input_tokens":10,"cache_creation_input_tokens":100,"cache_read_input_tokens":200,"output_tokens":5}}}`)
	usage, ok = ClaudeUsage(legacy)
	if !ok || usage.CacheWriteTokens != 100 || usage.CachedInputTokens != 200 || usage.Model != "claude-haiku-4-5" {
		t.Fatalf("legacy flat cache_creation not parsed: %+v %v", usage, ok)
	}
	if _, ok := ClaudeUsage(json.RawMessage(`{"type":"user","message":{"content":[{"type":"tool_result"}]}}`)); ok {
		t.Fatal("frames without usage must not parse")
	}
}
