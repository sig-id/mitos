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
