// Package device is the remote connector. It never asserts LocalOperator and
// never mounts the console, run creation, artifacts, or administrative routes.
package device

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"mitos/internal/studio"
)

type Server struct {
	engine       *studio.Engine
	stt          Transcriber
	sttSlots     chan struct{}
	voiceTimeout time.Duration
	mu           sync.Mutex
	limits       map[string]*limit
	views        map[string]*view
	cursorKey    []byte
}
type limit struct {
	tokens float64
	last   time.Time
	active int
}
type view struct {
	deviceID, revision string
	digest             string
	expires            time.Time
	snapshot           studio.DeviceSnapshot
	bytes              int
}

func New(engine *studio.Engine, stt Transcriber) *Server {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return &Server{engine: engine, stt: stt, sttSlots: make(chan struct{}, 2), voiceTimeout: 18 * time.Second, limits: map[string]*limit{}, views: map[string]*view{}, cursorKey: key}
}
func reply(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	var e *studio.DeviceError
	if errors.As(err, &e) {
		reply(w, e.Status, map[string]string{"error": e.Code})
		return
	}
	reply(w, 503, map[string]string{"error": "backend_unavailable"})
}
func problem(status int, code string) error { return &studio.DeviceError{Status: status, Code: code} }
func (s *Server) enter(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, l := range s.limits {
		if l.active == 0 && now.Sub(l.last) > time.Minute {
			delete(s.limits, key)
		}
	}
	l := s.limits[id]
	if l == nil {
		if len(s.limits) >= 1024 {
			return false
		}
		l = &limit{tokens: 30, last: now}
		s.limits[id] = l
	}
	l.tokens = min(30, l.tokens+now.Sub(l.last).Seconds()*2)
	l.last = now
	if l.tokens < 1 || l.active >= 2 {
		return false
	}
	l.tokens--
	l.active++
	return true
}
func (s *Server) leave(id string) { s.mu.Lock(); defer s.mu.Unlock(); s.limits[id].active-- }
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.TLS == nil {
		fail(w, problem(403, "tls_required"))
		return
	}
	permission := "read"
	switch {
	case r.Method == "GET" && r.URL.Path == "/api/device/v1/snapshot":
	case r.Method == "POST" && r.URL.Path == "/api/device/v1/resolutions":
		permission = "resolve"
	case r.Method == "POST" && r.URL.Path == "/api/device/v1/voice-drafts":
		permission = "voice"
	default:
		fail(w, problem(404, "unavailable"))
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || len(auth[0]) > 128 {
		fail(w, problem(401, "unauthenticated"))
		return
	}
	token := strings.TrimPrefix(auth[0], "Bearer ")
	d, err := s.engine.Store.AuthenticateDevice(token, permission)
	if err != nil {
		fail(w, err)
		return
	}
	if !s.enter(d.ID) {
		w.Header().Set("Retry-After", "1")
		fail(w, problem(429, "rate_limited"))
		return
	}
	defer s.leave(d.ID)
	switch permission {
	case "read":
		s.snapshot(w, r, token, d)
	case "resolve":
		if r.URL.RawQuery != "" {
			fail(w, problem(400, "malformed"))
			return
		}
		body, err := readBody(w, r, "application/json", 4095)
		if err != nil {
			fail(w, err)
			return
		}
		var cmd studio.DeviceCommand
		if err = DecodeObject(body, &cmd, []string{"version", "command_id", "decision_id", "run_id", "revision", "choice", "value"}); err != nil || cmd.Version != 1 || !studio.ValidDeviceID(cmd.CommandID) || !studio.ValidDeviceID(cmd.DecisionID) || !studio.ValidDeviceID(cmd.RunID) || !studio.ValidDeviceID(cmd.Revision) || !studio.ValidDeviceID(cmd.Choice) {
			fail(w, problem(400, "malformed"))
			return
		}
		receipt, err := s.engine.ResolveDevice(token, cmd, body)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 202, receipt)
	case "voice":
		s.voice(w, r, token)
	}
}
func readBody(w http.ResponseWriter, r *http.Request, expected string, maxBytes int64) ([]byte, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != expected {
		return nil, problem(400, "malformed")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	data, err := io.ReadAll(r.Body)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		clear(data)
		return nil, problem(413, "body_limit")
	}
	if err != nil {
		clear(data)
		var network net.Error
		if errors.As(err, &network) && network.Timeout() {
			return nil, problem(503, "backend_unavailable")
		}
		return nil, problem(400, "malformed")
	}
	return data, nil
}

// DecodeObject rejects duplicate, unknown and missing fields, null objects and
// trailing JSON. Required names are exact (encoding/json alone folds case).
func DecodeObject(body []byte, dst any, required []string) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return problem(400, "malformed")
	}
	fields := map[string]bool{}
	allowed := map[string]bool{}
	for _, k := range required {
		allowed[k] = true
	}
	for dec.More() {
		tok, err = dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok || fields[key] || !allowed[key] {
			return problem(400, "malformed")
		}
		fields[key] = true
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil {
			return err
		}
	}
	if _, err = dec.Token(); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF || len(fields) != len(required) {
		return problem(400, "malformed")
	}
	dec = json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
func (s *Server) cursor(id string, cards, runs int) string {
	payload := fmt.Sprintf("%s.%d.%d", id, cards, runs)
	mac := hmac.New(sha256.New, s.cursorKey)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (s *Server) parseCursor(cursor string) (string, int, int, error) {
	parts := strings.Split(cursor, ".")
	if len(parts) != 4 || len(cursor) > 256 {
		return "", 0, 0, problem(400, "malformed")
	}
	c, e1 := strconv.Atoi(parts[1])
	r, e2 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || c < 0 || r < 0 {
		return "", 0, 0, problem(400, "malformed")
	}
	if !hmac.Equal([]byte(cursor), []byte(s.cursor(parts[0], c, r))) {
		return "", 0, 0, problem(410, "cursor_expired")
	}
	return parts[0], c, r, nil
}
func (s *Server) snapshot(w http.ResponseWriter, r *http.Request, token string, d studio.Device) {
	query, err := strictQuery(r, []string{"cursor"}, false)
	if err != nil {
		fail(w, err)
		return
	}
	cursor := query["cursor"]
	id := ""
	ci, ri := 0, 0
	var fresh *view
	if cursor == "" {
		snapshot, err := s.engine.Store.DeviceSnapshot(token)
		if err != nil {
			fail(w, err)
			return
		}
		encoded, _ := json.Marshal(snapshot)
		if len(encoded) > 16<<20 {
			fail(w, problem(503, "snapshot_capacity"))
			return
		}
		id = rand.Text()
		digest := sha256.Sum256(encoded)
		fresh = &view{digest: fmt.Sprintf("%x", digest), deviceID: d.ID, revision: d.Revision, expires: time.Now().Add(5 * time.Minute), snapshot: snapshot, bytes: len(encoded)}
	} else {
		id, ci, ri, err = s.parseCursor(cursor)
		if err != nil {
			fail(w, err)
			return
		}
	}
	encoded, err := s.snapshotPage(id, ci, ri, fresh, d)
	if err != nil {
		fail(w, err)
		return
	}
	// Revocation or rotation during projection must not publish the old view.
	current, err := s.engine.Store.AuthenticateDevice(token, "read")
	if err != nil {
		fail(w, err)
		return
	}
	if current.Revision != d.Revision {
		fail(w, problem(403, "forbidden"))
		return
	}
	w.WriteHeader(200)
	_, _ = w.Write(append(encoded, '\n'))
}
func (s *Server) snapshotPage(id string, ci, ri int, fresh *view, d studio.Device) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count, total := 0, 0
	for key, v := range s.views {
		if time.Now().After(v.expires) {
			delete(s.views, key)
			continue
		}
		total += v.bytes
		if v.deviceID == d.ID {
			count++
		}
	}
	// Reuse an unchanged view when the device polls from the first page.
	// Otherwise ordinary three-second polling would exhaust the cursor quota.
	if fresh != nil {
		for key, old := range s.views {
			if old.deviceID == d.ID && old.revision == d.Revision && old.digest == fresh.digest {
				id = key
				fresh = nil
				break
			}
		}
	}
	v := s.views[id]
	if fresh != nil {
		v = fresh
	} else if v == nil || v.deviceID != d.ID || v.revision != d.Revision {
		return nil, problem(410, "cursor_expired")
	}
	snap := v.snapshot
	if ci > len(snap.Cards) || ri > len(snap.Runs) {
		return nil, problem(400, "malformed")
	}
	ce, re := min(ci+8, len(snap.Cards)), min(ri+8, len(snap.Runs))
	var encoded []byte
	for {
		next := ""
		if ce < len(snap.Cards) || re < len(snap.Runs) {
			next = s.cursor(id, ce, re)
		}
		page := studio.DeviceSnapshot{Version: 1, PendingTotal: snap.PendingTotal, NextCursor: next, Cards: snap.Cards[ci:ce], Runs: snap.Runs[ri:re]}
		encoded, _ = json.Marshal(page)
		if len(encoded)+1 <= 49152 {
			break
		}
		if ce-ci > 1 {
			ce--
		} else if re > ri {
			re--
		} else {
			return nil, problem(422, "display_unsupported")
		}
	}
	if fresh != nil && (ce < len(snap.Cards) || re < len(snap.Runs)) {
		if count >= 4 || len(s.views) >= 64 || total+fresh.bytes > 32<<20 {
			return nil, problem(429, "snapshot_capacity")
		}
		s.views[id] = fresh
	}
	return encoded, nil
}
func strictQuery(r *http.Request, names []string, required bool) (map[string]string, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, problem(400, "malformed")
	}
	out := map[string]string{}
	allowed := map[string]bool{}
	for _, k := range names {
		allowed[k] = true
	}
	for k, v := range values {
		if !allowed[k] || len(v) != 1 {
			return nil, problem(400, "malformed")
		}
		out[k] = v[0]
	}
	if required && len(out) != len(names) {
		return nil, problem(400, "malformed")
	}
	return out, nil
}
