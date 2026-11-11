package messaging

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

type Store interface {
	QueueMessage(Envelope) (Outbound, error)
	OutboundMessage(string) (Outbound, error)
	PendingMessages(time.Time, int) []Outbound
	ExpireMessages(time.Time, int) error
	AttemptMessage(string, time.Time, int) (Outbound, error)
	AcceptMessage(Envelope) (Receipt, error)
	AcceptReceipt(Envelope) error
}
type Request struct {
	ID          string          `json:"id"`
	To          string          `json:"to"`
	Project     string          `json:"project"`
	Mailbox     string          `json:"mailbox"`
	Kind        string          `json:"kind"`
	Type        string          `json:"type"`
	Correlation string          `json:"correlation,omitempty"`
	Generation  int             `json:"generation,omitempty"`
	Deadline    time.Time       `json:"deadline"`
	Payload     json.RawMessage `json:"payload"`
}
type Status struct {
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Server    string `json:"server"`
	PublicKey string `json:"public_key"`
	LastError string `json:"last_error,omitempty"`
	Rejected  uint64 `json:"rejected_packets"`
}
type Service struct {
	cfg             Config
	id              string
	key             ed25519.PrivateKey
	peers           map[string]Peer
	keys            map[string]ed25519.PublicKey
	store           Store
	cm              *autopaho.ConnectionManager
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	replies         chan Envelope
	mu              sync.Mutex
	status          Status
	connectionEpoch uint64
}

func New(ctx context.Context, cfg Config, id string, key ed25519.PrivateKey, store Store) (*Service, error) {
	u, tlsCfg, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	if !ValidID(id) || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid MQTT home identity")
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{cfg: cfg, id: id, key: append(ed25519.PrivateKey(nil), key...), store: store, peers: map[string]Peer{}, keys: map[string]ed25519.PublicKey{}, ctx: ctx, cancel: cancel, done: make(chan struct{}), replies: make(chan Envelope, 64)}
	s.status = Status{Enabled: true, Server: id, PublicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))}
	for _, p := range cfg.Peers {
		if p.ID == id {
			cancel()
			return nil, errors.New("home identity cannot be overridden by a peer")
		}
		k, _ := base64.StdEncoding.DecodeString(p.PublicKey)
		s.peers[p.ID] = p
		s.keys[p.ID] = ed25519.PublicKey(k)
	}
	s.keys[id] = key.Public().(ed25519.PublicKey)
	password := ""
	if cfg.PasswordEnv != "" {
		var ok bool
		password, ok = os.LookupEnv(cfg.PasswordEnv)
		if !ok || password == "" {
			cancel()
			return nil, errors.New("MQTT password environment variable is unset")
		}
	}
	config := autopaho.ClientConfig{
		ServerUrls: []*url.URL{u}, TlsCfg: tlsCfg, KeepAlive: 20, SessionExpiryInterval: cfg.SessionExpiry,
		ConnectUsername: cfg.Username, ConnectPassword: []byte(password), ConnectTimeout: 5 * time.Second,
		ReconnectBackoff: func(n int) time.Duration { return time.Second * time.Duration(1<<min(n, 5)) },
		OnConnectError:   func(error) { s.setError("broker connection failed") },
		OnConnectionDown: func() bool { s.mu.Lock(); s.connectionEpoch++; s.status.Connected = false; s.mu.Unlock(); return true },
		ConnectPacketBuilder: func(c *paho.Connect, _ *url.URL) (*paho.Connect, error) {
			if c.Properties == nil {
				c.Properties = &paho.ConnectProperties{}
			}
			max := uint32(MaxPacket)
			receive := uint16(16)
			c.Properties.MaximumPacketSize = &max
			c.Properties.ReceiveMaximum = &receive
			return c, nil
		},
		ClientConfig: paho.ClientConfig{ClientID: "mitos-" + id, PacketTimeout: 5 * time.Second, OnPublishReceived: []func(paho.PublishReceived) (bool, error){func(p paho.PublishReceived) (bool, error) { return true, s.receive(p.Packet) }}},
	}
	config.OnConnectionUp = func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
		s.mu.Lock()
		s.connectionEpoch++
		epoch := s.connectionEpoch
		s.status.Connected = false
		s.mu.Unlock()
		go func() {
			subCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			sub, err := cm.Subscribe(subCtx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: "mitos/v1/" + id + "/+/+/+", QoS: 1, RetainHandling: 2, RetainAsPublished: true}}})
			s.mu.Lock()
			defer s.mu.Unlock()
			if epoch != s.connectionEpoch || ctx.Err() != nil {
				return
			}
			if err != nil || len(sub.Reasons) != 1 || sub.Reasons[0] != 1 {
				s.status.LastError = "broker refused required QoS 1 subscription"
				return
			}
			s.status.Connected = true
			s.status.LastError = ""
		}()
	}
	s.cm, err = autopaho.NewConnection(ctx, config)
	if err != nil {
		cancel()
		return nil, err
	}
	go s.run()
	return s, nil
}
func (s *Service) Status() Status         { s.mu.Lock(); defer s.mu.Unlock(); return s.status }
func (s *Service) setError(detail string) { s.mu.Lock(); s.status.LastError = detail; s.mu.Unlock() }
func (s *Service) Close()                 { s.cancel(); <-s.done; <-s.cm.Done() }
func (s *Service) Send(r Request) (Outbound, error) {
	if r.To != s.id {
		if _, ok := s.peers[r.To]; !ok {
			return Outbound{}, errors.New("destination is not a configured peer")
		}
	}
	p, err := Normalize(r.Payload)
	if err != nil {
		return Outbound{}, err
	}
	m := Envelope{Version: 1, ID: r.ID, From: s.id, To: r.To, Project: r.Project, Mailbox: r.Mailbox, Kind: r.Kind, Type: r.Type, Correlation: r.Correlation, Generation: r.Generation, Deadline: r.Deadline.UTC(), Payload: p, Digest: Hash(p)}
	if err = m.Validate(); err != nil {
		return Outbound{}, err
	}
	if m.Kind == "receipt" {
		return Outbound{}, errors.New("receipts are runtime generated")
	}
	m.Sign(s.key)
	return s.store.QueueMessage(m)
}
func (s *Service) allowed(m Envelope) bool {
	if m.From == s.id {
		return true
	}
	// Receipt acceptance is additionally bound to an exact existing outbound message.
	if m.Kind == "receipt" {
		return true
	}
	for _, g := range s.peers[m.From].Grants {
		if g.Project == m.Project && g.Mailbox == m.Mailbox && g.Kind == m.Kind && g.Type == m.Type {
			return true
		}
	}
	return false
}
func (s *Service) receive(p *paho.Publish) error {
	reject := func() error { s.mu.Lock(); s.status.Rejected++; s.mu.Unlock(); return nil }
	if p == nil || p.QoS != 1 || p.Retain || len(p.Payload) > MaxPacket {
		return reject()
	}
	var m Envelope
	if err := Decode(p.Payload, &m); err != nil {
		return reject()
	}
	if err := m.Validate(); err != nil || m.To != s.id || m.Topic() != p.Topic {
		return reject()
	}
	key, ok := s.keys[m.From]
	if !ok || !m.Verify(key) || !s.allowed(m) {
		return reject()
	}
	if m.Kind == "receipt" {
		if err := s.store.AcceptReceipt(m); err != nil {
			s.setError("application receipt could not be committed")
			return err
		}
		return nil
	}
	r, err := s.store.AcceptMessage(m)
	if err != nil {
		s.setError("inbound message could not be committed")
		return err
	}
	reply := Reply(m, r)
	reply.Sign(s.key)
	select {
	case s.replies <- reply:
	default: /* durable receipt is recovered on sender retry */
	}
	return nil
}
func (s *Service) publish(m Envelope) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	props := &paho.PublishProperties{ContentType: "application/json", CorrelationData: []byte(m.ID)}
	if m.Kind != "receipt" {
		remaining := time.Until(m.Deadline)
		if remaining <= 0 {
			return errors.New("message deadline passed")
		}
		expiry := uint32((remaining + time.Second - 1) / time.Second)
		props.MessageExpiry = &expiry
		props.ResponseTopic = fmt.Sprintf("mitos/v1/%s/%s/%s/receipt", m.From, m.Project, m.Mailbox)
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	ack, err := s.cm.Publish(ctx, &paho.Publish{QoS: 1, Topic: m.Topic(), Payload: b, Properties: props})
	if err == nil && ack.ReasonCode >= 0x80 {
		err = errors.New("broker rejected publish")
	}
	return err
}
func (s *Service) run() {
	defer close(s.done)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case reply := <-s.replies:
			if err := s.publish(reply); err != nil {
				s.setError("receipt publication interrupted; source retry will recover it")
			}
		case <-tick.C:
			// Expiration is independent of delivery order, connectivity, and backoff.
			if err := s.store.ExpireMessages(time.Now().UTC(), 16); err != nil {
				s.setError("message expiration could not be committed")
				continue
			}
			if !s.Status().Connected {
				continue
			}
			for _, v := range s.store.PendingMessages(time.Now().UTC(), 16) {
				if s.ctx.Err() != nil {
					return
				}
				// Do not spend retry budget if the connection dropped during this batch.
				if !s.Status().Connected {
					break
				}
				now := time.Now().UTC()
				next, err := s.store.AttemptMessage(v.Message.ID, now, s.cfg.MaxAttempts)
				if err != nil {
					s.setError("outbound attempt could not be committed")
					break
				}
				if next.Status != "pending" {
					continue
				}
				if err = s.publish(next.Message); err != nil {
					s.setError("publication interrupted; awaiting application receipt")
				}
			}
		}
	}
}
