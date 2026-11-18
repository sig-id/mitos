// Package workerwake carries lossy pool-only wake hints. HTTP claims remain the
// only authority for task assignment, including after a duplicated or missed hint.
package workerwake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"mitos/internal/messaging"
	"mitos/internal/structuredworker"
)

type Hint struct {
	Pool       string `json:"pool"`
	Capability string `json:"capability,omitempty"`
}
type Client struct {
	cm        *autopaho.ConnectionManager
	cancel    context.CancelFunc
	authority string
	Wake      chan struct{}
	ready     chan struct{}
	once      sync.Once
}

func New(ctx context.Context, cfg messaging.Config, authority, pool string) (*Client, error) {
	if !messaging.ValidID(authority) || (pool != "" && !messaging.ValidID(pool)) {
		return nil, errors.New("invalid wake authority or pool")
	}
	broker, tlsCfg, err := cfg.Transport()
	if err != nil {
		return nil, err
	}
	password := ""
	if cfg.PasswordEnv != "" {
		password = os.Getenv(cfg.PasswordEnv)
		if password == "" {
			return nil, errors.New("MQTT password environment variable is unset")
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	client := &Client{cancel: cancel, authority: authority, Wake: make(chan struct{}, 1), ready: make(chan struct{})}
	config := autopaho.ClientConfig{ServerUrls: []*url.URL{broker}, TlsCfg: tlsCfg, KeepAlive: 20, ConnectTimeout: 3 * time.Second, ConnectUsername: cfg.Username, ConnectPassword: []byte(password), ReconnectBackoff: func(n int) time.Duration { return time.Duration(1<<min(n, 4)) * time.Second }, ClientConfig: paho.ClientConfig{ClientID: "mitos-wake-" + structuredworker.RequestID(), PacketTimeout: 3 * time.Second}}
	config.OnConnectionUp = func(*autopaho.ConnectionManager, *paho.Connack) { client.once.Do(func() { close(client.ready) }) }
	if pool != "" {
		topic := "$share/mitos-workers/" + client.topic(pool)
		config.OnConnectionUp = func(cm *autopaho.ConnectionManager, _ *paho.Connack) {
			go func() {
				subCtx, stop := context.WithTimeout(ctx, 3*time.Second)
				defer stop()
				ack, err := cm.Subscribe(subCtx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: topic, QoS: 1, RetainHandling: 2}}})
				if err == nil && ack != nil && len(ack.Reasons) == 1 && ack.Reasons[0] == 1 {
					client.once.Do(func() { close(client.ready) })
				}
			}()
		}
		config.OnPublishReceived = []func(paho.PublishReceived) (bool, error){func(received paho.PublishReceived) (bool, error) {
			packet := received.Packet
			if packet == nil || packet.Retain || len(packet.Payload) > 512 {
				return true, nil
			}
			var hint Hint
			d := json.NewDecoder(bytes.NewReader(packet.Payload))
			d.DisallowUnknownFields()
			if d.Decode(&hint) != nil || d.Decode(new(any)) != io.EOF || hint.Pool != pool || packet.Topic != client.topic(pool) {
				return true, nil
			}
			select {
			case client.Wake <- struct{}{}:
			default:
			}
			return true, nil
		}}
	}
	client.cm, err = autopaho.NewConnection(ctx, config)
	if err != nil {
		cancel()
		return nil, err
	}
	return client, nil
}
func (c *Client) topic(pool string) string { return "mitos/worker/v1/" + c.authority + "/pool/" + pool }
func (c *Client) Publish(ctx context.Context, hint Hint) error {
	if !messaging.ValidID(hint.Pool) || hint.Capability != "" && !messaging.ValidID(strings.ReplaceAll(hint.Capability, ".", "_")) {
		return errors.New("invalid wake class")
	}
	raw, _ := json.Marshal(hint)
	ack, err := c.cm.Publish(ctx, &paho.Publish{QoS: 1, Topic: c.topic(hint.Pool), Payload: raw, Retain: false})
	if err != nil {
		return err
	}
	if ack == nil || ack.ReasonCode >= 128 {
		return errors.New("wake publish refused")
	}
	return nil
}
func (c *Client) Close() {
	c.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.cm.Disconnect(ctx)
}

func (c *Client) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ready:
		return nil
	}
}
