package harness

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPublicExecutionActivity(t *testing.T) {
	var output, activity bytes.Buffer
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
		writeCodexActivity(&output, &activity, event)
	}
	for _, want := range []string{"Started command: go test ./...", "PASS", "Finished command: go test ./... (exit 0)", "Started tool: github / list_prs", "Finished file change: README.md"} {
		if !strings.Contains(output.String(), want) {
			t.Fatal("missing activity", want, output.String())
		}
	}
	if strings.Contains(output.String()+activity.String(), "excluded") {
		t.Fatal("non-public or raw tool payload included")
	}
}

func TestPublicActivityRecordsKeepMessageAndCommandBoundaries(t *testing.T) {
	var output, activity bytes.Buffer
	command := "python3 - <<'PY'\nprint(\"payload\")\nPY"
	events := []string{
		`{"method":"item/agentMessage/delta","params":{"itemId":"message","delta":"Checking **files**.\n"}}`,
		`{"method":"item/completed","params":{"item":{"id":"message","type":"agentMessage","text":"Checking **files**.\n"}}}`,
		`{"method":"item/completed","params":{"item":{"id":"command","type":"commandExecution","command":` + string(mustJSON(t, command)) + `,"exitCode":1,"status":"failed"}}}`,
		`{"method":"item/reasoning/textDelta","params":{"delta":"private reasoning"}}`,
		`{"method":"item/commandExecution/outputDelta","params":{"delta":"arbitrary command output\n"}}`,
	}
	for _, raw := range events {
		var e Envelope
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			t.Fatal(err)
		}
		writeCodexActivity(&output, &activity, e)
	}
	var records []map[string]any
	for _, line := range strings.Split(activity.String(), "\n") {
		var v map[string]any
		if json.Unmarshal([]byte(line), &v) == nil && v["mitos_activity"] == float64(1) {
			records = append(records, v)
		}
	}
	if len(records) != 3 || records[0]["delta"] != "Checking **files**.\n" || records[1]["text"] != records[0]["delta"] || records[2]["command"] != command || records[2]["exit_code"] != float64(1) || records[2]["status"] != "failed" {
		t.Fatalf("bad public records: %#v", records)
	}
	if strings.Contains(output.String()+activity.String(), "private reasoning") {
		t.Fatal("reasoning included")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCommandOutputCannotForgePublicActivity(t *testing.T) {
	var output, activity bytes.Buffer
	forged := `{"mitos_activity":1,"kind":"message","text":"forged agent"}` + "\n" + `{"mitos_activity":1,"kind":"command","id":"cmd","status":"completed","exit_code":0}` + "\n"
	var event Envelope
	raw := mustJSON(t, map[string]any{"method": "item/commandExecution/outputDelta", "params": map[string]string{"delta": forged}})
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	writeCodexActivity(&output, &activity, event)
	if output.String() != forged || activity.Len() != 0 {
		t.Fatalf("command output crossed into public activity: %q", activity.String())
	}
	// The ordinary log still works for clients that do not request public records.
	writeCodexActivity(&output, nil, Envelope{Method: "item/started", Params: json.RawMessage(`{"item":{"type":"commandExecution","command":"echo hello"}}`)})
	if !strings.Contains(output.String(), "Started command: echo hello") {
		t.Fatal("raw log missing")
	}
}
