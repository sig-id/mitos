package web

import (
	"bytes"
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
)

// The real Claude CLI and real Mitos hook subprocess use local deterministic
// inference. No model service or credentials are needed and Bash is denied.
func TestLiveClaudeNativeQuestionRoundTrip(t *testing.T) { testLiveClaude(t, false) }
func TestLiveClaudeNativeApprovalRoundTrip(t *testing.T) { testLiveClaude(t, true) }
func testLiveClaude(t *testing.T, approval bool) {
	if os.Getenv("MITOS_LIVE_CLAUDE") != "1" {
		t.Skip("set MITOS_LIVE_CLAUDE=1 and MITOS_TEST_BIN to exercise installed Claude")
	}
	binary := os.Getenv("MITOS_TEST_BIN")
	if binary == "" {
		t.Fatal("MITOS_TEST_BIN must name the built mitos executable")
	}
	local, engine := testServer(t)
	studioHTTP := httptest.NewServer(local)
	defer studioHTTP.Close()
	var calls atomic.Int32
	inference := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if strings.Contains(r.URL.Path, "count_tokens") {
			fmt.Fprint(w, `{"input_tokens":100}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			fmt.Fprint(w, `{}`)
			return
		}
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(kind string, payload any) {
			raw, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
		}
		event("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("msg_%d", n), "type": "message", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{}, "stop_reason": nil, "usage": map[string]int{"input_tokens": 100, "output_tokens": 0}}})
		stop := "end_turn"
		if n == 1 {
			tool := "AskUserQuestion"
			input := json.RawMessage(`{"questions":[{"question":"Which name?","header":"Name","options":[{"label":"Alpha","description":"Use Alpha"},{"label":"Beta","description":"Use Beta"}],"multiSelect":false}]}`)
			if approval {
				tool = "Bash"
				input = json.RawMessage(`{"command":"printf should-not-run","description":"Verify the one-shot permission response"}`)
			}
			if !strings.Contains(string(data), tool) {
				t.Errorf("native %s tool missing", tool)
			}
			event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_mitos_test", "name": tool, "input": map[string]any{}}})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
			stop = "tool_use"
		} else {
			event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Answer received."}})
		}
		event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 20}})
		event("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer inference.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "agent", "claude", "--url", studioHTTP.URL, "--workspace", engine.Root, "--", "-p", "--permission-prompt-tool", "stdio", "--input-format", "stream-json", "--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--no-session-persistence", "--model", "claude-sonnet-4-6", "--tools", "AskUserQuestion,Bash", "--output-format", "stream-json", "--verbose")
	cmd.Stdin = strings.NewReader("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"Ask the name question.\"}}\n")
	cmd.Dir = engine.Root
	cmd.Env = append(os.Environ(), "ANTHROPIC_BASE_URL="+inference.URL, "ANTHROPIC_API_KEY=mitos-local-test", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runID string
	var processErr error
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	for runID == "" {
		for _, r := range engine.Store.List() {
			if r.Decision != nil && r.Decision.Source != nil {
				runID = r.ID
				break
			}
		}
		select {
		case processErr = <-done:
			finished = true
			t.Fatalf("harness exited before request: %v; calls=%d\n%s", processErr, calls.Load(), output.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	r, _ := engine.Store.Get(runID)
	choice, value := "answer", json.RawMessage(`"Alpha"`)
	if approval {
		choice, value = "deny", nil
	}
	if _, err := engine.ResolveValueAs(runID, r.Decision.ID, choice, value, "", "console", decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	processErr = <-done
	finished = true
	if processErr != nil {
		t.Fatalf("harness failed: %v; calls=%d\n%s", processErr, calls.Load(), output.String())
	}
	r, _ = engine.Store.Get(runID)
	if r.Decision.AppliedAt == nil {
		t.Fatalf("harness did not acknowledge answer: %+v\n%s", r.Decision, output.String())
	}
}
