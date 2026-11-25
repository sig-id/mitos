package harness

import (
	"io"
	"strings"
	"testing"
)

func TestAntigravityRequiresSuccessfulResult(t *testing.T) {
	for _, tc := range []struct {
		name, frames, wantError, wantOutput string
	}{
		{"authentication", `{"event":"result","result":{"status":"ERROR","error":"authentication failed or timed out"}}`, "authentication failed or timed out", ""},
		{"canceled", `{"event":"result","result":{"status":"CANCELED","response":"partial work"}}`, "CANCELED", "partial work"},
		{"missing result", `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"partial work"}}`, "closed without a result", ""},
		{"malformed", `{"event":`, "invalid Antigravity event", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := struct {
				io.Reader
				io.Writer
			}{strings.NewReader(tc.frames), io.Discard}
			var output strings.Builder
			err := RunAntigravity(stream, &output, io.Discard, "Inspect the workspace")
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || output.String() != tc.wantOutput {
				t.Fatalf("output %q, error %v", output.String(), err)
			}
		})
	}
}
