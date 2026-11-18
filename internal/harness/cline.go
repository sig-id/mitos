package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Cline's terminal mode denies permission prompts when stdin is a pipe. ACP
// provides a bidirectional permission channel even for unattended workers.
func RunClineACP(ctx context.Context, stream io.ReadWriteCloser, output io.Writer, directory, prompt, model string, granted func(string) bool) error {
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	client := clineClient{out: json.NewEncoder(stream), in: nativeScanner(stream), granted: granted, text: output}
	var handshake struct{ ProtocolVersion int }
	if err := client.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": struct{}{}}, &handshake); err != nil {
		return err
	}
	if handshake.ProtocolVersion != 1 {
		return fmt.Errorf("unsupported Cline ACP version %d", handshake.ProtocolVersion)
	}
	var session struct {
		SessionID string
		Models    struct{ CurrentModelID string }
	}
	if err := client.call("session/new", map[string]any{"cwd": directory, "mcpServers": []any{}}, &session); err != nil {
		return err
	}
	if session.SessionID == "" {
		return errors.New("Cline returned no ACP session ID")
	}
	client.session = session.SessionID
	if model != "" && model != session.Models.CurrentModelID {
		if err := client.call("session/set_model", map[string]string{"sessionId": client.session, "modelId": model}, nil); err != nil {
			return err
		}
	}
	var result struct{ StopReason string }
	params := map[string]any{"sessionId": client.session, "prompt": []map[string]string{{"type": "text", "text": prompt}}}
	if err := client.call("session/prompt", params, &result); err != nil {
		return err
	}
	if result.StopReason != "end_turn" {
		return fmt.Errorf("Cline stopped with reason %q", result.StopReason)
	}
	return nil
}

type clineClient struct {
	out     *json.Encoder
	in      *bufio.Scanner
	session string
	seq     int
	granted func(string) bool
	text    io.Writer
}

func (c *clineClient) call(method string, params, result any) error {
	c.seq++
	if err := c.out.Encode(map[string]any{"jsonrpc": "2.0", "id": c.seq, "method": method, "params": params}); err != nil {
		return err
	}
	for c.in.Scan() {
		var frame struct {
			Envelope
			Result json.RawMessage
			Error  *struct{ Message string }
		}
		if err := json.Unmarshal(c.in.Bytes(), &frame); err != nil {
			return fmt.Errorf("invalid Cline ACP frame: %w", err)
		}
		if frame.Method != "" {
			if err := c.receive(frame.Envelope); err != nil {
				return err
			}
			continue
		}
		if string(frame.ID) != fmt.Sprint(c.seq) {
			return fmt.Errorf("unexpected Cline reply to %s", method)
		}
		if frame.Error != nil {
			return fmt.Errorf("Cline %s: %s", method, frame.Error.Message)
		}
		if len(frame.Result) == 0 {
			return fmt.Errorf("missing Cline %s result", method)
		}
		if result == nil {
			return nil
		}
		return json.Unmarshal(frame.Result, result)
	}
	if err := nativeScannerError(c.in); err != nil {
		return err
	}
	return fmt.Errorf("Cline closed ACP during %s: %w", method, io.ErrUnexpectedEOF)
}

func (c *clineClient) receive(frame Envelope) error {
	if len(frame.ID) > 0 {
		reply := map[string]any{"jsonrpc": "2.0", "id": frame.ID}
		if frame.Method == "session/request_permission" {
			reply["result"] = map[string]any{"outcome": c.permission(frame.Params)}
		} else {
			reply["error"] = map[string]any{"code": -32601, "message": "Unsupported ACP client method"}
		}
		return c.out.Encode(reply)
	}
	if frame.Method != "session/update" {
		return nil
	}
	var p struct {
		SessionID string
		Update    struct {
			SessionUpdate string
			Content       struct{ Type, Text string }
		}
	}
	if err := json.Unmarshal(frame.Params, &p); err != nil {
		return err
	}
	if c.session == "" || p.SessionID != c.session {
		return nil
	}
	switch p.Update.SessionUpdate {
	case "agent_message_chunk":
		if p.Update.Content.Type == "text" {
			_, err := io.WriteString(c.text, p.Update.Content.Text)
			return err
		}
	case "tool_call":
		_, err := io.WriteString(c.text, "\n")
		return err
	}
	return nil
}

func (c *clineClient) permission(params json.RawMessage) map[string]string {
	var req struct {
		SessionID string
		ToolCall  struct{ ToolCallID, Title, Kind string }
		Options   []struct{ OptionID, Kind string }
	}
	cancelled := map[string]string{"outcome": "cancelled"}
	if json.Unmarshal(params, &req) != nil || c.session == "" || req.SessionID != c.session || req.ToolCall.ToolCallID == "" {
		return cancelled
	}
	choice := "reject_once"
	if capability := clineToolCapability(req.ToolCall.Title, req.ToolCall.Kind); capability != "" && c.granted(capability) {
		choice = "allow_once"
	}
	for _, opt := range req.Options {
		if opt.Kind == choice && opt.OptionID == choice {
			return map[string]string{"outcome": "selected", "optionId": opt.OptionID}
		}
	}
	return cancelled
}

// Cline prefixes the display title with its native tool name. Match both the
// name and kind since local search and web search share the ACP "search" kind.
func clineToolCapability(title, kind string) string {
	name, _, _ := strings.Cut(title, ": ")
	switch kind + "/" + name {
	case "read/Read", "read/read_files", "search/Glob", "search/Grep", "search/search_codebase":
		return "workspace.read"
	case "edit/Edit", "edit/Write", "edit/editor", "edit/apply_patch", "other/apply_patch", "edit/NotebookEdit", "delete/Delete", "move/Move":
		return "workspace.write"
	case "execute/Bash", "execute/run_commands":
		return "command.run"
	case "fetch/WebFetch", "fetch/fetch_web_content", "search/WebSearch", "search/web_search":
		return "network.read"
	}
	return ""
}
