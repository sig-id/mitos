// Package messaging implements Mitos application envelopes over MQTT 5. The
// runtime journal, not the broker session, owns admission and application state.
package messaging

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
	"unicode/utf8"
)

const MaxPayload = 64 << 10
const MaxPacket = 96 << 10
const MaxRecords = 10000

var ErrConflict = errors.New("message identity conflicts with existing content")
var ErrCapacity = errors.New("message capacity reached; existing receipts retained")
var segment = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

func ValidID(s string) bool { return segment.MatchString(s) }

type Envelope struct {
	Version     int             `json:"version"`
	ID          string          `json:"id"`
	From        string          `json:"from"`
	To          string          `json:"to"`
	Project     string          `json:"project"`
	Mailbox     string          `json:"mailbox"`
	Kind        string          `json:"kind"`
	Type        string          `json:"type"`
	Correlation string          `json:"correlation,omitempty"`
	Generation  int             `json:"generation,omitempty"`
	Deadline    time.Time       `json:"deadline"`
	Digest      string          `json:"payload_sha256"`
	Payload     json.RawMessage `json:"payload"`
	Signature   string          `json:"signature"`
}

type Receipt struct {
	ID     string `json:"id"`
	Digest string `json:"message_sha256"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type Outbound struct {
	Message     Envelope  `json:"message"`
	Status      string    `json:"status"`
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt"`
	Receipt     *Receipt  `json:"receipt,omitempty"`
}

type Inbound struct {
	Message       Envelope `json:"message"`
	Receipt       Receipt  `json:"receipt"`
	Sequence      int64    `json:"sequence"`
	Acknowledged  bool     `json:"acknowledged"`
	AckGeneration int      `json:"ack_generation,omitempty"`
}

func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

func Normalize(b []byte) ([]byte, error) {
	if len(b) == 0 || len(b) > MaxPayload || !utf8.Valid(b) {
		return nil, errors.New("payload must contain 1–65536 bytes")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, errors.New("expected one payload")
	}
	return json.Marshal(v)
}
func Hash(b []byte) string             { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (m Envelope) unsigned() []byte    { m.Signature = ""; b, _ := json.Marshal(m); return b }
func (m Envelope) Fingerprint() string { return Hash(m.unsigned()) }
func (m Envelope) Key() string         { return m.From + "/" + m.ID }
func (m Envelope) Topic() string {
	return fmt.Sprintf("mitos/v1/%s/%s/%s/%s", m.To, m.Project, m.Mailbox, m.Kind)
}
func (m *Envelope) Sign(key ed25519.PrivateKey) {
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, m.unsigned()))
}
func (m Envelope) Verify(key ed25519.PublicKey) bool {
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	return err == nil && ed25519.Verify(key, m.unsigned(), sig)
}
func (m Envelope) Validate() error {
	if m.Version != 1 || !ValidID(m.ID) || !ValidID(m.From) || !ValidID(m.To) || !ValidID(m.Project) || !ValidID(m.Mailbox) || m.Generation < 0 || len(m.Correlation) > 100 || m.Deadline.IsZero() {
		return errors.New("invalid message envelope")
	}
	if m.Kind != "note" && m.Kind != "command" && m.Kind != "event" && m.Kind != "receipt" {
		return errors.New("invalid message kind")
	}
	if len(m.Type) == 0 || len(m.Type) > 100 {
		return errors.New("invalid message type")
	}
	p, err := Normalize(m.Payload)
	if err != nil || !bytes.Equal(p, m.Payload) || m.Digest != Hash(p) {
		return errors.New("invalid or noncanonical message payload")
	}
	return nil
}
func Reply(m Envelope, r Receipt) Envelope {
	b, _ := json.Marshal(r)
	b, _ = Normalize(b)
	return Envelope{Version: 1, ID: "receipt_" + Hash([]byte(m.Key()))[:32], From: m.To, To: m.From, Project: m.Project, Mailbox: m.Mailbox, Kind: "receipt", Type: "application", Correlation: m.ID, Deadline: m.Deadline, Digest: Hash(b), Payload: b}
}
