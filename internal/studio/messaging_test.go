package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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

func TestMessageExpirationBypassesBackoffAndDrainsBatches(t *testing.T) {
	s := messageStore(t)
	now := time.Now().UTC()
	deadline := now.Add(time.Minute)
	for i := 0; i < 33; i++ {
		m := messageEnvelope(t, s, fmt.Sprintf("expire_%02d", i))
		m.From = s.Home().ID
		m.To = "remote"
		m.Deadline = deadline
		if _, err := s.QueueMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	// This possible send has a retry scheduled after the deadline.
	attempted, err := s.AttemptMessage("expire_32", deadline.Add(-time.Second), 32)
	if err != nil || !attempted.NextAttempt.After(deadline) {
		t.Fatal(attempted, err)
	}
	for _, remaining := range []int{17, 1, 0} {
		if err := s.ExpireMessages(deadline, 16); err != nil {
			t.Fatal(err)
		}
		pending := 0
		for _, v := range s.messagesOut {
			if v.Status == "pending" {
				pending++
			}
		}
		if pending != remaining {
			t.Fatalf("pending=%d, want %d", pending, remaining)
		}
	}
	for id, v := range s.messagesOut {
		want := "expired"
		if id == "expire_32" {
			want = "unknown"
		}
		if v.Status != want {
			t.Fatalf("%s: status=%s want %s", id, v.Status, want)
		}
	}
	seq := s.seq
	if err := s.ExpireMessages(deadline, 16); err != nil || s.seq != seq {
		t.Fatal("repeated expiration changed journal", err)
	}
	dir := s.Dir()
	s.Close()
	replayed, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	for id, v := range s.messagesOut {
		got, err := replayed.OutboundMessage(id)
		if err != nil || got.Status != v.Status || got.Attempts != v.Attempts {
			t.Fatal("expiration lost on restart", id, got, err)
		}
	}
}

func pendingBacklog(count int, now time.Time) *Store {
	payload := json.RawMessage(`{"body":"` + strings.Repeat("x", 63<<10) + `"}`)
	s := &Store{messagesOut: make(map[string]messaging.Outbound, count)}
	for i := count - 1; i >= 0; i-- {
		id := fmt.Sprintf("queued_%05d", i)
		s.messagesOut[id] = messaging.Outbound{Message: messaging.Envelope{Version: 1, ID: id, Deadline: now.Add(time.Hour), Payload: payload}, Status: "pending", NextAttempt: now.Add(-time.Minute)}
	}
	return s
}

func TestPendingMessagesCopiesOnlySelectedBatch(t *testing.T) {
	now := time.Now().UTC()
	small, large := pendingBacklog(16, now), pendingBacklog(1024, now)
	measure := func(s *Store) float64 {
		return testing.AllocsPerRun(3, func() {
			if got := s.PendingMessages(now, 16); len(got) != 16 {
				t.Fatalf("batch has %d messages", len(got))
			}
		})
	}
	smallAllocs, largeAllocs := measure(small), measure(large)
	t.Logf("16 records: %.0f allocations; 1024 records: %.0f allocations", smallAllocs, largeAllocs)
	// Metadata slice growth may allocate; payload copying must not scale with backlog.
	if largeAllocs > smallAllocs+128 {
		t.Fatalf("copies grow with backlog: %.0f vs %.0f", largeAllocs, smallAllocs)
	}
	batch := large.PendingMessages(now, 16)
	for i, v := range batch {
		if v.Message.ID != fmt.Sprintf("queued_%05d", i) {
			t.Fatal("unstable tie order", v.Message.ID)
		}
	}
	batch[0].Message.Payload[0] = 'x'
	if !json.Valid(large.messagesOut[batch[0].Message.ID].Message.Payload) {
		t.Fatal("selected payload aliases journal state")
	}
	for _, limit := range []int{0, -1} {
		if got := large.PendingMessages(now, limit); len(got) != 0 {
			t.Fatal("nonpositive limit selected messages", limit)
		}
	}
	// Earlier retry times win; expired or settled work is never network-eligible.
	v := large.messagesOut["queued_00020"]
	v.NextAttempt = now.Add(-2 * time.Minute)
	large.messagesOut[v.Message.ID] = v
	v = large.messagesOut["queued_00000"]
	v.Message.Deadline = now
	large.messagesOut[v.Message.ID] = v
	v = large.messagesOut["queued_00001"]
	v.Status = "applied"
	large.messagesOut[v.Message.ID] = v
	v = large.messagesOut["queued_00002"]
	v.NextAttempt = now.Add(time.Minute)
	large.messagesOut[v.Message.ID] = v
	batch = large.PendingMessages(now, 2)
	if batch[0].Message.ID != "queued_00020" || batch[1].Message.ID != "queued_00003" {
		t.Fatal("incorrect eligibility or retry order", batch[0].Message.ID, batch[1].Message.ID)
	}
}

func BenchmarkPendingMessagesBacklog(b *testing.B) {
	now := time.Now().UTC()
	for _, count := range []int{16, 1024} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			s := pendingBacklog(count, now)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.PendingMessages(now, 16)
			}
		})
	}
}
