package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestCodexTerminalPreservesEarlyNotifications(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		scan := bufio.NewScanner(server)
		for scan.Scan() {
			var request struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
			}
			_ = json.Unmarshal(scan.Bytes(), &request)
			switch request.Method {
			case "initialize":
				fmt.Fprintln(server, `{"id":1,"result":{}}`)
			case "thread/start":
				fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"thread"},"model":"test"}}`)
			case "turn/start":
				// A quick server can emit notifications before the RPC response.
				// Server requests have a separate ID namespace from client RPCs.
				fmt.Fprintln(server, `{"id":3,"method":"unsupported/nativeRequest","params":{}}`)
				fmt.Fprintln(server, `{"method":"item/agentMessage/delta","params":{"delta":"preserved"}}`)
				fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn"}}}`)
				fmt.Fprintln(server, `{"id":3,"result":{}}`)
			}
		}
	}()
	var output bytes.Buffer
	if err := RunCodexTerminal(ctx, nil, client, strings.NewReader("{}\n"), &output, t.TempDir(), "test", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "preserved") {
		t.Fatal(output.String())
	}
	if !strings.Contains(output.String(), "unsupported/nativeRequest") {
		t.Fatal("native fallback request lost", output.String())
	}
}

func TestCodexTerminalKeepOpenAcceptsAnotherTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	turns := make(chan int, 1)
	go func() {
		count := 0
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
				count++
				fmt.Fprintf(server, "{\"id\":%d,\"result\":{}}\n", req.ID)
				fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"turn"}}}`)
				if count == 2 {
					turns <- count
				}
			}
		}
	}()
	var output bytes.Buffer
	if err := RunCodexTerminalOptions(ctx, nil, client, strings.NewReader("second assignment\n"), &output, t.TempDir(), "first", false, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-turns:
	default:
		t.Fatal("initial assignment closed the session", output.String())
	}
}

func TestCodexDurableConversationResume(t *testing.T) {
	for _, resume := range []string{"", "saved-thread"} {
		t.Run("resume="+resume, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			recorded := false
			requests := make(chan map[string]any, 3)
			go func() {
				scan := bufio.NewScanner(server)
				for scan.Scan() {
					var req map[string]any
					_ = json.Unmarshal(scan.Bytes(), &req)
					method := req["method"]
					switch method {
					case "initialize":
						fmt.Fprintln(server, `{"id":1,"result":{}}`)
					case "thread/start", "thread/resume":
						requests <- req
						fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"saved-thread"}}}`)
					case "turn/start":
						requests <- req
						fmt.Fprintln(server, `{"id":3,"result":{}}`)
						fmt.Fprintln(server, `{"method":"item/completed","params":{"item":{"type":"agentMessage","text":"done"}}}`)
						fmt.Fprintln(server, `{"method":"turn/completed","params":{"turn":{"status":"completed"}}}`)
					}
				}
			}()
			var output bytes.Buffer
			err := RunCodex(ctx, nil, client, strings.NewReader(""), &output, t.TempDir(), "continue", CodexOptions{Headless: true, ResumeThread: resume, ThreadStarted: func(id string) error { recorded = id == "saved-thread"; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			if !recorded {
				t.Fatal("conversation not persisted")
			}
			req := <-requests
			p := req["params"].(map[string]any)
			if resume == "" {
				if req["method"] != "thread/start" || p["ephemeral"] != false {
					t.Fatal("conversation was ephemeral", req)
				}
			} else if req["method"] != "thread/resume" || p["threadId"] != resume || p["ephemeral"] != nil {
				t.Fatal("wrong resume", req)
			}
			turn := <-requests
			if turn["params"].(map[string]any)["threadId"] != "saved-thread" {
				t.Fatal("turn used another conversation")
			}
		})
	}
}

func TestCodexCannotStartTurnBeforeConversationIsSaved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		scan := bufio.NewScanner(server)
		for scan.Scan() {
			var req struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(scan.Bytes(), &req)
			switch req.Method {
			case "initialize":
				fmt.Fprintln(server, `{"id":1,"result":{}}`)
			case "thread/start":
				fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"saved"}}}`)
			case "turn/start":
				panic("turn launched without saving conversation")
			}
		}
	}()
	var output bytes.Buffer
	err := RunCodex(ctx, nil, client, strings.NewReader(""), &output, t.TempDir(), "test", CodexOptions{Headless: true, ThreadStarted: func(string) error { return fmt.Errorf("journal unavailable") }})
	if err == nil || !strings.Contains(err.Error(), "journal unavailable") {
		t.Fatal("continued after persistence failure", err)
	}
}
