package supervision

import "encoding/json"

// ModelEvidence excerpts bulky result bodies, never action metadata. The caller
// retains the original event for deterministic checks, identity and audit. If
// metadata alone exceeds the model's request limit, review fails closed as usual.
func ModelEvidence(raw json.RawMessage) json.RawMessage {
	if len(raw) <= 24<<10 {
		return raw
	}
	var event map[string]json.RawMessage
	if json.Unmarshal(raw, &event) != nil {
		return raw
	}
	var params, item map[string]json.RawMessage
	if json.Unmarshal(event["params"], &params) != nil || json.Unmarshal(params["item"], &item) != nil {
		return raw
	}
	var kind string
	_ = json.Unmarshal(item["type"], &kind)
	// Share one output allowance across all changes. Escape expansion remains
	// bounded; unknown fields and all paths/arguments remain verbatim.
	remaining := 2048
	excerpt := func(fields map[string]json.RawMessage, key string) {
		body, ok := fields[key]
		if !ok {
			return
		}
		if len(body) <= remaining {
			remaining -= len(body)
			return
		}
		var value string
		if json.Unmarshal(body, &value) != nil {
			value = string(body)
		}
		runes := []rune(value)
		n := min(len(runes), remaining)
		remaining -= n
		fields[key], _ = json.Marshal(map[string]any{
			"excerpt": string(runes[:n]), "original_bytes": len(body),
			"omitted": true, "note": "Result body excerpted for model review; full event retained in supervision evidence.",
		})
	}
	switch kind {
	case "commandExecution":
		excerpt(item, "aggregatedOutput")
	case "fileChange":
		var changes []map[string]json.RawMessage
		if json.Unmarshal(item["changes"], &changes) != nil {
			return raw
		}
		for _, change := range changes {
			excerpt(change, "diff")
		}
		item["changes"], _ = json.Marshal(changes)
	case "mcpToolCall":
		excerpt(item, "result")
	default:
		return raw
	}
	params["item"], _ = json.Marshal(item)
	event["params"], _ = json.Marshal(params)
	bounded, err := json.Marshal(event)
	if err != nil {
		return raw
	}
	return bounded
}
