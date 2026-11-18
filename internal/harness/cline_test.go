package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

type clineTestStream struct {
	io.Reader
	io.Writer
}

func (clineTestStream) Close() error { return nil }

func TestClineACPPermissionExchange(t *testing.T) {
	const frames = `{"id":1,"result":{"protocolVersion":1}}
{"id":2,"result":{"sessionId":"worker","models":{"currentModelId":"default"}}}
{"id":3,"result":{}}
{"method":"session/update","params":{"sessionId":"worker","update":{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"private"}}}}
{"id":"read","method":"session/request_permission","params":{"sessionId":"worker","toolCall":{"toolCallId":"1","kind":"read","title":"read_files: README.md"},"options":[{"kind":"allow_always","optionId":"allow_always"},{"kind":"allow_once","optionId":"allow_once"}]}}
{"id":"write","method":"session/request_permission","params":{"sessionId":"worker","toolCall":{"toolCallId":"2","kind":"other","title":"apply_patch: update README.md"},"options":[{"kind":"allow_once","optionId":"allow_once"},{"kind":"reject_once","optionId":"reject_once"}]}}
{"method":"session/update","params":{"sessionId":"worker","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello "}}}}
{"method":"session/update","params":{"sessionId":"worker","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"world"}}}}
{"id":4,"result":{"stopReason":"end_turn"}}
`
	var sent, output bytes.Buffer
	err := RunClineACP(t.Context(), clineTestStream{strings.NewReader(frames), &sent}, &output, "/workspace", "assignment", "selected", func(cap string) bool { return cap == "workspace.read" })
	if err != nil || output.String() != "hello world" {
		t.Fatalf("response %q, error %v", output.String(), err)
	}
	scan := json.NewDecoder(&sent)
	for _, method := range []string{"initialize", "session/new", "session/set_model", "session/prompt"} {
		var msg struct {
			Method string
			Params struct {
				Cwd, SessionID, ModelID string
				Prompt                  []struct{ Text string }
			}
		}
		if err = scan.Decode(&msg); err != nil || msg.Method != method {
			t.Fatalf("%s: %+v %v", method, msg, err)
		}
		switch method {
		case "session/new":
			if msg.Params.Cwd != "/workspace" {
				t.Fatal(msg)
			}
		case "session/set_model":
			if msg.Params.ModelID != "selected" || msg.Params.SessionID != "worker" {
				t.Fatal(msg)
			}
		case "session/prompt":
			if msg.Params.SessionID != "worker" || len(msg.Params.Prompt) != 1 || msg.Params.Prompt[0].Text != "assignment" {
				t.Fatal(msg)
			}
		}
	}
	for _, want := range []struct{ id, option string }{{"read", "allow_once"}, {"write", "reject_once"}} {
		var reply struct {
			ID     string
			Result struct {
				Outcome struct{ Outcome, OptionID string }
			}
		}
		if err = scan.Decode(&reply); err != nil || reply.ID != want.id || reply.Result.Outcome.Outcome != "selected" || reply.Result.Outcome.OptionID != want.option {
			t.Fatalf("%s: %+v %v", want.id, reply, err)
		}
	}
}

func TestClineNativeToolGrants(t *testing.T) {
	for _, tc := range []struct{ name, kind, cap string }{
		{"read_files", "read", "workspace.read"}, {"Grep", "search", "workspace.read"}, {"web_search", "search", "network.read"},
		{"apply_patch", "other", "workspace.write"}, {"editor", "edit", "workspace.write"}, {"run_commands", "execute", "command.run"},
		{"spawn_agent", "think", ""}, {"unknown", "other", ""}, {"run_commands", "read", ""},
	} {
		if got := clineToolCapability(tc.name+": details", tc.kind); got != tc.cap {
			t.Errorf("%s/%s: %q", tc.name, tc.kind, got)
		}
	}
	client := clineClient{session: "bound", granted: func(string) bool { return true }}
	for _, params := range []string{
		`{"sessionId":"foreign","toolCall":{"toolCallId":"1","title":"Read","kind":"read"},"options":[{"kind":"allow_once","optionId":"allow_once"}]}`,
		`{"sessionId":"bound","toolCall":{"toolCallId":"1","title":"Read","kind":"read"},"options":[{"kind":"allow_always","optionId":"allow_always"}]}`,
	} {
		if client.permission(json.RawMessage(params))["outcome"] != "cancelled" {
			t.Fatal(params)
		}
	}
}

func TestClineIncompletePrompt(t *testing.T) {
	frames := "{\"id\":1,\"result\":{\"protocolVersion\":1}}\n{\"id\":2,\"result\":{\"sessionId\":\"s\"}}\n"
	for _, final := range []string{"", `{"id":3,"result":{"stopReason":"cancelled"}}`, `{"id":3,"error":{"message":"provider unavailable"}}`} {
		if err := RunClineACP(t.Context(), clineTestStream{strings.NewReader(frames + final), io.Discard}, io.Discard, "/dir", "assignment", "", nil); err == nil {
			t.Fatal("incomplete prompt accepted")
		}
	}
}
