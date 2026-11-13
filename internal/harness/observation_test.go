package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

type rejectingObserver struct{ asyncTestBroker }

func (b *rejectingObserver) Observe(context.Context, json.RawMessage) error {
	return errors.New("independent veto")
}
func TestProxyStopsOnObservationVeto(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- ProxyCodex(ctx, &rejectingObserver{}, client, input, &output) }()
	fmt.Fprintln(server, `{"method":"item/started","params":{"item":{"type":"commandExecution","command":"forbidden"}}}`)
	if err := <-done; err == nil || err.Error() != "independent veto" {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("vetoed observation forwarded")
	}
}

func TestLargeObservationBoundsOnlyResults(t *testing.T) {
	for _, typ := range []string{"commandExecution", "mcpToolCall"} {
		item := map[string]any{"type": typ, "command": "gh api repos/example/project/pulls", "cwd": "/work", "arguments": map[string]string{"url": "https://example.test"}, "aggregatedOutput": string(bytes.Repeat([]byte("x"), 100000)), "result": map[string]any{"content": string(bytes.Repeat([]byte("y"), 100000))}}
		raw, _ := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"item": item}})
		bounded, err := SupervisionObservation(raw)
		if err != nil || len(bounded) > 24<<10 {
			t.Fatalf("large %s result rejected: %v", typ, err)
		}
		var event struct {
			Params struct {
				Item map[string]json.RawMessage `json:"item"`
			} `json:"params"`
		}
		json.Unmarshal(bounded, &event)
		for _, key := range []string{"command", "cwd", "arguments"} {
			want, _ := json.Marshal(item[key])
			if !bytes.Equal(want, event.Params.Item[key]) {
				t.Fatal("action metadata changed", key)
			}
		}
		if !bytes.Contains(event.Params.Item["result"], []byte(`"truncated":true`)) || !bytes.Contains(event.Params.Item["result"], []byte(`"sha256"`)) {
			t.Fatal("missing truncation evidence")
		}
	}
	raw, _ := json.Marshal(map[string]any{"params": map[string]any{"item": map[string]any{"command": string(bytes.Repeat([]byte("z"), 30000))}}})
	if _, err := SupervisionObservation(raw); err == nil {
		t.Fatal("oversized command silently truncated")
	}
}
