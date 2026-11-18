package harness

import "encoding/json"

// UsageTracker accumulates usage reports for one attempt. Cumulative reports
// (Codex session totals) replace the totals so repeated notifications cannot
// double-count a turn; per-call reports (Claude assistant messages) add. The
// resolved model is the last one the harness named, or the requested model
// when the harness never named one.
type UsageTracker struct {
	fallback string
	model    string
	usage    Usage
	reported bool
}

func NewUsageTracker(requestedModel string) *UsageTracker {
	return &UsageTracker{fallback: requestedModel}
}

func (t *UsageTracker) Observe(report Usage) {
	if report.Model != "" {
		t.model = report.Model
	}
	if report.Cumulative {
		t.usage = report
	} else {
		t.usage = t.usage.Add(report)
	}
	t.reported = true
}

// Snapshot returns the accumulated totals and the resolved model. ok is false
// when no report arrived; absent usage is unknown, not zero.
func (t *UsageTracker) Snapshot() (model string, usage Usage, ok bool) {
	model = t.model
	if model == "" {
		model = t.fallback
	}
	return model, t.usage, t.reported
}

// Usage is normalized model consumption reported by a native harness. The
// splits are provider-reconciled before costing: InputTokens never includes
// cache hits or cache writes, CachedInputTokens counts cache hits, and
// CacheWriteTokens counts tokens written to an explicit cache premium (zero
// where the provider charges no write premium). Cumulative marks harness
// reports that carry session totals rather than a per-call delta, so
// accumulators replace instead of add and repeated notifications cannot
// double-count one turn.
type Usage struct {
	Model             string `json:"model,omitempty"`
	InputTokens       int64  `json:"input_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	CacheWriteTokens  int64  `json:"cache_write_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	Cumulative        bool   `json:"cumulative,omitempty"`
}

// Add sums usage from independent reports.
func (u Usage) Add(other Usage) Usage {
	u.InputTokens += other.InputTokens
	u.CachedInputTokens += other.CachedInputTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.OutputTokens += other.OutputTokens
	return u
}

// CodexUsage decodes token-usage notifications from the Codex app-server:
// the current `thread/tokenUsage/updated` notification and the legacy
// `tokenCount` notification. Only `total` usage is forwarded, since it is the
// cumulative session total; `last` is the per-turn delta already contained in
// it. Codex reports input inclusive of cache hits, so the uncached remainder
// is input minus cached. Model resolution stays with the caller: the
// notification carries no model field.
func CodexUsage(method string, params json.RawMessage) (Usage, bool) {
	switch method {
	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total struct {
					InputTokens       int64 `json:"inputTokens"`
					CachedInputTokens int64 `json:"cachedInputTokens"`
					CacheWriteTokens  int64 `json:"cacheWriteInputTokens"`
					OutputTokens      int64 `json:"outputTokens"`
					ReasoningTokens   int64 `json:"reasoningOutputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(params, &p) != nil {
			return Usage{}, false
		}
		t := p.TokenUsage.Total
		if t.InputTokens == 0 && t.CachedInputTokens == 0 && t.CacheWriteTokens == 0 && t.OutputTokens == 0 {
			return Usage{}, false
		}
		return Usage{
			InputTokens:       max(t.InputTokens-t.CachedInputTokens, 0),
			CachedInputTokens: t.CachedInputTokens,
			CacheWriteTokens:  t.CacheWriteTokens,
			OutputTokens:      t.OutputTokens,
			Cumulative:        true,
		}, true
	case "tokenCount":
		var p struct {
			Info struct {
				TotalTokenUsage *struct {
					InputTokens       int64 `json:"input_tokens"`
					CachedInputTokens int64 `json:"cached_input_tokens"`
					CacheWriteTokens  int64 `json:"cache_creation_input_tokens"`
					OutputTokens      int64 `json:"output_tokens"`
					ReasoningTokens   int64 `json:"reasoning_output_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
		}
		if json.Unmarshal(params, &p) != nil || p.Info.TotalTokenUsage == nil {
			return Usage{}, false
		}
		t := p.Info.TotalTokenUsage
		if t.InputTokens == 0 && t.CachedInputTokens == 0 && t.CacheWriteTokens == 0 && t.OutputTokens == 0 {
			return Usage{}, false
		}
		return Usage{
			InputTokens:       max(t.InputTokens-t.CachedInputTokens, 0),
			CachedInputTokens: t.CachedInputTokens,
			CacheWriteTokens:  t.CacheWriteTokens,
			OutputTokens:      t.OutputTokens,
			Cumulative:        true,
		}, true
	}
	return Usage{}, false
}

// ClaudeUsage decodes the usage block of a Claude Code stream-json assistant
// frame. Anthropic reports input_tokens exclusive of cache traffic, so the
// fields already match the normalized Usage split; cache_creation may appear
// as a legacy flat total or the current object form. Each assistant message
// reports one API call, so reports are deltas, not cumulative totals.
func ClaudeUsage(frame json.RawMessage) (Usage, bool) {
	var envelope struct {
		Message struct {
			Model string `json:"model"`
			Usage *struct {
				InputTokens              int64 `json:"input_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
				CacheCreation            *struct {
					Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
					Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(frame, &envelope) != nil || envelope.Message.Usage == nil {
		return Usage{}, false
	}
	u := envelope.Message.Usage
	if u.InputTokens == 0 && u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 && u.OutputTokens == 0 && u.CacheCreation == nil {
		return Usage{}, false
	}
	write := u.CacheCreationInputTokens
	if u.CacheCreation != nil {
		write = u.CacheCreation.Ephemeral5m + u.CacheCreation.Ephemeral1h
	}
	return Usage{
		Model:             envelope.Message.Model,
		InputTokens:       u.InputTokens,
		CachedInputTokens: u.CacheReadInputTokens,
		CacheWriteTokens:  write,
		OutputTokens:      u.OutputTokens,
	}, true
}
