package harness

// Claude Code's bidirectional stream-json control protocol. SDK MCP requests
// carry the same bounded handoff tools as Codex; no HTTP listener or credentials
// are exposed to the child. See the upstream claude-agent-sdk-python query and
// subprocess_cli implementations for the wire contract.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

func ClaudeArguments(options CodexOptions, nativeTools []string) []string {
	args := []string{"--print", "--verbose", "--input-format", "stream-json", "--output-format", "stream-json", "--permission-prompt-tool", "stdio", "--permission-mode", "default", "--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{"mitos":{"type":"sdk","name":"mitos"}}}`, "--tools", strings.Join(nativeTools, ","), "--disallowedTools", "Agent,Task"}
	if options.Model != "" {
		args = append(args, "--model", options.Model)
	}
	if options.ResumeThread != "" {
		args = append(args, "--resume="+options.ResumeThread)
	}
	if len(options.OutputSchema) > 0 {
		args = append(args, "--json-schema", string(options.OutputSchema))
	}
	if options.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprint(options.MaxTurns))
	}
	return args
}

type claudeFrame struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	RequestID string          `json:"request_id"`
	Request   json.RawMessage `json:"request"`
	Response  struct {
		RequestID string `json:"request_id"`
		Subtype   string `json:"subtype"`
		Error     string `json:"error"`
	} `json:"response"`
	SessionID  string          `json:"session_id"`
	Result     string          `json:"result"`
	IsError    bool            `json:"is_error"`
	Errors     []string        `json:"errors"`
	Structured json.RawMessage `json:"structured_output"`
	Message    struct {
		ID      string `json:"id"`
		Content []struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			ToolID string `json:"tool_use_id"`
			Name   string `json:"name"`
			Text   string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func RunClaude(ctx context.Context, broker Broker, stream io.ReadWriteCloser, output io.Writer, workspace, prompt string, options CodexOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var writes sync.Mutex
	send := func(v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writes.Lock()
		defer writes.Unlock()
		_, err = stream.Write(append(raw, '\n'))
		return err
	}
	hooks := map[string]any{}
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		hooks[event] = []any{map[string]any{"matcher": "Bash|Write|Edit|AskUserQuestion", "hookCallbackIds": []string{event}, "timeout": 3600}}
	}
	if err := send(map[string]any{"type": "control_request", "request_id": "mitos-init", "request": map[string]any{"subtype": "initialize", "hooks": hooks}}); err != nil {
		return err
	}
	frames := make(chan nativeFrame)
	go readNativeFrames(ctx, stream, frames)
	defer stream.Close()
	initTimer := time.NewTimer(30 * time.Second)
	defer initTimer.Stop()
	var ready bool
	var session string
	var requests sync.WaitGroup
	defer requests.Wait()
	// Cancellation must close a blocked writer before waiting for callbacks.
	defer func() { cancel(); _ = stream.Close() }()
	type pendingControl struct {
		cancel context.CancelFunc
		key    string
	}
	pending := map[string]pendingControl{}
	invalidate := func(key string) {
		if key == "" {
			return
		}
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_ = broker.Invalidate(cleanup, key)
	}
	failures := make(chan error, 1)
	report := func(err error) {
		if err != nil {
			select {
			case failures <- err:
			default:
			}
		}
	}
	steps := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			return err
		case <-initTimer.C:
			return errors.New("Claude control initialization timed out")
		case frame, ok := <-frames:
			if !ok {
				return errors.New("Claude stopped before a final result")
			}
			if frame.err != nil {
				return fmt.Errorf("Claude stopped before a final result: %w", frame.err)
			}
			var f claudeFrame
			if err := json.Unmarshal(frame.line, &f); err != nil {
				return fmt.Errorf("invalid Claude stream: %w", err)
			}
			// Raw protocol is saved separately from public activity.
			fmt.Fprintln(output, string(frame.line))
			if f.SessionID != "" && session == "" {
				session = f.SessionID
				if options.ThreadStarted != nil {
					if err := options.ThreadStarted(session); err != nil {
						return err
					}
				}
			} else if f.SessionID != "" && f.SessionID != session {
				return errors.New("Claude changed conversation identity")
			}
			switch f.Type {
			case "control_response":
				if f.Response.RequestID == "mitos-init" && !ready {
					if f.Response.Subtype != "success" {
						return fmt.Errorf("Claude initialization failed: %s", f.Response.Error)
					}
					ready = true
					initTimer.Stop()
					if err := send(map[string]any{"type": "user", "session_id": "", "message": map[string]any{"role": "user", "content": prompt}, "parent_tool_use_id": nil}); err != nil {
						return err
					}
				}
			case "control_cancel_request":
				if p, ok := pending[f.RequestID]; ok {
					p.cancel()
					requests.Add(1)
					go func() { defer requests.Done(); invalidate(p.key) }()
				}
			case "control_request":
				if f.RequestID == "" || pending[f.RequestID].cancel != nil || len(pending) >= 4096 {
					return errors.New("invalid or duplicate Claude control request")
				}
				requestCtx, stop := context.WithCancel(ctx)
				key := claudeControlKey(f.Request, session)
				pending[f.RequestID] = pendingControl{cancel: stop, key: key}
				requests.Add(1)
				go func(f claudeFrame, thread string) {
					defer requests.Done()
					defer func() {
						if requestCtx.Err() != nil {
							invalidate(key)
						}
						stop()
					}()
					value, err := claudeControl(requestCtx, broker, thread, workspace, f.Request, options)
					if requestCtx.Err() != nil {
						return
					}
					response := map[string]any{"subtype": "success", "request_id": f.RequestID, "response": value}
					if err != nil {
						// Hook transport errors are not necessarily blocking in native clients.
						// Return an explicit denial, then stop the attempt, rather than leave a
						// failed permission hook to a native fallback policy.
						var request struct {
							Subtype  string `json:"subtype"`
							Callback string `json:"callback_id"`
						}
						_ = json.Unmarshal(f.Request, &request)
						switch {
						case request.Subtype == "can_use_tool":
							response["response"] = map[string]any{"behavior": "deny", "message": err.Error(), "interrupt": true}
						case request.Subtype == "hook_callback" && request.Callback == "PreToolUse":
							response["response"] = map[string]any{"continue": false, "stopReason": err.Error(), "hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": err.Error()}}
						default:
							response = map[string]any{"subtype": "error", "request_id": f.RequestID, "error": err.Error()}
						}
					}
					report(send(map[string]any{"type": "control_response", "response": response}))
					if err != nil {
						report(err)
					}
				}(f, session)
			case "assistant", "user":
				for _, part := range f.Message.Content {
					switch part.Type {
					case "text":
						if f.Type == "assistant" {
							writeActivityRecord(options.Activity, map[string]any{"kind": "message", "id": f.Message.ID, "text": part.Text})
						}
					case "tool_use":
						writeActivityRecord(options.Activity, map[string]any{"kind": "tool", "id": part.ID, "tool": part.Name, "status": "running"})
					case "tool_result":
						steps[part.ToolID] = true
						writeActivityRecord(options.Activity, map[string]any{"kind": "tool", "id": part.ToolID, "status": "completed"})
						if options.MaxTurns > 0 && len(steps) >= options.MaxTurns {
							return errors.New("agent model-step limit reached")
						}
					}
				}
			case "result":
				if !ready {
					return errors.New("Claude returned a result before initialization")
				}
				if f.IsError || f.Subtype != "success" {
					return fmt.Errorf("Claude execution failed: %s %s %s", f.Subtype, f.Result, strings.Join(f.Errors, "; "))
				}
				if options.MaxTurns > 0 && len(steps)+1 > options.MaxTurns {
					return errors.New("agent model-step limit reached")
				}
				result := f.Result
				if len(options.OutputSchema) > 0 {
					if len(f.Structured) == 0 || string(f.Structured) == "null" {
						return errors.New("Claude did not return the required structured result")
					}
					result = string(f.Structured)
				}
				if options.Result != nil {
					return options.Result(result)
				}
				return nil
			}
		}
	}
}

func claudeControl(ctx context.Context, b Broker, thread, workspace string, raw json.RawMessage, options CodexOptions) (any, error) {
	var r struct {
		Subtype  string          `json:"subtype"`
		Server   string          `json:"server_name"`
		Message  json.RawMessage `json:"message"`
		Input    json.RawMessage `json:"input"`
		Tool     string          `json:"tool_name"`
		ToolID   string          `json:"tool_use_id"`
		Callback string          `json:"callback_id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Subtype == "mcp_message" {
		if r.Server != "mitos" {
			return nil, errors.New("unknown SDK MCP server")
		}
		response, err := claudeMCP(ctx, r.Message, options)
		return map[string]any{"mcp_response": response}, err
	}
	if r.Subtype != "hook_callback" && r.Subtype != "can_use_tool" {
		return nil, errors.New("unsupported Claude control request")
	}
	hook := r.Input
	if r.Subtype == "can_use_tool" {
		hook, _ = json.Marshal(map[string]any{"session_id": thread, "tool_use_id": r.ToolID, "hook_event_name": "PreToolUse", "tool_name": r.Tool, "tool_input": r.Input, "cwd": workspace})
	}
	var h struct {
		Event  string `json:"hook_event_name"`
		Thread string `json:"session_id"`
		ToolID string `json:"tool_use_id"`
	}
	if json.Unmarshal(hook, &h) != nil || h.Thread == "" || h.Thread != thread || h.ToolID == "" {
		return nil, errors.New("invalid Claude hook identity")
	}
	if r.Subtype == "hook_callback" && h.Event != r.Callback {
		return nil, errors.New("Claude hook callback mismatch")
	}
	key := SourceKey(h.Thread, h.ToolID)
	if h.Event == "PostToolUse" || h.Event == "PostToolUseFailure" {
		answer, err := b.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		if answer.State == "pending" {
			return nil, errors.New("tool completed before approval")
		}
		return map[string]any{}, b.Deliver(ctx, key, answer.Digest, "applied")
	}
	if _, err := Normalize("claude", hook); err != nil {
		// SDK handoff tools are the explicitly granted host protocol, not arbitrary
		// MCP servers. Other unknown permission requests fail closed.
		if r.Subtype == "can_use_tool" {
			for _, tool := range options.Tools {
				if r.Tool == "mcp__mitos__"+tool.Name {
					return map[string]any{"behavior": "allow", "updatedInput": r.Input}, nil
				}
			}
		}
		return nil, err
	}
	if observer, ok := b.(Observer); ok {
		if err := observer.Observe(ctx, hook); err != nil {
			return nil, err
		}
	}
	submitted, err := b.Submit(ctx, hook)
	if err != nil {
		return nil, err
	}
	answer, err := Wait(ctx, b, submitted.RequestID)
	if err != nil {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = b.Invalidate(cleanup, submitted.RequestID)
		stop()
		return nil, err
	}
	var value map[string]any
	if err = json.Unmarshal(answer.Body, &value); err != nil {
		return nil, err
	}
	if r.Subtype == "hook_callback" {
		return value, nil
	}
	details, _ := value["hookSpecificOutput"].(map[string]any)
	if details["permissionDecision"] != "allow" {
		return map[string]any{"behavior": "deny", "message": "Denied by Mitos for this request."}, nil
	}
	input := any(r.Input)
	if v, ok := details["updatedInput"]; ok {
		input = v
	}
	return map[string]any{"behavior": "allow", "updatedInput": input}, nil
}
func claudeMCP(ctx context.Context, raw json.RawMessage, options CodexOptions) (any, error) {
	var r struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var result any
	switch r.Method {
	case "initialize":
		result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "mitos", "version": "1"}}
	case "notifications/initialized", "ping":
		result = map[string]any{}
	case "tools/list":
		tools := []map[string]any{}
		for _, t := range options.Tools {
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema})
		}
		result = map[string]any{"tools": tools}
	case "tools/call":
		found := false
		for _, t := range options.Tools {
			if t.Name == r.Params.Name {
				found = true
			}
		}
		if !found || options.ToolCall == nil {
			return nil, errors.New("tool is not granted")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value, err := options.ToolCall(r.Params.Name, r.Params.Arguments)
		var encoded []byte
		if err != nil {
			encoded = []byte(err.Error())
		} else {
			encoded, _ = json.Marshal(value)
		}
		result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(encoded)}}, "isError": err != nil}
	default:
		return nil, errors.New("unsupported MCP method")
	}
	return map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result}, nil
}

func claudeControlKey(raw json.RawMessage, thread string) string {
	var r struct {
		Subtype string          `json:"subtype"`
		ToolID  string          `json:"tool_use_id"`
		Input   json.RawMessage `json:"input"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return ""
	}
	if r.Subtype == "can_use_tool" && thread != "" && r.ToolID != "" {
		return SourceKey(thread, r.ToolID)
	}
	if r.Subtype == "hook_callback" {
		var h struct {
			Thread string `json:"session_id"`
			ToolID string `json:"tool_use_id"`
			Event  string `json:"hook_event_name"`
		}
		if json.Unmarshal(r.Input, &h) == nil && h.Thread == thread && h.ToolID != "" && h.Event == "PreToolUse" {
			return SourceKey(thread, h.ToolID)
		}
	}
	return ""
}
