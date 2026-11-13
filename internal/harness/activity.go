package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Render public execution activity only. Reasoning notifications and raw request
// payloads are deliberately excluded from the user-facing execution log.
func writeCodexActivity(output io.Writer, event Envelope) {
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
	switch item.Type {
	case "commandExecution":
		fmt.Fprintf(output, "\n%s command: %s", verb, item.Command)
		if item.ExitCode != nil {
			fmt.Fprintf(output, " (exit %d)", *item.ExitCode)
		}
		fmt.Fprintln(output)
	case "mcpToolCall", "dynamicToolCall":
		fmt.Fprintf(output, "\n%s tool: %s\n", verb, strings.Trim(item.Server+" / "+item.Tool, " /"))
	case "fileChange":
		for _, change := range item.Changes {
			fmt.Fprintf(output, "\n%s file change: %s\n", verb, change.Path)
		}
	}
}
