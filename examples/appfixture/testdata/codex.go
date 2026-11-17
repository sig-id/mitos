// Deterministic model stand-in for the real confined worker/stdio path.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"
)

var forbiddenPath string
var forbiddenDial string

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
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
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
			reply := "ack"
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
	}
}
