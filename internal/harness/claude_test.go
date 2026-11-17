package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"mitos/internal/decision"
)

type claudeTestBroker struct {
	mu          sync.Mutex
	answer      Delivery
	choice      string
	applied     bool
	invalidated bool
}

func (b *claudeTestBroker) Submit(_ context.Context, raw json.RawMessage) (Delivery, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, err := Normalize("claude", raw)
	if err != nil {
		return Delivery{}, err
	}
	choice := b.choice
	if choice == "" {
		choice = "approve"
	}
	v, err := Response("claude", r, []decision.Resolution{{Choice: choice}})
	b.answer = Delivery{RequestID: r.Key, State: "accepted", Digest: "d", Body: v}
	return b.answer, err
}
func (b *claudeTestBroker) Get(context.Context, string) (Delivery, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.answer, nil
}
func (b *claudeTestBroker) Deliver(_ context.Context, _, _, state string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applied = state == "applied"
	return nil
}
func (b *claudeTestBroker) Invalidate(context.Context, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.invalidated = true
	return nil
}
func TestClaudeCoordinatedProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	b := &claudeTestBroker{}
	var output, activity bytes.Buffer
	var result string
	done := make(chan error, 1)
	go func() {
		done <- RunClaude(ctx, b, client, &output, "/work", "Inspect project", CodexOptions{MaxTurns: 10, Activity: &activity, Tools: []DynamicTool{{Name: "result_get", InputSchema: json.RawMessage(`{"type":"object"}`)}}, ToolCall: func(name string, raw json.RawMessage) (any, error) {
			return map[string]string{"state": "candidate"}, nil
		}, Result: func(v string) error { result = v; return nil }})
	}()
	scanner := bufio.NewScanner(server)
	read := func() map[string]any {
		t.Helper()
		if !scanner.Scan() {
			t.Fatal(scanner.Err())
		}
		var v map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	send := func(v any) {
		t.Helper()
		raw, _ := json.Marshal(v)
		if _, err := fmt.Fprintln(server, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	init := read()
	if init["request_id"] != "mitos-init" {
		t.Fatal(init)
	}
	send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "mitos-init", "response": map[string]any{}}})
	if user := read(); user["message"].(map[string]any)["content"] != "Inspect project" {
		t.Fatal(user)
	}
	send(map[string]any{"type": "system", "subtype": "init", "session_id": "thread"})
	control := func(id string, request any) map[string]any {
		send(map[string]any{"type": "control_request", "request_id": id, "request": request})
		return read()["response"].(map[string]any)
	}
	for _, method := range []string{"initialize", "tools/list", "tools/call"} {
		response := control(method, map[string]any{"subtype": "mcp_message", "server_name": "mitos", "message": map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": map[string]any{"name": "result_get", "arguments": map[string]any{}}}})
		if response["subtype"] != "success" {
			t.Fatal(response)
		}
	}
	input := map[string]any{"session_id": "thread", "tool_use_id": "tool", "hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "echo checked"}, "cwd": "/work"}
	response := control("approval", map[string]any{"subtype": "hook_callback", "callback_id": "PreToolUse", "input": input})
	if response["response"].(map[string]any)["hookSpecificOutput"].(map[string]any)["permissionDecision"] != "allow" {
		t.Fatal(response)
	}
	if b.applied {
		t.Fatal("approval was acknowledged before tool completion")
	}
	input["hook_event_name"] = "PostToolUse"
	control("ack", map[string]any{"subtype": "hook_callback", "callback_id": "PostToolUse", "input": input})
	send(map[string]any{"type": "assistant", "message": map[string]any{"id": "a", "content": []any{map[string]any{"type": "text", "text": "Checked project"}}}})
	send(map[string]any{"type": "result", "subtype": "success", "session_id": "thread", "result": "Done"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result != "Done" || !b.applied || !strings.Contains(activity.String(), "Checked project") {
		t.Fatalf("result=%s applied=%v activity=%s", result, b.applied, activity.String())
	}
}
func TestClaudeControlRefusesUnknownToolsAndPreservesDenial(t *testing.T) {
	b := &claudeTestBroker{choice: "deny"}
	for _, tool := range []string{"Bash", "UnknownExternalTool"} {
		raw, _ := json.Marshal(map[string]any{"subtype": "can_use_tool", "tool_use_id": "id", "tool_name": tool, "input": map[string]any{"command": "danger"}})
		value, err := claudeControl(context.Background(), b, "thread", "/work", raw, CodexOptions{})
		if tool == "Bash" {
			if err != nil || value.(map[string]any)["behavior"] != "deny" {
				t.Fatal(value, err)
			}
		} else if err == nil {
			t.Fatal("unknown permission allowed")
		}
	}
	if _, err := claudeMCP(context.Background(), json.RawMessage(`{"id":1,"method":"tools/call","params":{"name":"artifact_read","arguments":{}}}`), CodexOptions{}); err == nil {
		t.Fatal("ungranted tool accepted")
	}
}
func TestClaudeCancellationAndMissingStructuredResult(t *testing.T) {
	for _, mode := range []string{"cancel", "missing-schema", "error", "eof"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			done := make(chan error, 1)
			go func() {
				done <- RunClaude(ctx, &claudeTestBroker{}, client, io.Discard, "/work", "Prompt", CodexOptions{OutputSchema: json.RawMessage(`{"type":"object"}`)})
			}()
			scanner := bufio.NewScanner(server)
			scanner.Scan()
			fmt.Fprintln(server, `{"type":"control_response","response":{"request_id":"mitos-init","subtype":"success"}}`)
			scanner.Scan()
			switch mode {
			case "cancel":
				cancel()
			case "eof":
				server.Close()
			case "error":
				fmt.Fprintln(server, `{"type":"result","subtype":"error_max_turns","is_error":true}`)
			case "missing-schema":
				fmt.Fprintln(server, `{"type":"result","subtype":"success","result":"not structured"}`)
			}
			if err := <-done; err == nil {
				t.Fatal("invalid completion accepted")
			}
		})
	}
}

func TestClaudeFailedHookExplicitlyDeniesBeforeStopping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunClaude(ctx, &claudeTestBroker{}, client, io.Discard, "/work", "Prompt", CodexOptions{})
	}()
	scanner := bufio.NewScanner(server)
	scanner.Scan()
	fmt.Fprintln(server, `{"type":"control_response","response":{"request_id":"mitos-init","subtype":"success"}}`)
	scanner.Scan()
	fmt.Fprintln(server, `{"type":"system","subtype":"init","session_id":"thread"}`)
	// A mismatched source must be denied even if the native hook error policy is continue.
	fmt.Fprintln(server, `{"type":"control_request","request_id":"bad-hook","request":{"subtype":"hook_callback","callback_id":"PreToolUse","input":{"session_id":"wrong","tool_use_id":"tool","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"danger"}}}}`)
	if !scanner.Scan() {
		t.Fatal(scanner.Err())
	}
	var response struct {
		Response struct {
			Subtype string `json:"subtype"`
			Value   struct {
				Continue bool `json:"continue"`
				Hook     struct {
					Decision string `json:"permissionDecision"`
				} `json:"hookSpecificOutput"`
			} `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Response.Subtype != "success" || response.Response.Value.Continue || response.Response.Value.Hook.Decision != "deny" {
		t.Fatal(string(scanner.Bytes()))
	}
	if err := <-done; err == nil {
		t.Fatal("failed hook did not stop execution")
	}
}

type claudeWaitingBroker struct {
	claudeTestBroker
	submitted chan struct{}
}

func (b *claudeWaitingBroker) Submit(_ context.Context, raw json.RawMessage) (Delivery, error) {
	request, err := Normalize("claude", raw)
	if err != nil {
		return Delivery{}, err
	}
	b.mu.Lock()
	b.answer = Delivery{RequestID: request.Key, State: "pending"}
	answer := b.answer
	b.mu.Unlock()
	close(b.submitted)
	return answer, nil
}
func TestClaudeNativeCancellationInvalidatesApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	broker := &claudeWaitingBroker{submitted: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- RunClaude(ctx, broker, client, io.Discard, "/work", "Prompt", CodexOptions{}) }()
	scanner := bufio.NewScanner(server)
	scanner.Scan()
	fmt.Fprintln(server, `{"type":"control_response","response":{"request_id":"mitos-init","subtype":"success"}}`)
	scanner.Scan()
	fmt.Fprintln(server, `{"type":"system","subtype":"init","session_id":"thread"}`)
	fmt.Fprintln(server, `{"type":"control_request","request_id":"approval","request":{"subtype":"can_use_tool","tool_use_id":"tool","tool_name":"Bash","input":{"command":"echo waiting"}}}`)
	<-broker.submitted
	fmt.Fprintln(server, `{"type":"control_cancel_request","request_id":"approval"}`)
	fmt.Fprintln(server, `{"type":"result","subtype":"error_during_execution","is_error":true}`)
	if err := <-done; err == nil {
		t.Fatal("unexpected success")
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	if !broker.invalidated || broker.applied {
		t.Fatal("cancelled approval remained deliverable")
	}
}
