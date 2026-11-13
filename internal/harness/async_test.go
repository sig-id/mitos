package harness

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mitos/internal/decision"
	"net"
	"strings"
	"testing"
	"time"
)

type asyncTestBroker struct {
	answer    Delivery
	delivered chan string
	request   chan Request
}

func (b *asyncTestBroker) Submit(_ context.Context, raw json.RawMessage) (Delivery, error) {
	r, err := Normalize("codex", raw)
	if err != nil {
		return Delivery{}, err
	}
	body, err := Response("codex", r, []decision.Resolution{{Choice: "answer", Value: json.RawMessage(`"blue"`)}})
	b.answer = Delivery{RequestID: r.Key, State: "accepted", Body: body, Digest: "answer"}
	b.request <- r
	return b.answer, err
}
func (b *asyncTestBroker) Get(context.Context, string) (Delivery, error) { return b.answer, nil }
func (b *asyncTestBroker) Deliver(_ context.Context, _, _, state string) error {
	b.delivered <- state
	return nil
}
func (b *asyncTestBroker) Invalidate(context.Context, string) error { return nil }
func TestAsyncQuestionsSteerWithoutStoppingServerEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	broker := &asyncTestBroker{delivered: make(chan string, 1), request: make(chan Request, 1)}
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- ProxyCodex(ctx, broker, client, input, &output) }()
	fmt.Fprintln(server, `{"method":"item/completed","params":{"threadId":"t","turnId":"u","item":{"type":"agentMessage","id":"q","delivery":"async","questions":[{"title":"Which color?","options":["blue","red"]}],"text":"Which color?"}}}`)
	fmt.Fprintln(server, `{"method":"item/agentMessage/delta","params":{"delta":"Continuing independent work"}}`)
	scanner := bufio.NewScanner(server)
	if !scanner.Scan() {
		t.Fatal(scanner.Err())
	}
	var steer struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params struct {
			Turn string `json:"expectedTurnId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &steer); err != nil {
		t.Fatal(err)
	}
	if steer.Method != "turn/steer" || steer.Params.Turn != "u" || !strings.Contains(scanner.Text(), "Which color?") {
		t.Fatal(scanner.Text())
	}
	select {
	case <-broker.delivered:
		t.Fatal("marked applied before source acknowledgment")
	default:
	}
	fmt.Fprintf(server, "{\"id\":%q,\"result\":{}}\n", steer.ID)
	select {
	case state := <-broker.delivered:
		if state != "applied" {
			t.Fatal(state)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	server.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Continuing independent work") {
		t.Fatal(output.String())
	}
}
