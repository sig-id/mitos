package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// RunCodexTerminal supplies a small interactive app-server client for Mitos desk
// terminals. Supported questions go to Mitos; unsupported native requests remain
// visible with their exact payload and require an explicit local JSON response.
func RunCodexTerminal(ctx context.Context, b Broker, server io.ReadWriteCloser, input io.Reader, output io.Writer, workspace, prompt string, plan bool) error {
	return RunCodexTerminalOptions(ctx, b, server, input, output, workspace, prompt, plan, false)
}

func RunCodexTerminalOptions(ctx context.Context, b Broker, server io.ReadWriteCloser, input io.Reader, output io.Writer, workspace, prompt string, plan, keepOpen bool) error {
	return RunCodex(ctx, b, server, input, output, workspace, prompt, CodexOptions{Plan: plan, KeepOpen: keepOpen})
}

type CodexOptions struct {
	Plan, KeepOpen, Headless bool
	Model, Sandbox           string
	ResumeThread             string
	ThreadStarted            func(string) error
	OutputSchema             json.RawMessage
	Result                   func(string) error
}

func RunCodex(ctx context.Context, b Broker, server io.ReadWriteCloser, input io.Reader, output io.Writer, workspace, prompt string, options CodexOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	proxyInput, sendPipe := io.Pipe()
	defer proxyInput.Close()
	defer sendPipe.Close()
	readPipe, proxyOutput := io.Pipe()
	defer readPipe.Close()
	defer proxyOutput.Close()
	go func() {
		err := ProxyCodex(ctx, b, server, proxyInput, proxyOutput)
		_ = proxyOutput.CloseWithError(err)
	}()
	messages := make(chan nativeFrame, 32)
	go readNativeFrames(ctx, readPipe, messages)
	userLines := make(chan string)
	go func() {
		defer close(userLines)
		scan := bufio.NewScanner(input)
		scan.Buffer(make([]byte, 4096), 32<<10)
		for scan.Scan() {
			select {
			case userLines <- scan.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	send := func(v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = sendPipe.Write(append(raw, '\n'))
		return err
	}
	var steering <-chan string
	var activeThread, activeTurn string
	steerSequence := 0
	nextWire := func() (json.RawMessage, error) {
		for {
			select {
			case text, ok := <-steering:
				if !ok {
					steering = nil
					continue
				}
				steerSequence++
				if err := send(map[string]any{"id": fmt.Sprintf("mitos-input:%d", steerSequence), "method": "turn/steer", "params": map[string]any{"threadId": activeThread, "expectedTurnId": activeTurn, "input": []any{map[string]any{"type": "text", "text": text}}}}); err != nil {
					return nil, err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			case frame, ok := <-messages:
				if !ok {
					return nil, io.EOF
				}
				return frame.line, frame.err
			}
		}
	}
	var backlog []json.RawMessage
	read := func() (json.RawMessage, error) {
		if len(backlog) > 0 {
			raw := backlog[0]
			backlog = backlog[1:]
			return raw, nil
		}
		return nextWire()
	}
	wait := func(id int) (json.RawMessage, error) {
		for {
			raw, err := nextWire()
			if err != nil {
				return nil, err
			}
			var r struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(raw, &r) == nil && r.ID == id && r.Method == "" {
				if len(r.Error) > 0 {
					return nil, fmt.Errorf("app-server: %s", r.Error)
				}
				return r.Result, nil
			}
			if len(backlog) >= 256 {
				return nil, errors.New("too many app-server events before RPC response")
			}
			backlog = append(backlog, raw)
		}
	}
	if err := send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "mitos", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}}); err != nil {
		return err
	}
	if _, err := wait(1); err != nil {
		return err
	}
	if err := send(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	threadParams := map[string]any{"cwd": workspace}
	if options.Model != "" {
		threadParams["model"] = options.Model
	}
	if options.Sandbox != "" {
		threadParams["sandbox"] = options.Sandbox
	}
	if options.Headless {
		threadParams["approvalPolicy"] = "on-request"
		threadParams["approvalsReviewer"] = "user"
		threadParams["ephemeral"] = options.ThreadStarted == nil
	}
	method := "thread/start"
	if options.ResumeThread != "" {
		method = "thread/resume"
		threadParams["threadId"] = options.ResumeThread
		delete(threadParams, "ephemeral")
	}
	if err := send(map[string]any{"id": 2, "method": method, "params": threadParams}); err != nil {
		return err
	}
	raw, err := wait(2)
	if err != nil {
		return err
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &thread) != nil || thread.Thread.ID == "" {
		return errors.New("app-server did not create a thread")
	}
	if options.ResumeThread != "" && options.ResumeThread != thread.Thread.ID {
		return errors.New("app-server resumed a different conversation")
	}
	if options.ThreadStarted != nil {
		if err := options.ThreadStarted(thread.Thread.ID); err != nil {
			return err
		}
	}
	if !options.Headless {
		fmt.Fprintln(output, "Connected to Codex. Review supported requests in Mitos or on the paired device.")
	}
	once := prompt != "" && !options.KeepOpen
	requestID := 3
	for {
		if prompt == "" {
			fmt.Fprint(output, "\nYou> ")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case text, ok := <-userLines:
				if !ok {
					return nil
				}
				prompt = text
			}
			if prompt == "" {
				continue
			}
		}
		params := map[string]any{"threadId": thread.Thread.ID, "input": []any{map[string]any{"type": "text", "text": prompt}}}
		if len(options.OutputSchema) > 0 {
			params["outputSchema"] = options.OutputSchema
		}
		if options.Plan {
			params["collaborationMode"] = map[string]any{"mode": "plan", "settings": map[string]any{"model": thread.Model}}
		}
		if err = send(map[string]any{"id": requestID, "method": "turn/start", "params": params}); err != nil {
			return err
		}
		started, err := wait(requestID)
		if err != nil {
			return err
		}
		var startedTurn struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(started, &startedTurn)
		activeThread, activeTurn = thread.Thread.ID, startedTurn.Turn.ID
		if !options.Headless && activeTurn != "" {
			steering = userLines
		}
		requestID++
		finalText := ""
		for {
			raw, err = read()
			if err != nil {
				return err
			}
			var event Envelope
			if json.Unmarshal(raw, &event) != nil {
				return errors.New("invalid app-server event")
			}
			if options.Headless {
				writeCodexActivity(output, event)
			}
			if len(event.ID) > 0 && event.Method != "" {
				if options.Headless {
					return fmt.Errorf("unsupported native request requires an explicit session: %s", event.Method)
				}
				fmt.Fprintf(output, "\nThis request requires the local console. Native request:\n%s\nEnter its JSON result, or Ctrl-C to stop:\n", raw)
				var text string
				select {
				case <-ctx.Done():
					return ctx.Err()
				case line, ok := <-userLines:
					if !ok {
						return io.EOF
					}
					text = line
				}
				if !json.Valid([]byte(text)) {
					return errors.New("invalid native response JSON")
				}
				if err = send(map[string]any{"id": event.ID, "result": json.RawMessage(text)}); err != nil {
					return err
				}
			}
			if len(event.ID) > 0 && event.Method == "" {
				var response struct {
					Error json.RawMessage `json:"error"`
				}
				_ = json.Unmarshal(raw, &response)
				if len(response.Error) > 0 && string(response.Error) != "null" {
					fmt.Fprintln(output, "\nLive input was rejected; retry after this turn or answer the request in Inbox.")
				}
			}
			if event.Method == "item/agentMessage/delta" {
				var p struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal(event.Params, &p)
				fmt.Fprint(output, p.Delta)
			}
			if event.Method == "item/completed" {
				var p struct {
					Item struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						Phase    string `json:"phase"`
						Delivery string `json:"delivery"`
					} `json:"item"`
				}
				if json.Unmarshal(event.Params, &p) == nil && p.Item.Type == "agentMessage" && p.Item.Delivery != "async" && (p.Item.Phase == "final_answer" || p.Item.Phase == "") {
					finalText = p.Item.Text
				}
			}
			if event.Method == "turn/completed" {
				steering = nil
				var p struct {
					Turn struct {
						Status string `json:"status"`
					} `json:"turn"`
				}
				_ = json.Unmarshal(event.Params, &p)
				if p.Turn.Status == "failed" || p.Turn.Status == "interrupted" {
					return fmt.Errorf("Codex turn %s", p.Turn.Status)
				}
				if options.Headless && finalText == "" {
					return errors.New("Codex completed without a final result")
				}
				if options.Result != nil {
					if err := options.Result(finalText); err != nil {
						return err
					}
				}
				fmt.Fprintln(output)
				break
			}
		}
		if once {
			return nil
		}
		prompt = ""
	}
}
