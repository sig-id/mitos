package messaging

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigurationRejectsUnsafeTransportAndBroadGrants(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	pub := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
	for _, cfg := range []Config{
		{Broker: "mqtt://remote.example:1883"},
		{Broker: "mqtt://localhost:1883"},
		{Broker: "mqtt://user:password@127.0.0.1:1883"},
		{Broker: "https://broker.example"},
		{Broker: "mqtt://127.0.0.1:1883", SessionExpiry: 9999999},
		{Broker: "mqtt://127.0.0.1:1883", MaxAttempts: -1},
		{Broker: "mqtt://127.0.0.1:1883", Peers: []Peer{{ID: "peer", PublicKey: pub, Grants: []Grant{{Project: "#", Mailbox: "box", Kind: "note", Type: "text"}}}}},
	} {
		if _, _, err := cfg.validate(); err == nil {
			t.Fatalf("accepted unsafe configuration: %+v", cfg)
		}
	}
	cfg := Config{Broker: "mqtts://broker.example:8883"}
	u, tc, err := cfg.validate()
	if err != nil || u.Scheme != "tls" || tc.InsecureSkipVerify || cfg.SessionExpiry == 0 {
		t.Fatal("TLS defaults", u, tc, err)
	}
}
func TestSigningAndIdentitySurviveRestart(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	same, err := LoadIdentity(dir)
	if err != nil || string(key) != string(same) {
		t.Fatal("identity changed", err)
	}
	p, _ := Normalize([]byte(` { "body": "hello" } `))
	m := Envelope{Version: 1, ID: "one", From: "sender", To: "receiver", Project: "studio", Mailbox: "operator", Kind: "note", Type: "text", Deadline: time.Now().Add(time.Hour), Payload: p, Digest: Hash(p)}
	m.Sign(key)
	if err = m.Validate(); err != nil || !m.Verify(key.Public().(ed25519.PublicKey)) {
		t.Fatal("invalid signed envelope", err)
	}
	m.To = "other"
	if m.Verify(key.Public().(ed25519.PublicKey)) {
		t.Fatal("signature did not bind routing")
	}
	if err = os.Chmod(filepath.Join(dir, "mqtt-ed25519.key"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadIdentity(dir); err == nil {
		t.Fatal("accepted readable private key")
	}
}
