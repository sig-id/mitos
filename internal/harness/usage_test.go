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
