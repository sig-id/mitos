package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func sizedObservation(size int) []byte {
	const prefix = `{"method":"item/completed","params":{"item":{"type":"mcpToolCall","result":"`
	const suffix = `"}}}`
	return []byte(prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix)
}

type captureObserver struct {
	asyncTestBroker
	events []json.RawMessage
}

func (b *captureObserver) Observe(_ context.Context, raw json.RawMessage) error {
	b.events = append(b.events, append(json.RawMessage(nil), raw...))
	return nil
}

func TestProxyObservationAtFrameLimit(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n", ""} {
		t.Run(fmt.Sprintf("ending-%q", ending), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			input, writer := io.Pipe()
			defer input.Close()
			defer writer.Close()
			raw := sizedObservation(MaxObservationBytes)
			go func() { defer server.Close(); server.Write(append(raw, []byte(ending)...)) }()
			broker := &captureObserver{}
			var output bytes.Buffer
			if err := ProxyCodex(ctx, broker, client, input, &output); err != nil {
				t.Fatal(err)
			}
			if len(broker.events) != 1 || !bytes.Equal(broker.events[0], raw) {
				t.Fatal("valid observation did not reach supervision unchanged")
			}
			if !bytes.Equal(output.Bytes(), append(raw, '\n')) {
				t.Fatal("valid observation was not forwarded")
			}
		})
	}
}

func TestCodexRetainsSupervisionFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	// Fail while initialization is waiting for its response.
	go func() {
		defer server.Close()
		b := make([]byte, 4096)
		server.Read(b)
		fmt.Fprintln(server, `{"method":"item/started","params":{"item":{"type":"commandExecution","command":"forbidden"}}}`)
	}()
	err := RunCodex(ctx, &rejectingObserver{}, client, strings.NewReader(""), io.Discard, t.TempDir(), "test", CodexOptions{Headless: true})
	if err == nil || err.Error() != "independent veto" {
		t.Fatalf("lost supervision failure: %v", err)
	}
}

func TestProxyRejectsInvalidFramesWithoutForwarding(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  []byte
		want string
	}{
		{"oversized-newline", append(sizedObservation(MaxObservationBytes+1), '\n'), errNativeFrameTooLarge.Error()},
		{"oversized-eof", sizedObservation(MaxObservationBytes + 1), errNativeFrameTooLarge.Error()},
		{"oversized-unterminated", bytes.Repeat([]byte("x"), MaxObservationBytes+32), errNativeFrameTooLarge.Error()},
		{"malformed", []byte("{broken}\n"), "invalid app-server JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			client, server := net.Pipe()
			defer server.Close()
			input, writer := io.Pipe()
			defer input.Close()
			defer writer.Close()
			go func() { defer server.Close(); server.Write(tc.raw) }()
			broker := &captureObserver{}
			var output bytes.Buffer
			err := ProxyCodex(ctx, broker, client, input, &output)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("failure lost: %v", err)
			}
			if len(broker.events) != 0 || output.Len() != 0 {
				t.Fatal("invalid event forwarded")
			}
		})
	}
}

func TestCodexConsumesLimitEventAndFinalResultBeforeEOF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	raw := sizedObservation(MaxObservationBytes)
	go func() {
		defer server.Close()
		scanner := bufio.NewScanner(server)
		for scanner.Scan() {
			var request Envelope
			_ = json.Unmarshal(scanner.Bytes(), &request)
			switch request.Method {
			case "initialize":
				fmt.Fprintln(server, `{"id":1,"result":{}}`)
			case "thread/start":
				fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"thread"}}}`)
			case "turn/start":
				fmt.Fprintln(server, `{"id":3,"result":{"turn":{"id":"turn"}}}`)
				fmt.Fprintf(server, "%s\n", raw)
				fmt.Fprintln(server, `{"method":"item/completed","params":{"item":{"type":"agentMessage","phase":"final_answer","text":"done"}}}`)
				fmt.Fprintln(server, `{"method":"turn/completed","params":{"turn":{"id":"turn","status":"completed"}}}`)
				return
			}
		}
	}()
	broker := &captureObserver{}
	result := ""
	err := RunCodex(ctx, broker, client, strings.NewReader(""), io.Discard, t.TempDir(), "test", CodexOptions{Headless: true, Result: func(text string) error { result = text; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if result != "done" || len(broker.events) != 1 || !bytes.Equal(broker.events[0], raw) {
		t.Fatal("large supervised event or final result lost")
	}
}
