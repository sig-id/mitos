// Deterministic model stand-in for the real confined worker/stdio path.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

var forbiddenPath string
var forbiddenDial string
var actionTarget string
var waitForInput string

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println("codex-cli 0.1.0 fixture")
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	send := func(value any) {
		if err := encoder.Encode(value); err != nil {
			os.Exit(1)
		}
	}

	finish := func(reply string) {
		if forbiddenPath != "" {
			if file, err := os.Open(forbiddenPath); err == nil {
				file.Close()
				reply = "EXPOSED"
			}
		}
		if forbiddenDial != "" {
			if conn, err := net.DialTimeout("tcp", forbiddenDial, 1500*time.Millisecond); err == nil {
				conn.Close()
				reply = "EXPOSED"
			}
		}
		result, _ := json.Marshal(map[string]string{"reply": reply})
		send(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "item": map[string]string{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": string(result)}}})
		send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "fixture-thread", "turn": map[string]string{"id": "fixture-turn", "status": "completed"}}})
	}
	tool := func(id, name string, payload bool) {
		arguments := map[string]any{"target": actionTarget, "key": "fixture-effect"}
		if payload {
			arguments["payload"] = map[string]string{"reply": "ack"}
		}
		send(map[string]any{"id": id, "method": "item/tool/call", "params": map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "tool": name, "arguments": arguments}})
	}
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Input []struct {
					Text string `json:"text"`
				} `json:"input"`
			} `json:"params"`
			Result struct {
				Success      bool `json:"success"`
				ContentItems []struct {
					Text string `json:"text"`
				} `json:"contentItems"`
			} `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(1)
		}
		switch request.Method {
		case "initialize":
			send(map[string]any{"id": request.ID, "result": map[string]any{}})
		case "thread/start":
			send(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]string{"id": "fixture-thread"}, "model": "fixture"}})
		case "turn/start":
			send(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]string{"id": "fixture-turn"}}})
			if waitForInput != "" {
				resumed := false
				for _, input := range request.Params.Input {
					if strings.Contains(input.Text, `"resume":true`) {
						resumed = true
					}
				}
				if resumed {
					finish("ack")
				} else {
					send(map[string]any{"id": "wait-call", "method": "item/tool/call", "params": map[string]any{"threadId": "fixture-thread", "turnId": "fixture-turn", "tool": "mitos_wait", "arguments": map[string]any{"cursor": 0, "checkpoint": map[string]string{"state": "waiting"}}}})
				}
			} else if actionTarget != "" {
				tool("action-call", "mitos_action", true)
			} else {
				finish("ack")
			}
		default:
			if string(request.ID) == `"wait-call"` && request.Result.Success {
				finish("ack")
			}
			if string(request.ID) == `"action-call"` {
				tool("reconcile-call", "mitos_reconcile", false)
			}
			if string(request.ID) == `"reconcile-call"` {
				state := ""
				if request.Result.Success && len(request.Result.ContentItems) > 0 {
					var receipt struct {
						State string `json:"state"`
					}
					_ = json.Unmarshal([]byte(request.Result.ContentItems[0].Text), &receipt)
					state = receipt.State
				}
				if state == "applied" {
					finish("ack")
				} else {
					finish("UNRECONCILED")
				}
			}
		}
	}
}
