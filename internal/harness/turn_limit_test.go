package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func runWithActions(t *testing.T, actions, limit int) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		scan := bufio.NewScanner(server)
		for scan.Scan() {
			var req struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(scan.Bytes(), &req)
			switch req.Method {
			case "initialize":
				fmt.Fprintln(server, `{"id":1,"result":{}}`)
			case "thread/start":
				fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"t"}}}`)
			case "turn/start":
				fmt.Fprintf(server, "{\"id\":%d,\"result\":{\"turn\":{\"id\":\"u\"}}}\n", req.ID)
				fmt.Fprintln(server, `{"method":"item/completed","params":{"item":{"type":"reasoning"}}}`)
				for range actions {
					fmt.Fprintln(server, `{"method":"item/completed","params":{"item":{"type":"commandExecution"}}}`)
				}
				fmt.Fprintln(server, `{"method":"item/completed","params":{"item":{"type":"agentMessage","phase":"final_answer","text":"done"}}}`)
				fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"completed"}}}`)
			}
		}
	}()
	result := ""
	err := RunCodex(ctx, nil, client, strings.NewReader(""), io.Discard, t.TempDir(), "work", CodexOptions{Headless: true, MaxTurns: limit, Result: func(s string) error { result = s; return nil }})
	return result, err
}

func TestCodexStopsAtModelStepLimit(t *testing.T) {
	// Two actions plus the final answer fit a three-step allowance.
	if result, err := runWithActions(t, 2, 3); err != nil || result != "done" {
		t.Fatalf("within limit: %q %v", result, err)
	}
	var limit *TurnLimitError
	if _, err := runWithActions(t, 3, 3); !errors.As(err, &limit) || limit.Limit != 3 {
		t.Fatalf("limit not enforced: %v", err)
	}
	if _, err := runWithActions(t, 5, 0); err != nil {
		t.Fatalf("zero must mean unbounded: %v", err)
	}
}

// Claude reports model-step exhaustion through the same structured marker as
// Codex so admission records budget exhaustion without scraping summaries.
func TestClaudeStopsAtModelStepLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		done <- RunClaude(ctx, &claudeTestBroker{}, client, io.Discard, "/work", "Prompt", CodexOptions{MaxTurns: 1})
	}()
	scanner := bufio.NewScanner(server)
	scanner.Scan() // init control request
	fmt.Fprintln(server, `{"type":"control_response","response":{"request_id":"mitos-init","subtype":"success"}}`)
	scanner.Scan() // prompt as user message
	fmt.Fprintln(server, `{"type":"system","subtype":"init","session_id":"thread"}`)
	fmt.Fprintln(server, `{"type":"assistant","message":{"id":"a","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo hi"}}]}}`)
	fmt.Fprintln(server, `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"hi"}]}}`)
	var limit *TurnLimitError
	if err := <-done; !errors.As(err, &limit) || limit.Limit != 1 {
		t.Fatalf("claude limit not enforced structurally: %v", err)
	}
}
