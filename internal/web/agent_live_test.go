package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/harness"
)

type liveStream struct {
	io.Reader
	io.Writer
	close func() error
}

func (s *liveStream) Close() error { return s.close() }

// This gate uses the installed, real Codex app-server with deterministic local
// inference. It exercises an actual waiting harness without API credentials,
// paid inference, or executing a model-selected command.
func TestLiveCodexNativeQuestionRoundTrip(t *testing.T) { testLiveCodex(t, false) }
func TestLiveCodexNativeApprovalRoundTrip(t *testing.T) { testLiveCodex(t, true) }
func testLiveCodex(t *testing.T, approval bool) {
	if os.Getenv("MITOS_LIVE_CODEX") != "1" {
		t.Skip("set MITOS_LIVE_CODEX=1 to exercise installed Codex")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	local, engine := testServer(t)
	studioHTTP := httptest.NewServer(local)
	defer studioHTTP.Close()
	broker, _ := harness.NewClient(studioHTTP.URL, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if err = broker.Start(ctx, "codex", "Actual Codex request", engine.Root); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if !strings.Contains(r.URL.Path, "responses") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"data":[]}`)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(kind string, payload any) {
			b, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b)
		}
		event("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("resp_%d", n), "status": "in_progress"}})
		var item map[string]any
		if n == 1 {
			if !approval && !strings.Contains(string(data), "request_user_input") {
				t.Error("native tool was not exposed by actual harness")
			}
			args := `{"questions":[{"id":"name","header":"Name","question":"Which name?","options":[{"label":"Alpha","description":"Use Alpha"},{"label":"Beta","description":"Use Beta"}]}]}`
			item = map[string]any{"type": "function_call", "id": "fc_question", "call_id": "call_question", "name": "request_user_input", "arguments": args}
			if approval {
				var request struct {
					Tools []struct {
						Name string `json:"name"`
					} `json:"tools"`
				}
				_ = json.Unmarshal(data, &request)
				tool := ""
				for _, candidate := range request.Tools {
					if candidate.Name == "exec_command" || candidate.Name == "shell_command" {
						tool = candidate.Name
						break
					}
				}
				if tool == "" {
					t.Error("actual command tool was not exposed")
				}
				command := map[string]any{"sandbox_permissions": "require_escalated", "justification": "Verify a one-shot permission response without running the command."}
				if tool == "exec_command" {
					command["cmd"] = "printf should-not-run"
				} else {
					command["command"] = "printf should-not-run"
				}
				raw, _ := json.Marshal(command)
				item["name"] = tool
				item["arguments"] = string(raw)
			}

		} else {
			item = map[string]any{"type": "message", "id": "message_done", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Answer received.", "annotations": []any{}}}}
		}
		event("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
		event("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		event("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_%d", n), "status": "completed", "output": []any{item}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}})
	}))
	defer inference.Close()
	cmd := exec.CommandContext(ctx, binary, "app-server", "-c", `model_provider="mitos_test"`, "-c", `model="gpt-5.4"`, "-c", `model_providers.mitos_test.name="Mitos local test"`, "-c", `model_providers.mitos_test.base_url="`+inference.URL+`"`, "-c", `model_providers.mitos_test.wire_api="responses"`, "-c", `model_providers.mitos_test.requires_openai_auth=false`, "-c", `model_providers.mitos_test.supports_websockets=false`, "-c", `history.persistence="none"`)
	cmd.Dir = engine.Root
	cmd.Stderr = io.Discard
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = input.Close(); _ = cmd.Wait() }()
	incoming, clientWrite := io.Pipe()
	defer incoming.Close()
	defer clientWrite.Close()
	clientRead, outgoing := io.Pipe()
	defer clientRead.Close()
	defer outgoing.Close()
	stream := &liveStream{Reader: output, Writer: input, close: func() error { _ = input.Close(); return output.Close() }}
	proxyDone := make(chan error, 1)
	go func() { proxyDone <- harness.ProxyCodex(ctx, broker, stream, incoming, outgoing) }()
	lines := make(chan json.RawMessage, 128)
	go func() {
		scan := bufio.NewScanner(clientRead)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			select {
			case lines <- append(json.RawMessage(nil), scan.Bytes()...):
			case <-ctx.Done():
				return
			}
		}
	}()
	send := func(value any) {
		t.Helper()
		raw, _ := json.Marshal(value)
		if _, err = clientWrite.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(id int) json.RawMessage {
		t.Helper()
		for {
			select {
			case line := <-lines:
				var message struct {
					ID     int             `json:"id"`
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				_ = json.Unmarshal(line, &message)
				if message.ID == id {
					if len(message.Error) > 0 {
						t.Fatal(string(message.Error))
					}
					return message.Result
				}
			case err := <-proxyDone:
				t.Fatal("proxy stopped", err)
			case <-ctx.Done():
				t.Fatalf("actual harness timeout; inference calls=%d", calls.Load())
			}
		}
	}
	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "mitos_test", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}})
	wait(1)
	send(map[string]any{"method": "initialized", "params": map[string]any{}})
	send(map[string]any{"id": 2, "method": "thread/start", "params": map[string]any{"cwd": engine.Root, "model": "gpt-5.4", "modelProvider": "mitos_test", "approvalPolicy": "on-request", "sandbox": "read-only", "ephemeral": true}})
	result := wait(2)
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if json.Unmarshal(result, &thread) != nil || thread.Thread.ID == "" {
		t.Fatal(string(result))
	}
	send(map[string]any{"id": 3, "method": "turn/start", "params": map[string]any{"threadId": thread.Thread.ID, "input": []any{map[string]any{"type": "text", "text": "Ask the name question."}}, "collaborationMode": map[string]any{"mode": "plan", "settings": map[string]any{"model": "gpt-5.4", "reasoning_effort": "low"}}}})
	wait(3)
	var runID string
	var diagnostics strings.Builder
	for runID == "" {
		for _, r := range engine.Store.List() {
			if r.Decision != nil && r.Decision.Source != nil {
				runID = r.ID
				break
			}
		}
		select {
		case line := <-lines:
			diagnostics.Write(line)
			diagnostics.WriteByte('\n')
		case err := <-proxyDone:
			t.Fatalf("proxy stopped: %v\n%s", err, diagnostics.String())
		case <-ctx.Done():
			t.Fatalf("no actual waiting request; inference calls=%d\n%s", calls.Load(), diagnostics.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	r, _ := engine.Store.Get(runID)
	value := json.RawMessage(nil)
	choice := r.Decision.Options[0].ID
	if approval {
		choice = "deny"
	}
	if r.Decision.InputProfile() == "text" {
		value = json.RawMessage(`"Alpha"`)
	}
	if _, err = engine.ResolveValueAs(runID, r.Decision.ID, choice, value, "", "console", decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	for {
		r, _ = engine.Store.Get(runID)
		if r.Decision.AppliedAt != nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("actual harness never acknowledged answer")
		case <-time.After(20 * time.Millisecond):
		}
	}
	cancel()
	clientWrite.Close()
	outgoing.Close()
	<-proxyDone
}
