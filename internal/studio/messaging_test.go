package studio

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"mitos/internal/messaging"
)

func messageStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ensureHome(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func messageEnvelope(t *testing.T, s *Store, id string) messaging.Envelope {
	t.Helper()
	p, _ := messaging.Normalize([]byte(`{"name":"inventory.ready","payload":{"n":9007199254740993}}`))
	return messaging.Envelope{Version: 1, ID: id, From: "remote", To: s.Home().ID, Project: "studio", Mailbox: "events", Kind: "command", Type: "service.event", Deadline: time.Now().UTC().Add(time.Hour), Payload: p, Digest: messaging.Hash(p)}
}
func TestMessageApplicationAtomicReplayAndConflict(t *testing.T) {
	s := messageStore(t)
	m := messageEnvelope(t, s, "once")
	r, err := s.AcceptMessage(m)
	if err != nil || r.Status != "applied" {
		t.Fatal(r, err)
	}
	seq := s.seq
	if len(s.CustomEvents()) != 1 || s.CustomEvents()[0].Seq != seq || s.MessageInbox("studio", "events", 0)[0].Sequence != seq {
		t.Fatal("effect and inbox are not one journal record")
	}
	again, err := s.AcceptMessage(m)
	if err != nil || again != r || s.seq != seq {
		t.Fatal("duplicate reapplied", again, err)
	}
	m.Type = "other"
	if _, err = s.AcceptMessage(m); !errors.Is(err, messaging.ErrConflict) {
		t.Fatal("changed identity did not conflict", err)
	}
	m.Type = "service.event"
	dir := s.Dir()
	s.Close()
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	again, err = s.AcceptMessage(m)
	if err != nil || again != r || s.seq != seq || len(s.CustomEvents()) != 1 {
		t.Fatal("replay duplicated effect", again, err)
	}
}
func TestMessageFailedCommitIsNotAcknowledged(t *testing.T) {
	s := messageStore(t)
	m := messageEnvelope(t, s, "failed")
	s.file = &controlledLog{eventLog: s.file, failure: errors.New("sync failed")}
	if _, err := s.AcceptMessage(m); err == nil {
		t.Fatal("acknowledged failed sync")
	}
	if len(s.MessageInbox("studio", "events", 0)) != 0 || len(s.CustomEvents()) != 0 {
		t.Fatal("published uncommitted state")
	}
	if _, err := s.AcceptMessage(m); err == nil {
		t.Fatal("poisoned writer accepted retry")
	}
	// The complete write may survive a failed sync. Recovery must deduplicate it.
	dir := s.Dir()
	s.Close()
	recovered, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if r, err := recovered.AcceptMessage(m); err != nil || r.Status != "applied" || len(recovered.CustomEvents()) != 1 {
		t.Fatal("uncertain commit recovery", r, err)
	}
}
func TestMessageGenerationDeadlinesAndAcknowledgment(t *testing.T) {
	s := messageStore(t)
	if err := s.SaveDesk([]byte(`{"version":1,"responsibilities":[{"id":"supervisor","project_id":"project","generation":2}]}`)); err != nil {
		t.Fatal(err)
	}
	m := messageEnvelope(t, s, "stale")
	m.Kind = "note"
	m.Type = "text"
	m.Project = "project"
	m.Mailbox = "supervisor"
	m.Generation = 1
	m.Payload = []byte(`{"body":"review this"}`)
	m.Digest = messaging.Hash(m.Payload)
	r, err := s.AcceptMessage(m)
	if err != nil || r.Status != "rejected" {
		t.Fatal("accepted stale generation", r, err)
	}
	m.ID = "current"
	m.Generation = 2
	r, err = s.AcceptMessage(m)
	if err != nil || r.Status != "accepted" {
		t.Fatal(r, err)
	}
	if err = s.AcknowledgeMessage(m.From, m.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale acknowledgment", err)
	}
	if err = s.AcknowledgeMessage(m.From, m.ID, 2); err != nil {
		t.Fatal(err)
	}
	if inbox := s.MessageInbox("project", "supervisor", 0); len(inbox) != 2 || inbox[1].AckGeneration != 2 {
		t.Fatal("acknowledgment lost generation", inbox)
	}
	seq := s.seq
	if err = s.AcknowledgeMessage(m.From, m.ID, 2); err != nil || s.seq != seq {
		t.Fatal("duplicate ack", err)
	}
	m.ID = "expired"
	m.Deadline = time.Now().Add(-time.Second)
	r, err = s.AcceptMessage(m)
	if err != nil || r.Status != "expired" {
		t.Fatal(r, err)
	}
	m = messageEnvelope(t, s, "bad-event")
	m.Payload = []byte(`{"name":"service.forged","payload":{}}`)
	m.Digest = messaging.Hash(m.Payload)
	r, err = s.AcceptMessage(m)
	if err != nil || r.Status != "rejected" || len(s.CustomEvents()) != 0 {
		t.Fatal("reserved event admitted", r, err)
	}
}
func TestMessageOutboxRetriesUnknownAndLateReceipt(t *testing.T) {
	s := messageStore(t)
	m := messageEnvelope(t, s, "outgoing")
	m.From = s.Home().ID
	m.To = "remote"
	v, err := s.QueueMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.AttemptMessage(m.ID, v.NextAttempt, 1)
	if err != nil || v.Attempts != 1 {
		t.Fatal(v, err)
	}
	v, err = s.AttemptMessage(m.ID, v.NextAttempt, 1)
	if err != nil || v.Status != "unknown" {
		t.Fatal("retry exhaustion asserted nonexecution", v, err)
	}
	r := messaging.Receipt{ID: m.ID, Digest: m.Fingerprint(), Status: "applied"}
	reply := messaging.Reply(m, r)
	forged := reply
	forged.From = "wrong"
	if err = s.AcceptReceipt(forged); !errors.Is(err, messaging.ErrConflict) {
		t.Fatal("wrong server receipt accepted", err)
	}
	if err = s.AcceptReceipt(reply); err != nil {
		t.Fatal(err)
	}
	seq := s.seq
	if err = s.AcceptReceipt(reply); err != nil || seq != s.seq {
		t.Fatal("duplicate receipt", err)
	}
	v, _ = s.OutboundMessage(m.ID)
	if v.Status != "applied" {
		t.Fatal(v)
	}
	v.Message.Payload[0] = 'x'
	again, _ := s.OutboundMessage(m.ID)
	if !json.Valid(again.Message.Payload) {
		t.Fatal("reader mutated store")
	}
	m.ID = "unsent"
	v, err = s.QueueMessage(m)
	if err != nil {
		t.Fatal(err)
	}
	v, err = s.AttemptMessage(m.ID, m.Deadline.Add(time.Second), 3)
	if err != nil || v.Status != "expired" {
		t.Fatal(v, err)
	}
}

func TestMessageRejectsExistingServiceEventKeyWithoutPoisoningJournal(t *testing.T) {
	s := messageStore(t)
	m := messageEnvelope(t, s, "collision")
	engine := &Engine{Store: s}
	key := "mqtt-" + messaging.Hash([]byte(m.Key()))
	if _, err := engine.EmitServiceEvent("inventory.ready", key, []byte(`{"existing":true}`)); err != nil {
		t.Fatal(err)
	}
	r, err := s.AcceptMessage(m)
	if err != nil || r.Status != "rejected" {
		t.Fatal(r, err)
	}
	m.ID = "next"
	r, err = s.AcceptMessage(m)
	if err != nil || r.Status != "applied" {
		t.Fatal("collision poisoned writer", r, err)
	}
}
