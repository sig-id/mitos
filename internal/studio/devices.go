package studio

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"mitos/internal/autonomy"
	"mitos/internal/decision"
)

// DeviceError is deliberately safe to expose at the remote boundary.
type DeviceError struct {
	Status int
	Code   string
}

func (e *DeviceError) Error() string            { return e.Code }
func deviceError(status int, code string) error { return &DeviceError{status, code} }

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,96}$`)

func ValidDeviceID(s string) bool { return deviceIDPattern.MatchString(s) }

type DevicePermissions struct {
	Read    bool `json:"read"`
	Resolve bool `json:"resolve"`
	Voice   bool `json:"voice"`
}
type Device struct {
	ID            string            `json:"device_id"`
	Name          string            `json:"name"`
	Operator      string            `json:"operator"`
	Tenant        decision.Tenant   `json:"tenant"`
	Permissions   DevicePermissions `json:"permissions"`
	ExpiresAt     time.Time         `json:"expires_at"`
	Revoked       bool              `json:"revoked"`
	Revision      string            `json:"revision"`
	TokenHash     string            `json:"token_hash,omitempty"`
	STTDisclosure string            `json:"stt_disclosure"`
}
type DevicePairInput struct {
	Name          string            `json:"name"`
	Permissions   DevicePermissions `json:"permissions"`
	ExpiresAt     time.Time         `json:"expires_at"`
	STTDisclosure string            `json:"stt_disclosure"`
}
type DeviceCommand struct {
	Version    int             `json:"version"`
	CommandID  string          `json:"command_id"`
	DecisionID string          `json:"decision_id"`
	RunID      string          `json:"run_id"`
	Revision   string          `json:"revision"`
	Choice     string          `json:"choice"`
	Value      json.RawMessage `json:"value"`
}
type DeviceReceipt struct {
	Version     int    `json:"version"`
	CommandID   string `json:"command_id"`
	DecisionID  string `json:"decision_id"`
	State       string `json:"state"`
	ResultRunID string `json:"result_run_id"`
}
type DeviceCommandRecord struct {
	DeviceID string        `json:"device_id"`
	RunID    string        `json:"run_id"`
	Digest   string        `json:"digest"`
	Receipt  DeviceReceipt `json:"receipt"`
}

func newDeviceToken() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func publicDevice(d Device) Device { d.TokenHash = ""; return d }

func (s *Store) PairDevice(in DevicePairInput, p decision.Principal) (Device, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !p.Authenticated || p.ID == "" || !p.Tenant.Valid() {
		return Device{}, "", deviceError(403, "forbidden")
	}
	if in.Name == "" || len(in.Name) > 160 || !in.ExpiresAt.After(time.Now()) || in.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) || !in.Permissions.Read || in.Permissions.Voice && in.STTDisclosure == "" || len(in.STTDisclosure) > 1024 {
		return Device{}, "", deviceError(400, "malformed")
	}
	token, err := newDeviceToken() // 256 random bits; only its digest enters the event log.
	if err != nil {
		return Device{}, "", err
	}
	d := Device{ID: "device_" + rand.Text(), Name: in.Name, Operator: p.ID, Tenant: p.Tenant, Permissions: in.Permissions, ExpiresAt: in.ExpiresAt.UTC(), Revision: rand.Text(), TokenHash: tokenHash(token), STTDisclosure: in.STTDisclosure}
	if err := s.commit("", "effect", "device_paired", change{Device: &d}); err != nil {
		return Device{}, "", err
	}
	return publicDevice(d), token, nil
}
func (s *Store) Devices(p decision.Principal) []Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Device{}
	for _, d := range s.devices {
		if p.Authenticated && d.Operator == p.ID && d.Tenant == p.Tenant {
			out = append(out, publicDevice(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Store) ChangeDevice(id string, rotate bool, expires time.Time, p decision.Principal) (Device, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok || !p.Authenticated || d.Operator != p.ID || d.Tenant != p.Tenant {
		return Device{}, "", deviceError(404, "unavailable")
	}
	token := ""
	kind := "device_revoked"
	if rotate {
		if d.Revoked || !expires.After(time.Now()) || expires.After(time.Now().Add(366*24*time.Hour)) {
			return Device{}, "", deviceError(400, "malformed")
		}
		replacement, err := newDeviceToken()
		if err != nil {
			return Device{}, "", err
		}
		token = replacement
		d.TokenHash = tokenHash(token)
		d.ExpiresAt = expires.UTC()
		kind = "device_rotated"
	} else {
		d.Revoked = true
	}
	d.Revision = rand.Text()
	if err := s.commit("", "effect", kind, change{Device: &d}); err != nil {
		return Device{}, "", err
	}
	return publicDevice(d), token, nil
}
func (s *Store) authenticateDevice(token, permission string) (Device, error) {
	id := s.deviceTokens[tokenHash(token)]
	d, ok := s.devices[id]
	if !ok || token == "" {
		return Device{}, deviceError(401, "unauthenticated")
	}
	if d.Revoked {
		return Device{}, deviceError(403, "revoked")
	}
	if !time.Now().Before(d.ExpiresAt) {
		return Device{}, deviceError(403, "expired_credential")
	}
	allowed := permission == "read" && d.Permissions.Read || permission == "resolve" && d.Permissions.Resolve || permission == "voice" && d.Permissions.Voice
	if !allowed {
		return Device{}, deviceError(403, "forbidden")
	}
	return d, nil
}
func (s *Store) AuthenticateDevice(token, permission string) (Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, err := s.authenticateDevice(token, permission)
	return publicDevice(d), err
}
func (s *Store) applyDevice(d Device) {
	if s.devices == nil {
		s.devices = map[string]Device{}
		s.deviceTokens = map[string]string{}
	}
	if old, ok := s.devices[d.ID]; ok {
		delete(s.deviceTokens, old.TokenHash)
	}
	s.devices[d.ID] = d
	s.deviceTokens[d.TokenHash] = d.ID
}
func deviceRevision(d Device, r *Run, card *decision.Card) string {
	b, _ := json.Marshal(struct {
		Authorization, Title, Status, State string
		Card                                *decision.Card
	}{d.Revision, r.Brief.Title, r.Status, deviceCardState(r, card), card})
	return tokenHash(string(b))
}
func (s *Store) deviceCard(token, permission, runID, decisionID, revision string) (Device, *Run, error) {
	d, err := s.authenticateDevice(token, permission)
	if err != nil {
		return Device{}, nil, err
	}
	r := s.runs[runID]
	if r == nil || runTenant(r) != d.Tenant || r.Decision == nil || r.Decision.ID != decisionID || r.Decision.Tenant != d.Tenant || r.Sample {
		return d, nil, deviceError(404, "unavailable")
	}
	c := r.Decision
	if c.Source != nil && s.agentSessions[c.Source.SessionID].Operator != d.Operator {
		return d, nil, deviceError(404, "unavailable")
	}
	if c.SupersededAt != nil {
		return d, nil, deviceError(409, "superseded")
	}
	if c.Source != nil && !s.agentSourceActive(c.Source) {
		return d, nil, deviceError(409, "session_ended")
	}
	if c.Due(time.Now()) {
		return d, nil, deviceError(410, "expired")
	}
	if c.Resolution != nil || c.Choice != "" || r.Status != "parked" {
		return d, nil, deviceError(409, "conflict")
	}
	if deviceRevision(d, r, c) != revision {
		return d, nil, deviceError(409, "stale_revision")
	}
	if _, err := projectDeviceCard(d, r, c); err != nil {
		return d, nil, err
	}
	if c.InputProfile() == "unsupported" {
		return d, nil, deviceError(422, "input_unsupported")
	}
	return d, r, nil
}
func (s *Store) DeviceDraftCard(token, runID, decisionID, revision string) (DeviceCard, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, r, err := s.deviceCard(token, "voice", runID, decisionID, revision)
	if err != nil {
		return DeviceCard{}, err
	}
	return projectDeviceCard(d, r, r.Decision)
}

func (s *Store) ValidateDeviceDraftValue(token, runID, decisionID, revision string, value json.RawMessage) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, r, err := s.deviceCard(token, "voice", runID, decisionID, revision)
	if err != nil {
		return err
	}
	if err = r.Decision.ValidateValue(value); err != nil {
		return deviceError(422, "invalid_value")
	}
	return nil
}

// The body digest, original receipt and canonical resolution are a SINGLE log
// record. Replay can never recover a resolution without its command receipt.
func (s *Store) acceptDeviceCommand(token string, cmd DeviceCommand, body []byte, active bool) (DeviceReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.authenticateDevice(token, "resolve")
	if err != nil {
		return DeviceReceipt{}, false, err
	}
	key := d.ID + "/" + cmd.CommandID
	if saved, ok := s.deviceCommands[key]; ok {
		r := s.runs[saved.RunID]
		if r == nil || runTenant(r) != d.Tenant {
			return DeviceReceipt{}, false, deviceError(404, "unavailable")
		}
		if saved.Digest != tokenHash(string(body)) {
			return DeviceReceipt{}, false, deviceError(409, "idempotency_conflict")
		}
		return saved.Receipt, false, nil
	}
	_, r, err := s.deviceCard(token, "resolve", cmd.RunID, cmd.DecisionID, cmd.Revision)
	if err != nil {
		return DeviceReceipt{}, false, err
	}
	if err := r.Decision.ValidateValue(cmd.Value); err != nil {
		return DeviceReceipt{}, false, deviceError(422, "invalid_value")
	}
	if r.Decision.InputProfile() == "text" {
		var text string
		_ = json.Unmarshal(cmd.Value, &text)
		if !DeviceText(text, 1024) {
			return DeviceReceipt{}, false, deviceError(422, "display_unsupported")
		}
	}
	if !r.Decision.HasOption(cmd.Choice) {
		return DeviceReceipt{}, false, deviceError(422, "invalid_choice")
	}
	var rewind *autonomy.Rewind
	if cmd.Choice == "rewind" && r.Decision.Source == nil {
		if r.Claim != nil && r.Claim.ReleasedAt == nil && !active {
			return DeviceReceipt{}, false, deviceError(409, "claims_unreleased")
		}
		rewind = decisionRewind(*r)
	}
	receipt := DeviceReceipt{Version: 1, CommandID: cmd.CommandID, DecisionID: cmd.DecisionID, State: "accepted"}
	record := &DeviceCommandRecord{DeviceID: d.ID, RunID: r.ID, Digest: tokenHash(string(body)), Receipt: receipt}
	resolution := decision.Resolution{DecisionID: cmd.DecisionID, Choice: cmd.Choice, Value: cmd.Value, Via: "esp-notify", DeviceID: d.ID, CommandID: cmd.CommandID}
	err = s.resolveChoice(r.ID, resolution, decision.Principal{ID: d.Operator, Tenant: d.Tenant, Authenticated: true}, rewind, record)
	if errors.Is(err, ErrConflict) {
		err = deviceError(409, "conflict")
	}
	return receipt, err == nil, err
}
func decisionRewind(r Run) *autonomy.Rewind {
	if r.PendingRewind != nil {
		copy := *r.PendingRewind
		return &copy
	}
	if r.ReadyRewind != nil {
		copy := *r.ReadyRewind
		return &copy
	}
	signal := r.Decision.ReasonSignal
	return &autonomy.Rewind{Scope: "step", FromSegment: len(r.Segments), Memo: fmt.Sprintf("The operator requested checkpoint recovery after %s. Correct the recorded failure using the preceding segment's output and predicate evidence, while preserving the original brief.", signal), ReasonSignal: signal}
}
func (e *Engine) ResolveDevice(token string, cmd DeviceCommand, body []byte) (DeviceReceipt, error) {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.mu.Lock()
	_, active := e.cancel[cmd.RunID]
	e.mu.Unlock()
	receipt, fresh, err := e.Store.acceptDeviceCommand(token, cmd, body, active)
	if err != nil || !fresh {
		return receipt, err
	}
	r, err := e.Store.Get(cmd.RunID)
	if err == nil {
		_, err = e.applyResolution(r)
	}
	if err != nil {
		_ = e.Store.failDecisionApplication(cmd.RunID, err.Error(), false)
	}
	// Acceptance is durable even if application fails. Later snapshots expose its
	// progress; replays always return this exact original acceptance receipt.
	return receipt, nil
}
