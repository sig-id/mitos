package harness

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestPiEventStreamRendersPublicActivity(t *testing.T) {
	var log strings.Builder
	var activity syncBuffer
	stream := NewPiEventStream(&log, &activity)
	events := []string{
		`{"type":"session","version":3,"id":"s1","cwd":"/tmp"}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Checking the file"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"thinking_delta","contentIndex":1,"delta":"private reasoning"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":2,"id":"call_bash","toolName":"bash"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":2,"toolCall":{"type":"toolCall","id":"call_bash","name":"bash","arguments":{"command":"printf checked"}}}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":3,"toolCall":{"type":"toolCall","id":"call_edit","name":"edit","arguments":{"path":"internal/web/static/work-graph.js","replacements":[]}}}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":4,"toolCall":{"type":"toolCall","id":"call_mcp","name":"mcp__mitos__result_submit","arguments":{"result":"done"}}}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The change is in place."}]}}`,
		`{"type":"turn_end","message":{"role":"assistant"},"toolResults":[{"toolCallId":"call_bash","toolName":"bash","isError":false,"details":{"exit_code":0}},{"toolCallId":"call_edit","toolName":"edit","isError":false},{"toolCallId":"call_mcp","toolName":"mcp__mitos__result_submit","isError":false}]}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
		`{"type":"agent_settled","aborted":false}`,
	}
	for _, event := range events {
		if _, err := stream.Write([]byte(event + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	// A trailing line without its newline is still rendered.
	if _, err := stream.Write([]byte("final partial log line")); err != nil {
		t.Fatal(err)
	}
	stream.Flush()
	records := activity.lines()
	if len(records) != 6 {
		t.Fatalf("expected 6 public records, got %d: %v", len(records), records)
	}
	// Deltas and completion share one message identity.
	if records[0]["kind"] != "message" || records[0]["delta"] != "Checking the file" || records[0]["id"] != "pi-1" {
		t.Fatalf("text delta was not recorded as message pi-1: %v", records[0])
	}
	bash := records[1]
	if bash["kind"] != "command" || bash["command"] != "printf checked" || bash["status"] != "running" {
		t.Fatalf("bash call was not announced as a running command: %v", bash)
	}
	edit := records[2]
	if edit["kind"] != "file" || edit["name"] != "internal/web/static/work-graph.js" || edit["status"] != "running" {
		t.Fatalf("edit call was not announced as a file change: %v", edit)
	}
	if final := records[3]; final["kind"] != "message" || final["id"] != "pi-1" || final["text"] != "The change is in place." {
		t.Fatalf("final message must replace the deltas of pi-1: %v", final)
	}
	if done := records[4]; done["kind"] != "command" || done["status"] != "completed" || done["exit_code"] != float64(0) {
		t.Fatalf("bash result was not completed with its exit code: %v", done)
	}
	if saved := records[5]; saved["kind"] != "file" || saved["status"] != "completed" {
		t.Fatalf("edit result was not completed: %v", saved)
	}
	for _, record := range records {
		if name, ok := record["name"].(string); ok && strings.HasPrefix(name, "mcp__mitos__") {
			t.Fatalf("the MCP server's own tools must not be duplicated: %v", record)
		}
	}
	if strings.Contains(activity.text(), "private reasoning") {
		t.Fatal("reasoning deltas must stay private")
	}
	assertLog := func(needle string) {
		t.Helper()
		if !strings.Contains(log.String(), needle) {
			t.Fatalf("execution log is missing %q:\n%s", needle, log.String())
		}
	}
	assertLog("Started command: printf checked")
	assertLog("Finished command: printf checked (exit 0)")
	assertLog("Started file change: internal/web/static/work-graph.js")
	assertLog("The change is in place.")
	assertLog("final partial log line")
}

func TestPiEventStreamNeverRecordsEmptyNames(t *testing.T) {
	var log strings.Builder
	var activity syncBuffer
	stream := NewPiEventStream(&log, &activity)
	// Arguments that do not decode must not produce blank activity rows.
	for _, event := range []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","toolCall":{"id":"call_b","name":"bash","arguments":"not an object"}}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","toolCall":{"id":"call_e","name":"edit","arguments":{}}}}`,
	} {
		if _, err := stream.Write([]byte(event + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	records := activity.lines()
	if len(records) != 2 {
		t.Fatalf("expected one record per call, got %v", records)
	}
	if records[0]["command"] != "bash" {
		t.Fatalf("undecodable bash arguments must fall back to the tool name, got %v", records[0])
	}
	if records[1]["name"] != "edit" {
		t.Fatalf("missing edit path must fall back to the tool name, got %v", records[1])
	}
}

func TestPiEventStreamFailedToolResult(t *testing.T) {
	var log strings.Builder
	var activity syncBuffer
	stream := NewPiEventStream(&log, &activity)
	for _, event := range []string{
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","contentIndex":0,"toolCall":{"id":"call_1","name":"bash","arguments":{"command":"make test"}}}}`,
		`{"type":"turn_end","toolResults":[{"toolCallId":"call_1","toolName":"bash","isError":true,"details":{"exit_code":2}}]}`,
	} {
		if _, err := stream.Write([]byte(event + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	records := activity.lines()
	if len(records) != 2 {
		t.Fatalf("expected announce and result records, got %v", records)
	}
	if records[1]["status"] != "failed" || records[1]["exit_code"] != float64(2) {
		t.Fatalf("failed result must carry its exit code: %v", records[1])
	}
}

func TestPiEventStreamPassesUnknownLinesThrough(t *testing.T) {
	var log strings.Builder
	var activity syncBuffer
	stream := NewPiEventStream(&log, &activity)
	for _, chunk := range []string{"cli crashed: ", "{\"type\":\"session\" has no id}\n", "not json at all\n"} {
		if _, err := stream.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got := log.String(); got != "cli crashed: {\"type\":\"session\" has no id}\nnot json at all\n" {
		t.Fatalf("non-event stdout must reach the log unchanged, got %q", got)
	}
	if len(activity.lines()) != 0 {
		t.Fatalf("unparsed stdout must not become activity: %v", activity.lines())
	}
}

func TestPiEventStreamDropsUnusedEventRecords(t *testing.T) {
	var log strings.Builder
	var activity syncBuffer
	stream := NewPiEventStream(&log, &activity)
	// System messages carry a string content where assistant messages carry
	// an array; the session header and usage records have other shapes. None
	// of them may leak into the public log.
	for _, event := range []string{
		`{"type":"session","version":3,"id":"s1","cwd":"/tmp"}`,
		`{"type":"message_start","message":{"role":"system","content":"", "sections":{"preamble":"You are a coding assistant."}}}`,
		`{"type":"message_end","message":{"role":"system","content":"","sections":{"rules":"<rules>"}}}`,
		`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"the full assignment, never public prose"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":""}}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
	} {
		if _, err := stream.Write([]byte(event + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if log.Len() != 0 {
		t.Fatalf("unused event records must not reach the log, got %q", log.String())
	}
	if len(activity.lines()) != 0 {
		t.Fatalf("unused event records must not become activity: %v", activity.lines())
	}
}

func TestContinuePiEventStreamKeepsMessageIDsUnique(t *testing.T) {
	var activity syncBuffer
	first := NewPiEventStream(nil, &activity)
	for _, event := range []string{
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"first process answer"}]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"second message"}]}}`,
	} {
		if _, err := first.Write([]byte(event + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	// A continuation process must never reuse the first process's ids: the
	// feed merges records by id and would overwrite the earlier messages.
	continuation := ContinuePiEventStream(nil, &activity, first.Messages())
	if _, err := continuation.Write([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"continuation answer"}]}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	var texts []string
	seen := map[string]bool{}
	for _, record := range activity.lines() {
		if record["kind"] != "message" {
			t.Fatalf("unexpected record: %v", record)
		}
		text, _ := record["text"].(string)
		if seen[text] {
			t.Fatalf("message overwritten: %v", activity.lines())
		}
		seen[text] = true
		texts = append(texts, text)
	}
	if len(texts) != 3 {
		t.Fatalf("expected three distinct messages, got %v", texts)
	}
	if texts[2] != "continuation answer" {
		t.Fatalf("continuation message lost: %v", texts)
	}
}

// syncBuffer is a minimal concurrent-safe activity sink for tests.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) text() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) lines() []map[string]any {
	var records []map[string]any
	for _, line := range strings.Split(b.text(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record["mitos_activity"] != float64(1) {
			continue
		}
		records = append(records, record)
	}
	return records
}
