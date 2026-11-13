package harness

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicExecutionActivity(t *testing.T) {
	var output bytes.Buffer
	for _, raw := range []string{
		`{"method":"item/started","params":{"item":{"type":"commandExecution","command":"go test ./..."}}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"delta":"PASS\n"}}`,
		`{"method":"item/completed","params":{"item":{"type":"commandExecution","command":"go test ./...","exitCode":0}}}`,
		`{"method":"item/started","params":{"item":{"type":"mcpToolCall","server":"github","tool":"list_prs","arguments":{"secret":"excluded"}}}}`,
		`{"method":"item/completed","params":{"item":{"type":"fileChange","changes":[{"path":"README.md","diff":"excluded"}]}}}`,
		`{"method":"item/reasoning/textDelta","params":{"delta":"excluded"}}`,
		`{"method":"item/started","params":{"item":{"type":"reasoning","text":"excluded"}}}`,
	} {
		var event Envelope
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		writeCodexActivity(&output, event)
	}
	for _, want := range []string{"Started command: go test ./...", "PASS", "Finished command: go test ./... (exit 0)", "Started tool: github / list_prs", "Finished file change: README.md"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal("missing activity", want, output.String())
		}
	}
	if strings.Contains(output.String(), "excluded") {
		t.Fatal("non-public or raw tool payload included")
	}
}
