package harness

import (
	"encoding/json"
	"fmt"
	"io"
)

// RunAntigravity sends one prompt over agy's headless stream. Step text is
// previewed as it arrives, and only the terminal response can act on a Mitos
// request. Each invocation starts a fresh conversation with host-owned history.
func RunAntigravity(stream io.ReadWriter, output, preview io.Writer, prompt string) error {
	request := map[string]any{"event": "user", "message": map[string]string{"content": prompt}}
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		return err
	}
	scanner := nativeScanner(stream)
	for scanner.Scan() {
		if len(scanner.Bytes()) == 0 {
			continue
		}
		var frame struct {
			Event string
			Step  struct {
				Type string `json:"step_type"`
				Text string `json:"text_delta"`
			} `json:"step_update"`
			Result struct{ Status, Response, Error string }
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return fmt.Errorf("invalid Antigravity event: %w", err)
		}
		switch frame.Event {
		case "step_update":
			if frame.Step.Type == "agent_response" && frame.Step.Text != "" {
				if _, err := io.WriteString(preview, frame.Step.Text); err != nil {
					return err
				}
			}
		case "result":
			if frame.Result.Response != "" {
				if _, err := io.WriteString(output, frame.Result.Response); err != nil {
					return err
				}
			}
			if frame.Result.Status != "SUCCESS" {
				return fmt.Errorf("Antigravity ended with status %q: %s", frame.Result.Status, frame.Result.Error)
			}
			return nil
		}
	}
	if err := nativeScannerError(scanner); err != nil {
		return fmt.Errorf("reading Antigravity events: %w", err)
	}
	return fmt.Errorf("Antigravity closed without a result: %w", io.ErrUnexpectedEOF)
}
