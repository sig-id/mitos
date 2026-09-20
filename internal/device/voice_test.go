package device

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCommandTranscriberBoundsAndCancellation(t *testing.T) {
	for _, mode := range []string{"transcript", "oversize", "failure", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			script := "#!/bin/sh\n"
			switch mode {
			case "transcript":
				script += "test \"$MITOS_STT_FORMAT\" = s16le || exit 1\ntest \"$MITOS_STT_SAMPLE_RATE\" = 16000 || exit 1\ncat >/dev/null\nprintf Close\n"
			case "oversize":
				script += "printf '" + strings.Repeat("a", 1025) + "'\n"
			case "failure":
				script += "printf 'private provider error' >&2\nexit 1\n"
			case "timeout":
				script += "exec sleep 10\n"
			}
			path := filepath.Join(t.TempDir(), "transcribe")
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			text, err := (CommandTranscriber{Path: path}).Transcribe(ctx, []byte{0, 0}, "en")
			if mode == "transcript" {
				if err != nil || text != "Close" {
					t.Fatal(text, err)
				}
			} else if err == nil || text != "" {
				t.Fatal("provider failure was not bounded", len(text), err)
			}
		})
	}
}
