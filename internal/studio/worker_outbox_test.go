package studio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/messaging"
	"mitos/internal/workerwake"
)

func TestWorkerOutboxDurabilityAndSharedMQTTHints(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	if _, err := e.LaunchStructured(structuredBrief("private-task-secret", "agent:mitos.generalist")); err != nil {
		t.Fatal(err)
	}
	pending := e.Store.PendingWorkerOutbox(1)
	if len(pending) != 1 || pending[0].Pool != "interactive" {
		t.Fatal("admission wake not committed", pending)
	}
	item := pending[0]
	raw, _ := json.Marshal(workerwake.Hint{Pool: item.Pool, Capability: item.Capability})
	if strings.Contains(string(raw), "private-task-secret") || strings.Contains(string(raw), "task_id") {
		t.Fatal("task leaked in wake", string(raw))
	}
	dir := e.Store.Dir()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.PendingWorkerOutbox(1); len(got) != 1 || got[0] != item {
		t.Fatal("unpublished wake lost on restart", got)
	}
	broker, address := mqttBroker(t, "127.0.0.1:0")
	defer broker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg := messaging.Config{Broker: "mqtt://" + address}
	publisher, err := workerwake.New(ctx, cfg, s.Home().ID, "")
	if err != nil {
		t.Fatal(err)
	}
	defer publisher.Close()
	one, err := workerwake.New(ctx, cfg, s.Home().ID, "interactive")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := workerwake.New(ctx, cfg, s.Home().ID, "interactive")
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	for _, client := range []*workerwake.Client{publisher, one, two} {
		if err = client.WaitReady(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = publisher.Publish(ctx, workerwake.Hint{Pool: item.Pool, Capability: item.Capability}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-one.Wake:
	case <-two.Wake:
	case <-ctx.Done():
		t.Fatal("shared subscribers missed live hint")
	}
	select {
	case <-one.Wake:
		t.Fatal("shared hint broadcast twice")
	case <-two.Wake:
		t.Fatal("shared hint broadcast twice")
	case <-time.After(150 * time.Millisecond):
	}
	// A lost publisher acknowledgement can repeat the hint without assigning work.
	if err = publisher.Publish(ctx, workerwake.Hint{Pool: item.Pool, Capability: item.Capability}); err != nil {
		t.Fatal(err)
	}
	if len(s.PendingWorkerOutbox(1)) != 1 {
		t.Fatal("publish alone acknowledged outbox")
	}
	if err = s.ackWorkerOutbox(item.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.PendingWorkerOutbox(1)) != 0 {
		t.Fatal("published wake retained as pending")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.PendingWorkerOutbox(1)) != 0 {
		t.Fatal("acknowledged wake replayed")
	}
}
