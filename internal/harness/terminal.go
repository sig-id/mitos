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
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	proxyInput, sendPipe := io.Pipe()
	defer proxyInput.Close()
	defer sendPipe.Close()
	readPipe, proxyOutput := io.Pipe()
	defer readPipe.Close()
	defer proxyOutput.Close()
	stopped := make(chan error, 1)
	go func() { stopped <- ProxyCodex(ctx, b, server, proxyInput, proxyOutput) }()
	messages := make(chan json.RawMessage, 32)
	go func() {
		defer close(messages)
		scan := bufio.NewScanner(readPipe)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			line := append(json.RawMessage(nil), scan.Bytes()...)
			select {
			case messages <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
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
	nextWire := func() (json.RawMessage, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-stopped:
			if err == nil {
				err = io.EOF
			}
			return nil, err
		case line, ok := <-messages:
			if !ok {
				return nil, io.EOF
			}
			return line, nil
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
	if err := send(map[string]any{"id": 2, "method": "thread/start", "params": map[string]any{"cwd": workspace}}); err != nil {
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
	fmt.Fprintln(output, "Connected to Codex. Review supported requests in Mitos or on the paired device.")
	once := prompt != ""
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
		if plan {
			params["collaborationMode"] = map[string]any{"mode": "plan", "settings": map[string]any{"model": thread.Model}}
		}
		if err = send(map[string]any{"id": requestID, "method": "turn/start", "params": params}); err != nil {
			return err
		}
		if _, err = wait(requestID); err != nil {
			return err
		}
		requestID++
		for {
			raw, err = read()
			if err != nil {
				return err
			}
			var event Envelope
			if json.Unmarshal(raw, &event) != nil {
				return errors.New("invalid app-server event")
			}
			if len(event.ID) > 0 && event.Method != "" {
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
			if event.Method == "item/agentMessage/delta" {
				var p struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal(event.Params, &p)
				fmt.Fprint(output, p.Delta)
			}
			if event.Method == "turn/completed" {
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
