package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Render public execution activity only. Reasoning notifications and raw request
// payloads are deliberately excluded from the user-facing execution log.
// Keep a versioned public record alongside the human-readable log. Readers must
// never infer assistant messages from arbitrary command output.
func writeActivityRecord(output io.Writer, value map[string]any) {
	value["mitos_activity"] = 1
	data, err := json.Marshal(value)
	if err == nil {
		fmt.Fprintf(output, "\n%s\n", data)
	}
}

func writeCodexActivity(output io.Writer, event Envelope) {
	if event.Method == "item/agentMessage/delta" {
		var p struct {
			Delta  string `json:"delta"`
			ItemID string `json:"itemId"`
		}
		if json.Unmarshal(event.Params, &p) == nil {
			writeActivityRecord(output, map[string]any{"kind": "message", "id": p.ItemID, "delta": p.Delta})
		}
		return
	}
	if event.Method == "item/commandExecution/outputDelta" {
		var p struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(event.Params, &p) == nil {
			fmt.Fprint(output, p.Delta)
		}
		return
	}
	if event.Method != "item/started" && event.Method != "item/completed" {
		return
	}
	var p struct {
		Item struct {
			ID       string `json:"id"`
			Text     string `json:"text"`
			Type     string `json:"type"`
			Command  string `json:"command"`
			Tool     string `json:"tool"`
			Server   string `json:"server"`
			Status   string `json:"status"`
			ExitCode *int   `json:"exitCode"`
			Changes  []struct {
				Path string `json:"path"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal(event.Params, &p) != nil {
		return
	}
	item := p.Item
	verb := "Started"
	if event.Method == "item/completed" {
		verb = "Finished"
	}
	record := map[string]any{"id": item.ID, "status": item.Status}
	if event.Method == "item/started" {
		record["status"] = "running"
	} else if item.Status == "" {
		record["status"] = "completed"
	}
	switch item.Type {
	case "agentMessage":
		if event.Method == "item/completed" {
			writeActivityRecord(output, map[string]any{"kind": "message", "id": item.ID, "text": item.Text})
		}

	case "commandExecution":
		record["kind"], record["command"], record["exit_code"] = "command", item.Command, item.ExitCode
		writeActivityRecord(output, record)
		fmt.Fprintf(output, "\n%s command: %s", verb, item.Command)
		if item.ExitCode != nil {
			fmt.Fprintf(output, " (exit %d)", *item.ExitCode)
		}
		fmt.Fprintln(output)
	case "mcpToolCall", "dynamicToolCall":
		record["kind"], record["name"] = "tool", strings.Trim(item.Server+" / "+item.Tool, " /")
		writeActivityRecord(output, record)
		fmt.Fprintf(output, "\n%s tool: %s\n", verb, strings.Trim(item.Server+" / "+item.Tool, " /"))
	case "fileChange":
		for _, change := range item.Changes {
			record["kind"], record["name"] = "file", change.Path
			writeActivityRecord(output, record)
			fmt.Fprintf(output, "\n%s file change: %s\n", verb, change.Path)
		}
	}
}
