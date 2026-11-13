package supervision

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelEvidenceKeepsActionMetadata(t *testing.T) {
	for _, kind := range []string{"commandExecution", "fileChange", "mcpToolCall"} {
		t.Run(kind, func(t *testing.T) {
			content := strings.Repeat("\"<&\n", 40000)
			item := map[string]any{"type": kind, "command": "cat secret", "cwd": "/workspace", "arguments": map[string]string{"path": "/outside"}, "additionalPermissions": map[string]string{"network": "yes"}}
			item["aggregatedOutput"] = content
			if kind != "commandExecution" {
				delete(item, "aggregatedOutput")
			}
			if kind == "fileChange" {
				item["changes"] = []any{map[string]string{"path": "/outside/first", "diff": content}, map[string]string{"path": "../second", "diff": content}}
			}
			if kind == "mcpToolCall" {
				item["result"] = map[string]string{"text": content}
			}
			raw, _ := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"item": item}})
			original := append([]byte(nil), raw...)
			bounded := ModelEvidence(raw)
			if !bytes.Equal(raw, original) {
				t.Fatal("modified full evidence")
			}
			if !json.Valid(bounded) || len(bounded) > 24<<10 || !bytes.Contains(bounded, []byte(`"omitted":true`)) {
				t.Fatalf("not bounded: %d", len(bounded))
			}
			var event struct {
				Params struct{ Item map[string]json.RawMessage }
			}
			if err := json.Unmarshal(bounded, &event); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"command", "cwd", "arguments", "additionalPermissions"} {
				want, _ := json.Marshal(item[key])
				if !bytes.Equal(want, event.Params.Item[key]) {
					t.Fatalf("metadata changed: %s", key)
				}
			}
			if kind == "fileChange" && (!bytes.Contains(bounded, []byte("/outside/first")) || !bytes.Contains(bounded, []byte("../second"))) {
				t.Fatal("lost target path")
			}
		})
	}
}
