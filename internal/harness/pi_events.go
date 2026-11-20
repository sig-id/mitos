package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// PiEventStream translates pi's JSON event stream (pi --mode json) into the
// public execution log and activity records, the way writeCodexActivity
// renders app-server events. Reasoning deltas stay private: only assistant
// text, native tool calls, and their results become public activity.
//
// The stream is strict JSONL: records split on LF, an optional CR is stripped.
// Fields are decoded tolerantly (system messages carry a string content where
// assistant messages carry an array), and any valid JSON record the translator
// does not use is dropped rather than printed: only plain text from the CLI,
// such as a crash report, reaches the log unchanged.
type PiEventStream struct {
	output   io.Writer
	activity io.Writer
	partial  []byte
	pending  map[string]piPendingCall
	messages int
}

// piPendingCall is one announced tool call waiting for its result.
type piPendingCall struct {
	kind string
	name string
}

type piTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type piToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type piToolResult struct {
	ToolCallID string          `json:"toolCallId"`
	IsError    bool            `json:"isError"`
	Details    json.RawMessage `json:"details"`
}

type piAssistantEvent struct {
	Type     string      `json:"type"`
	Delta    string      `json:"delta"`
	ToolCall *piToolCall `json:"toolCall"`
}

type piEvent struct {
	Type                  string           `json:"type"`
	Message               json.RawMessage  `json:"message"`
	AssistantMessageEvent piAssistantEvent `json:"assistantMessageEvent"`
	ToolResults           []piToolResult   `json:"toolResults"`
}

// NewPiEventStream renders into output, the human-readable execution log, and
// writes public activity records to activity. Either may be nil.
func NewPiEventStream(output, activity io.Writer) *PiEventStream {
	return &PiEventStream{output: output, activity: activity, pending: map[string]piPendingCall{}}
}

func (s *PiEventStream) Write(p []byte) (int, error) {
	data := append(s.partial, p...)
	for {
		line, rest, found := strings.Cut(string(data), "\n")
		if !found {
			s.partial = data
			return len(p), nil
		}
		data = []byte(rest)
		s.line(strings.TrimSuffix(line, "\r"))
	}
}

// Flush renders any trailing bytes that never reached a line boundary.
func (s *PiEventStream) Flush() {
	if len(s.partial) > 0 {
		s.line(strings.TrimSuffix(string(s.partial), "\r"))
		s.partial = nil
	}
}

func (s *PiEventStream) log(text string) {
	if s.output != nil && text != "" {
		fmt.Fprint(s.output, text)
	}
}

func (s *PiEventStream) record(value map[string]any) {
	writeActivityRecord(s.activity, value)
}

func (s *PiEventStream) line(line string) {
	if line == "" {
		return
	}
	// Plain text from the CLI, such as a crash report, stays visible;
	// valid event records that translate to nothing are dropped.
	var event piEvent
	if line[0] != '{' || json.Unmarshal([]byte(line), &event) != nil {
		s.log(line + "\n")
		return
	}
	switch event.Type {
	case "message_update":
		s.update(event.AssistantMessageEvent)
	case "message_end":
		s.messageEnd(event.Message)
	case "turn_end":
		s.toolResults(event.ToolResults)
	}
}

// update streams assistant text deltas and announces completed tool calls.
// A tool call arrives twice: toolcall_start without arguments, then
// toolcall_end with the complete call. Only the complete call is recorded.
func (s *PiEventStream) update(event piAssistantEvent) {
	switch event.Type {
	case "text_delta":
		if event.Delta != "" {
			s.record(map[string]any{"kind": "message", "id": fmt.Sprintf("pi-%d", s.messages+1), "delta": event.Delta})
		}
	case "toolcall_end":
		if event.ToolCall != nil {
			s.toolCall(*event.ToolCall)
		}
	}
}

func (s *PiEventStream) toolCall(call piToolCall) {
	if call.ID == "" {
		return
	}
	// The per-attempt MCP server already records the Mitos tools with its own
	// identity; a second record from the CLI would duplicate the feed row.
	if strings.HasPrefix(call.Name, piMCPTools) {
		return
	}
	kind, name := classifyPiTool(call.Name, call.Arguments)
	s.pending[call.ID] = piPendingCall{kind: kind, name: name}
	record := map[string]any{"kind": kind, "id": call.ID, "status": "running"}
	if kind == "command" {
		record["command"] = name
	} else {
		record["name"] = name
	}
	s.record(record)
	s.log(fmt.Sprintf("\nStarted %s: %s\n", piActivityLabel(kind), name))
}

func (s *PiEventStream) messageEnd(message json.RawMessage) {
	var decoded struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(message, &decoded) != nil || decoded.Role != "assistant" || len(decoded.Content) == 0 || decoded.Content[0] != '[' {
		return
	}
	var blocks []piTextBlock
	if json.Unmarshal(decoded.Content, &blocks) != nil {
		return
	}
	var text strings.Builder
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(block.Text)
		}
	}
	if text.Len() == 0 {
		return
	}
	s.messages++
	s.record(map[string]any{"kind": "message", "id": fmt.Sprintf("pi-%d", s.messages), "text": text.String()})
	s.log("\n" + text.String() + "\n")
}

func (s *PiEventStream) toolResults(results []piToolResult) {
	for _, result := range results {
		call, ok := s.pending[result.ToolCallID]
		if !ok {
			continue
		}
		delete(s.pending, result.ToolCallID)
		status := "completed"
		if result.IsError {
			status = "failed"
		}
		record := map[string]any{"kind": call.kind, "id": result.ToolCallID, "status": status}
		if call.kind == "command" {
			record["command"] = call.name
			var details struct {
				ExitCode *float64 `json:"exit_code"`
			}
			if json.Unmarshal(result.Details, &details) == nil && details.ExitCode != nil {
				record["exit_code"] = int(*details.ExitCode)
			}
		} else {
			record["name"] = call.name
		}
		s.record(record)
		s.log(fmt.Sprintf("\nFinished %s: %s", piActivityLabel(call.kind), call.name))
		if code, ok := record["exit_code"]; ok {
			s.log(fmt.Sprintf(" (exit %d)", code))
		}
		s.log("\n")
	}
}

// piMCPTools marks the Mitos MCP tools, which the MCP server records itself.
const piMCPTools = "mcp__mitos__"

// classifyPiTool maps one native pi tool call to a public activity kind and a
// display name: shell commands and file edits get their concrete value, other
// tools keep their tool name.
func classifyPiTool(tool string, arguments json.RawMessage) (kind, name string) {
	var args map[string]any
	_ = json.Unmarshal(arguments, &args)
	text := func(keys ...string) string {
		for _, key := range keys {
			if value, ok := args[key].(string); ok && value != "" {
				return value
			}
		}
		return ""
	}
	switch tool {
	case "bash", "sh":
		// A call whose arguments did not decode keeps the tool name, so the
		// activity row never renders with empty text.
		if command := text("command"); command != "" {
			return "command", command
		}
		return "command", tool
	case "edit", "write":
		if path := text("path", "file"); path != "" {
			return "file", path
		}
		return "file", tool
	default:
		return "tool", tool
	}
}

func piActivityLabel(kind string) string {
	switch kind {
	case "command":
		return "command"
	case "file":
		return "file change"
	default:
		return "tool"
	}
}
