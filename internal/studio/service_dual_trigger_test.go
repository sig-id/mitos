package studio

import (
	"encoding/json"
	"testing"
	"time"

	"mitos/internal/messaging"
	"mitos/internal/service"
)

func TestCronAndMQTTShareDurableQueue(t *testing.T) {
	e, s, v := cronEngine(t)
	if err := s.ensureHome(); err != nil {
		t.Fatal(err)
	}
	v.Catalog.Config.Units[0].Events = service.EventPatterns{"github.pr.changed"}
	v, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	due := *v.States[0].NextAt
	if err = e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"name":"github.pr.changed","payload":{"number":1}}`)
	m := messaging.Envelope{Version: 1, ID: "pr-event", From: s.home.ID, To: s.home.ID, Project: "studio", Mailbox: "events", Kind: "event", Type: "service.event", Deadline: time.Now().Add(time.Hour), Payload: payload, Digest: messaging.Hash(payload)}
	receipt, err := s.AcceptMessage(m)
	if err != nil || receipt.Status != "applied" {
		t.Fatal(receipt, err)
	}
	if _, err = s.AcceptMessage(m); err != nil {
		t.Fatal(err)
	}
	// One cron task plus one pending event exhausts the shared bound.
	if err = e.scheduleServices(due.Add(15*time.Minute), false); err != nil {
		t.Fatal(err)
	}
	view, _ := s.Services()
	if view.States[0].Skipped != 1 || len(s.List()) != 1 {
		t.Fatal("sources did not share capacity", view.States)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	e.Store = replay
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	if len(replay.List()) != 2 {
		t.Fatal("pending event lost across restart")
	}
	if _, err = replay.AcceptMessage(m); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil || len(replay.List()) != 2 {
		t.Fatal("duplicate delivery", err)
	}
	if _, err = AuditAutonomyLogs(replay.Dir()); err != nil {
		t.Fatal(err)
	}
}
