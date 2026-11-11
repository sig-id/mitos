package studio

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/mochi-mqtt/server/v2/packets"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"mitos/internal/messaging"
	"mitos/internal/service"
)

func mqttBroker(t *testing.T, address string) (*mqtt.Server, string) {
	t.Helper()
	b := mqtt.New(&mqtt.Options{InlineClient: true, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := b.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	l := listeners.NewTCP(listeners.Config{ID: "test", Address: address})
	if err := b.AddListener(l); err != nil {
		t.Fatal(err)
	}
	if err := b.Serve(); err != nil {
		t.Fatal(err)
	}
	return b, l.Address()
}
func mqttWait(t *testing.T, why string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out:", why)
}
func mqttKeys(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key, base64.StdEncoding.EncodeToString(pub)
}
func mqttStart(t *testing.T, store messaging.Store, id string, key ed25519.PrivateKey, c messaging.Config) *messaging.Service {
	t.Helper()
	s, err := messaging.New(context.Background(), c, id, key, store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	mqttWait(t, "MQTT subscribe", func() bool { return s.Status().Connected })
	return s
}

type lostReceiptStore struct {
	*Store
	discard atomic.Int32
}

func (s *lostReceiptStore) AcceptReceipt(m messaging.Envelope) error {
	if s.discard.Add(-1) >= 0 {
		return errors.New("simulated lost application receipt after transport acknowledgment")
	}
	return s.Store.AcceptReceipt(m)
}

func TestMQTTEndToEndServiceTaskAndLostReceipt(t *testing.T) {
	broker, address := mqttBroker(t, "127.0.0.1:0")
	defer broker.Close()
	a, b := messageStore(t), messageStore(t)
	apps, err := LoadApps("")
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(b, apps, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	catalog := ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "consumer", Scope: "project", Trigger: "event", Schedule: &service.Schedule{Type: "event", Trigger: service.EventPatterns{"inventory.*"}}}}, Intent: service.Intent{Enable: []string{"consumer"}}}, Work: map[string]Brief{"consumer": {Title: "Inspect triggered work", Workspace: engine.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	// The built-in inventory application uses no external model or credentials.
	if _, err = engine.ConfigureServices(catalog, 0); err != nil {
		t.Fatal(err)
	}
	ak, ap := mqttKeys(t)
	bk, bp := mqttKeys(t)
	lost := &lostReceiptStore{Store: a}
	lost.discard.Store(1)
	sa := mqttStart(t, lost, a.Home().ID, ak, messaging.Config{Broker: "mqtt://" + address, Peers: []messaging.Peer{{ID: b.Home().ID, PublicKey: bp}}})
	sb := mqttStart(t, b, b.Home().ID, bk, messaging.Config{Broker: "mqtt://" + address, Peers: []messaging.Peer{{ID: a.Home().ID, PublicKey: ap, Grants: []messaging.Grant{{Project: "studio", Mailbox: "events", Kind: "command", Type: "service.event"}}}}})
	_ = sb
	req := messaging.Request{ID: "create-work", To: b.Home().ID, Project: "studio", Mailbox: "events", Kind: "command", Type: "service.event", Deadline: time.Now().Add(time.Minute), Payload: json.RawMessage(`{"name":"inventory.ready","payload":{"revision":1}}`)}
	if _, err = sa.Send(req); err != nil {
		t.Fatal(err)
	}
	mqttWait(t, "application receipt recovery", func() bool { v, _ := a.OutboundMessage(req.ID); return v.Status == "applied" })
	v, _ := a.OutboundMessage(req.ID)
	if v.Attempts < 2 {
		t.Fatal("lost receipt did not cause application retry", v)
	}
	mqttWait(t, "one admitted service task", func() bool {
		for _, task := range b.Tasks() {
			if task.Source == "trigger:consumer" {
				return task.Status == "done"
			}
		}
		return false
	})
	if len(b.CustomEvents()) != 1 {
		t.Fatal("duplicate custom event")
	}
	n := 0
	for _, task := range b.Tasks() {
		if task.Source == "trigger:consumer" {
			n++
		}
	}
	if n != 1 {
		t.Fatal("duplicated service task", n)
	}
	if _, err = sa.Send(req); err != nil {
		t.Fatal(err)
	}
}

func TestMQTTBrokerStateLossAndServerRestart(t *testing.T) {
	broker, address := mqttBroker(t, "127.0.0.1:0")
	a, b := messageStore(t), messageStore(t)
	ak, ap := mqttKeys(t)
	bk, bp := mqttKeys(t)
	ac := messaging.Config{Broker: "mqtt://" + address, SessionExpiry: 1, Peers: []messaging.Peer{{ID: b.Home().ID, PublicKey: bp}}}
	bc := messaging.Config{Broker: "mqtt://" + address, SessionExpiry: 1, Peers: []messaging.Peer{{ID: a.Home().ID, PublicKey: ap, Grants: []messaging.Grant{{Project: "studio", Mailbox: "operator", Kind: "note", Type: "text"}}}}}
	sa := mqttStart(t, a, a.Home().ID, ak, ac)
	sb := mqttStart(t, b, b.Home().ID, bk, bc)
	sb.Close()
	broker.Close()
	req := messaging.Request{ID: "offline", To: b.Home().ID, Project: "studio", Mailbox: "operator", Kind: "note", Type: "text", Deadline: time.Now().Add(time.Minute), Payload: json.RawMessage(`{"body":"survive session loss"}`)}
	if _, err := sa.Send(req); err != nil {
		t.Fatal(err)
	}
	sa.Close()
	dir := a.Dir()
	a.Close()
	var err error
	a, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// A new broker on the same endpoint has no sessions or queued messages.
	broker, _ = mqttBroker(t, address)
	defer broker.Close()
	sa = mqttStart(t, a, a.Home().ID, ak, ac)
	_ = sa
	sb = mqttStart(t, b, b.Home().ID, bk, bc)
	_ = sb
	mqttWait(t, "journal replay after broker state loss", func() bool { v, _ := a.OutboundMessage(req.ID); return v.Status == "accepted" })
	if len(b.MessageInbox("studio", "operator", 0)) != 1 {
		t.Fatal("lost or duplicated note")
	}
}

func TestMQTTRejectsForgeryWrongTopicsRetainedAndRevokedPeers(t *testing.T) {
	broker, address := mqttBroker(t, "127.0.0.1:0")
	defer broker.Close()
	a, b := messageStore(t), messageStore(t)
	ak, ap := mqttKeys(t)
	bk, _ := mqttKeys(t)
	bc := messaging.Config{Broker: "mqtt://" + address, Peers: []messaging.Peer{{ID: a.Home().ID, PublicKey: ap, Grants: []messaging.Grant{{Project: "studio", Mailbox: "operator", Kind: "note", Type: "text"}}}}}
	sb := mqttStart(t, b, b.Home().ID, bk, bc)
	m := messageEnvelope(t, b, "safe")
	m.From = a.Home().ID
	m.Kind = "note"
	m.Type = "text"
	m.Mailbox = "operator"
	m.Payload = []byte(`{"body":"hello"}`)
	m.Digest = messaging.Hash(m.Payload)
	m.Sign(ak)
	cases := []struct {
		m      messaging.Envelope
		topic  string
		retain bool
	}{}
	forged := m
	forged.ID = "forged"
	cases = append(cases, struct {
		m      messaging.Envelope
		topic  string
		retain bool
	}{forged, forged.Topic(), false})
	wrong := m
	wrong.ID = "wrong"
	wrong.Sign(ak)
	cases = append(cases, struct {
		m      messaging.Envelope
		topic  string
		retain bool
	}{wrong, "mitos/v1/" + b.Home().ID + "/studio/other/note", false})
	retained := m
	retained.ID = "retained"
	retained.Sign(ak)
	cases = append(cases, struct {
		m      messaging.Envelope
		topic  string
		retain bool
	}{retained, retained.Topic(), true})
	denied := m
	denied.ID = "denied"
	denied.Mailbox = "events"
	denied.Sign(ak)
	cases = append(cases, struct {
		m      messaging.Envelope
		topic  string
		retain bool
	}{denied, denied.Topic(), false})
	for _, tc := range cases {
		raw, _ := json.Marshal(tc.m)
		if err := broker.Publish(tc.topic, raw, tc.retain, 1); err != nil {
			t.Fatal(err)
		}
	}
	mqttWait(t, "rejected malicious packets", func() bool { return sb.Status().Rejected >= uint64(len(cases)) })
	if len(b.MessageInbox("studio", "operator", 0)) != 0 || len(b.CustomEvents()) != 0 {
		t.Fatal("unauthorized effect")
	}
	raw, _ := json.Marshal(m)
	if err := broker.Publish(m.Topic(), raw, false, 1); err != nil {
		t.Fatal(err)
	}
	mqttWait(t, "valid signed note", func() bool { return len(b.MessageInbox("studio", "operator", 0)) == 1 })
	sb.Close()
	bc.Peers = nil
	sb = mqttStart(t, b, b.Home().ID, bk, bc)
	m.ID = "revoked"
	m.Sign(ak)
	raw, _ = json.Marshal(m)
	if err := broker.Publish(m.Topic(), raw, false, 1); err != nil {
		t.Fatal(err)
	}
	mqttWait(t, "revoked peer rejected", func() bool { return sb.Status().Rejected > 0 })
	if len(b.MessageInbox("studio", "operator", 0)) != 1 {
		t.Fatal("revoked peer admitted")
	}
}

func TestMQTTTLSBrokerAndUntrustedCertificate(t *testing.T) {
	https := httptest.NewTLSServer(nil)
	certificate := https.TLS.Certificates[0]
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: https.Certificate().Raw})
	https.Close()
	caFile := filepath.Join(t.TempDir(), "broker-ca.pem")
	if err := os.WriteFile(caFile, ca, 0600); err != nil {
		t.Fatal(err)
	}
	broker := mqtt.New(&mqtt.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err := broker.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatal(err)
	}
	listener := listeners.NewTCP(listeners.Config{ID: "tls", Address: "127.0.0.1:0", TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}})
	if err := broker.AddListener(listener); err != nil {
		t.Fatal(err)
	}
	if err := broker.Serve(); err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	store := messageStore(t)
	key, _ := mqttKeys(t)
	cfg := messaging.Config{Broker: "mqtts://" + listener.Address(), CAFile: caFile}
	client := mqttStart(t, store, store.Home().ID, key, cfg)
	request := messaging.Request{ID: "tls", To: store.Home().ID, Project: "studio", Mailbox: "operator", Kind: "note", Type: "text", Deadline: time.Now().Add(time.Minute), Payload: json.RawMessage(`{"body":"TLS note"}`)}
	if _, err := client.Send(request); err != nil {
		t.Fatal(err)
	}
	mqttWait(t, "TLS message receipt", func() bool { v, _ := store.OutboundMessage("tls"); return v.Status == "accepted" })
	client.Close()
	cfg.CAFile = ""
	untrusted, err := messaging.New(context.Background(), cfg, store.Home().ID, key, store)
	if err != nil {
		t.Fatal(err)
	}
	defer untrusted.Close()
	mqttWait(t, "certificate rejection", func() bool { return untrusted.Status().LastError != "" })
	if untrusted.Status().Connected {
		t.Fatal("connected without trusting broker certificate")
	}
}

type messageACL struct {
	mqtt.HookBase
	denyCommands atomic.Bool
}

func (h *messageACL) ID() string { return "message-acl" }
func (h *messageACL) Provides(b byte) bool {
	return b == mqtt.OnConnectAuthenticate || b == mqtt.OnACLCheck
}
func (h *messageACL) OnConnectAuthenticate(_ *mqtt.Client, _ packets.Packet) bool { return true }
func (h *messageACL) OnACLCheck(cl *mqtt.Client, topic string, write bool) bool {
	if !write {
		return strings.HasPrefix(topic, "mitos/v1/"+strings.TrimPrefix(cl.ID, "mitos-")+"/")
	}
	return !(h.denyCommands.Load() && strings.HasSuffix(topic, "/command"))
}
func TestMQTTBrokerPublishDenialRetainsPendingWork(t *testing.T) {
	broker := mqtt.New(&mqtt.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	acl := &messageACL{}
	acl.denyCommands.Store(true)
	if err := broker.AddHook(acl, nil); err != nil {
		t.Fatal(err)
	}
	listener := listeners.NewTCP(listeners.Config{ID: "acl", Address: "127.0.0.1:0"})
	if err := broker.AddListener(listener); err != nil {
		t.Fatal(err)
	}
	if err := broker.Serve(); err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	a, b := messageStore(t), messageStore(t)
	ak, ap := mqttKeys(t)
	bk, bp := mqttKeys(t)
	sa := mqttStart(t, a, a.Home().ID, ak, messaging.Config{Broker: "mqtt://" + listener.Address(), Peers: []messaging.Peer{{ID: b.Home().ID, PublicKey: bp}}})
	mqttStart(t, b, b.Home().ID, bk, messaging.Config{Broker: "mqtt://" + listener.Address(), Peers: []messaging.Peer{{ID: a.Home().ID, PublicKey: ap, Grants: []messaging.Grant{{Project: "studio", Mailbox: "events", Kind: "command", Type: "service.event"}}}}})
	req := messaging.Request{ID: "denied-publish", To: b.Home().ID, Project: "studio", Mailbox: "events", Kind: "command", Type: "service.event", Deadline: time.Now().Add(time.Minute), Payload: json.RawMessage(`{"name":"inventory.ready","payload":{}}`)}
	if _, err := sa.Send(req); err != nil {
		t.Fatal(err)
	}
	mqttWait(t, "broker rejected command publish", func() bool { return sa.Status().LastError != "" })
	v, _ := a.OutboundMessage(req.ID)
	if v.Status != "pending" || len(b.CustomEvents()) != 0 {
		t.Fatal("denial falsely completed work", v)
	}
	acl.denyCommands.Store(false)
	mqttWait(t, "pending work after ACL restoration", func() bool { v, _ := a.OutboundMessage(req.ID); return v.Status == "applied" })
}

type pendingPollStore struct {
	*Store
	polls atomic.Int64
}

func (s *pendingPollStore) PendingMessages(now time.Time, limit int) []messaging.Outbound {
	s.polls.Add(1)
	return s.Store.PendingMessages(now, limit)
}
func TestMQTTOfflineBacklogDoesNotStarveExpiration(t *testing.T) {
	// A bound listener without an MQTT server keeps the connection unavailable.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	store := messageStore(t)
	counted := &pendingPollStore{Store: store}
	key, _ := mqttKeys(t)
	client, err := messaging.New(context.Background(), messaging.Config{Broker: "mqtt://" + listener.Addr().String()}, store.Home().ID, key, counted)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	req := messaging.Request{To: store.Home().ID, Project: "studio", Mailbox: "operator", Kind: "note", Type: "text", Deadline: time.Now().Add(time.Hour), Payload: json.RawMessage(`{"body":"offline"}`)}
	for i := 0; i < 16; i++ {
		req.ID = fmt.Sprintf("long_%02d", i)
		if _, err = client.Send(req); err != nil {
			t.Fatal(err)
		}
	}
	req.ID = "short"
	req.Deadline = time.Now().Add(time.Second)
	if _, err = client.Send(req); err != nil {
		t.Fatal(err)
	}
	mqttWait(t, "expiration behind offline batch", func() bool { v, _ := store.OutboundMessage("short"); return v.Status == "expired" })
	if counted.polls.Load() != 0 {
		t.Fatal("offline loop copied network batches", counted.polls.Load())
	}
	for i := 0; i < 16; i++ {
		v, err := store.OutboundMessage(fmt.Sprintf("long_%02d", i))
		if err != nil || v.Status != "pending" || v.Attempts != 0 {
			t.Fatal("offline message spent retry budget", v, err)
		}
	}
}
