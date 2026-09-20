package device

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/studio"
)

func testConnector(t *testing.T, n int) (*Server, string, studio.Device) {
	t.Helper()
	store, err := studio.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for i := 0; i < n; i++ {
		r, err := store.Create(studio.Brief{Title: fmt.Sprintf("Work %d", i), Workspace: ".", AppID: "workspace-inventory", TimeoutMinutes: 1}, "Inventory", "", false)
		if err != nil {
			t.Fatal(err)
		}
		card := &decision.Card{ID: fmt.Sprintf("decision_%d", i), Kind: "choose", Reversibility: "external", Question: "Continue this work?", Context: decision.Context{Attempted: "Inspected workspace.", BlockedBy: "Needs operator review."}, Options: []decision.Option{{ID: "retry", Label: "Retry", Consequence: "A new attempt runs."}, {ID: "cancel", Label: "Close", Consequence: "Work stays stopped."}}}
		if err = store.Park(r.ID, card); err != nil {
			t.Fatal(err)
		}
	}
	d, token, err := store.PairDevice(studio.DevicePairInput{Name: "Handheld", Permissions: studio.DevicePermissions{Read: true, Resolve: true, Voice: true}, ExpiresAt: time.Now().Add(time.Hour), STTDisclosure: "Local test; no retention"}, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	return New(&studio.Engine{Store: store}, nil), token, d
}
func call(s *Server, token, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://device.test"+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}
func snapshot(t *testing.T, s *Server, token string) studio.DeviceSnapshot {
	t.Helper()
	w := call(s, token, "GET", "/api/device/v1/snapshot", "", nil)
	var snap studio.DeviceSnapshot
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &snap) != nil {
		t.Fatalf("snapshot %d: %s", w.Code, w.Body.String())
	}
	return snap
}
func TestRemoteBoundaryAndVerifiedTLS(t *testing.T) {
	s, token, d := testConnector(t, 1)
	for _, test := range []struct {
		token, path string
		status      int
	}{{"", "/api/device/v1/snapshot", 401}, {"wrong", "/api/device/v1/snapshot", 401}, {token, "/api/runs", 404}, {token, "/api/devices", 404}, {token, "/api/device/v2/snapshot", 404}} {
		w := call(s, test.token, "GET", test.path, "", nil)
		if w.Code != test.status || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Type") != "application/json" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "http://localhost/api/device/v1/snapshot", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("plaintext accepted")
	}
	ts := httptest.NewTLSServer(s)
	defer ts.Close()
	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	request, _ := http.NewRequest("GET", ts.URL+"/api/device/v1/snapshot", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.Status)
	}
	bad := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "wrong.example"}}
	defer bad.CloseIdleConnections()
	if res, err := (&http.Client{Transport: bad}).Do(request); err == nil {
		res.Body.Close()
		t.Fatal("wrong TLS name accepted")
	}
	unknown := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool()}}
	defer unknown.CloseIdleConnections()
	if res, err := (&http.Client{Transport: unknown}).Do(request); err == nil {
		res.Body.Close()
		t.Fatal("untrusted chain accepted")
	}
	if _, _, err = s.engine.Store.ChangeDevice(d.ID, false, time.Time{}, decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if got := call(s, token, "GET", "/api/device/v1/snapshot", "", nil); got.Code != 403 {
		t.Fatal("revoked read", got.Code)
	}
}
func TestSnapshotStablePaginationAndCursorExpiry(t *testing.T) {
	s, token, _ := testConnector(t, 19)
	page := snapshot(t, s, token)
	if page.PendingTotal != 19 || len(page.Cards) != 8 || len(page.Runs) != 8 || page.NextCursor == "" {
		t.Fatalf("bad first page %+v", page)
	}
	cursor := page.NextCursor
	cards, runs := map[string]bool{}, map[string]bool{}
	for {
		for _, c := range page.Cards {
			if cards[c.DecisionID] {
				t.Fatal("duplicate card")
			}
			cards[c.DecisionID] = true
		}
		for _, r := range page.Runs {
			if runs[r.RunID] {
				t.Fatal("duplicate run")
			}
			runs[r.RunID] = true
		}
		if page.NextCursor == "" {
			break
		}
		w := call(s, token, "GET", "/api/device/v1/snapshot?cursor="+url.QueryEscape(page.NextCursor), "", nil)
		if w.Code != 200 || w.Body.Len() > 49152 {
			t.Fatal(w.Code, w.Body.Len(), w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
	}
	if len(cards) != 19 || len(runs) != 19 {
		t.Fatal("lost records", len(cards), len(runs))
	}
	// Any cursor can be polled repeatedly and continues the same immutable scan.
	a := call(s, token, "GET", "/api/device/v1/snapshot?cursor="+cursor, "", nil)
	b := call(s, token, "GET", "/api/device/v1/snapshot?cursor="+cursor, "", nil)
	if !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Fatal("cursor changed")
	}
	s.mu.Lock()
	for _, v := range s.views {
		v.expires = time.Now().Add(-time.Second)
	}
	s.mu.Unlock()
	if w := call(s, token, "GET", "/api/device/v1/snapshot?cursor="+cursor, "", nil); w.Code != 410 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestResolutionStrictJSONAndReplay(t *testing.T) {
	s, token, _ := testConnector(t, 1)
	card := snapshot(t, s, token).Cards[0]
	cmd := studio.DeviceCommand{Version: 1, CommandID: "0123456789abcdef0123456789abcdef", DecisionID: card.DecisionID, RunID: card.RunID, Revision: card.Revision, Choice: "cancel", Value: json.RawMessage("null")}
	body, _ := json.Marshal(cmd)
	for _, bad := range [][]byte{
		bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		bytes.Replace(body, []byte(`"version":1`), []byte(`"Version":1`), 1),
		bytes.Replace(body, []byte(`"version":1`), []byte(`"version":2`), 1),
		bytes.Replace(body, []byte(`,"value":null`), nil, 1),
		append(append([]byte{}, body...), []byte(` {}`)...),
		bytes.Replace(body, []byte(`"value":null`), []byte(`"value":null,"resolver":"forged"`), 1),
	} {
		w := call(s, token, "POST", "/api/device/v1/resolutions", "application/json", bad)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call(s, token, "POST", "/api/device/v1/resolutions", "application/json", bytes.Repeat([]byte(" "), 4096)); w.Code != 413 {
		t.Fatal(w.Code)
	}
	a := call(s, token, "POST", "/api/device/v1/resolutions", "application/json", body)
	b := call(s, token, "POST", "/api/device/v1/resolutions", "application/json", body)
	if a.Code != 202 || b.Code != 202 || !bytes.Equal(a.Body.Bytes(), b.Body.Bytes()) {
		t.Fatal(a.Code, a.Body.String(), b.Code, b.Body.String())
	}
	run, _ := s.engine.Store.Get(card.RunID)
	if run.Decision.Resolution == nil || run.Decision.Resolution.Via != "esp-notify" || run.Decision.AppliedAt == nil {
		t.Fatal("not applied through authority")
	}
	if got := snapshot(t, s, token).Cards[0].State; got != "applied" {
		t.Fatal(got)
	}
	conflict := call(s, token, "POST", "/api/device/v1/resolutions", "application/json", append(body, ' '))
	if conflict.Code != 409 {
		t.Fatal(conflict.Code)
	}
}

type transcribeFunc func(context.Context, []byte, string) (string, error)

func (f transcribeFunc) Transcribe(ctx context.Context, b []byte, locale string) (string, error) {
	return f(ctx, b, locale)
}
func wav() []byte {
	b := make([]byte, 6444)
	copy(b, "RIFF")
	copy(b[8:], "WAVEfmt ")
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	binary.LittleEndian.PutUint32(b[40:], 6400)
	return b
}
func voicePath(c studio.DeviceCard) string {
	return "/api/device/v1/voice-drafts?decision_id=" + c.DecisionID + "&run_id=" + c.RunID + "&revision=" + c.Revision
}
func TestVoiceDraftOnlyValidationAndRecheck(t *testing.T) {
	for _, mode := range []string{"success", "ambiguous", "silence", "timeout", "revocation", "resolution", "bad_audio", "oversized", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			s, token, d := testConnector(t, 1)
			card := snapshot(t, s, token).Cards[0]
			before, _ := s.engine.Store.Get(card.RunID)
			var calls atomic.Int32
			s.stt = transcribeFunc(func(ctx context.Context, pcm []byte, locale string) (string, error) {
				calls.Add(1)
				if len(pcm) != 6400 || locale != "en" {
					t.Error("wrong PCM")
				}
				switch mode {
				case "silence":
					return "   ", nil
				case "ambiguous":
					return "Maybe retry or close", nil
				case "timeout":
					<-ctx.Done()
					return "", ctx.Err()
				case "revocation":
					_, _, err := s.engine.Store.ChangeDevice(d.ID, false, time.Time{}, decision.LocalOperator())
					return "Close", err
				case "resolution":
					err := s.engine.Store.Resolve(card.RunID, card.DecisionID, "cancel")
					return "Close", err
				case "unsupported":
					return "✓", nil
				}
				return "Close", nil
			})
			s.voiceTimeout = 20 * time.Millisecond
			body := wav()
			want := 200
			switch mode {
			case "bad_audio":
				body[24] = 1
				want = 422
			case "oversized":
				body = make([]byte, 384045)
				want = 413
			case "silence", "unsupported":
				want = 422
			case "timeout":
				want = 503
			case "revocation":
				want = 403
			case "resolution":
				want = 409
			}
			w := call(s, token, "POST", voicePath(card), "audio/wav", body)
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if mode == "bad_audio" || mode == "oversized" {
				if calls.Load() != 0 {
					t.Fatal("invalid audio reached STT")
				}
			}
			if mode == "success" && (!strings.Contains(w.Body.String(), `"choice":"cancel"`) || !strings.Contains(w.Body.String(), `"value":null`)) {
				t.Fatal(w.Body.String())
			}
			if mode == "ambiguous" && !strings.Contains(w.Body.String(), `"choice":""`) {
				t.Fatal(w.Body.String())
			}
			after, _ := s.engine.Store.Get(card.RunID)
			if mode != "resolution" && (after.Decision.Resolution != nil || len(after.Events) != len(before.Events)) {
				t.Fatal("voice mutated decision")
			}
			log, _ := io.ReadAll(bytes.NewReader(mustJSON(after.Events)))
			if bytes.Contains(log, []byte("Maybe retry or close")) {
				t.Fatal("draft logged")
			}
		})
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func TestRateAndConcurrencyLimits(t *testing.T) {
	s, token, d := testConnector(t, 1)
	if !s.enter(d.ID) || !s.enter(d.ID) {
		t.Fatal("initial slots denied")
	}
	if w := call(s, token, "GET", "/api/device/v1/snapshot", "", nil); w.Code != 429 {
		t.Fatal(w.Code)
	}
	s.leave(d.ID)
	s.leave(d.ID)
	s.mu.Lock()
	s.limits[d.ID].tokens = 0
	s.mu.Unlock()
	if w := call(s, token, "GET", "/api/device/v1/snapshot", "", nil); w.Code != 429 {
		t.Fatal(w.Code)
	}
}
func TestWAVExactProfile(t *testing.T) {
	for _, mutate := range []func([]byte) []byte{
		func(b []byte) []byte { return b[:43] }, func(b []byte) []byte { return append(b, 0) }, func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte { b[20] = 3; return b }, func(b []byte) []byte { b[22] = 2; return b }, func(b []byte) []byte { b[34] = 8; return b }, func(b []byte) []byte { b[40] = 1; return b },
	} {
		if _, err := parseWAV(mutate(wav())); err == nil {
			t.Fatal("invalid WAV accepted")
		}
	}
}

func TestPollingReusesViewAndRestartExpiresCursor(t *testing.T) {
	s, token, d := testConnector(t, 12)
	first := snapshot(t, s, token)
	for range 6 {
		page := snapshot(t, s, token)
		if page.NextCursor != first.NextCursor {
			t.Fatal("unchanged poll allocated another cursor")
		}
	}
	if len(s.views) != 1 {
		t.Fatal("polling leaked snapshots", len(s.views))
	}
	restarted := New(s.engine, nil)
	if w := call(restarted, token, "GET", "/api/device/v1/snapshot?cursor="+first.NextCursor, "", nil); w.Code != 410 {
		t.Fatal(w.Code, w.Body.String())
	}
	_, rotated, err := s.engine.Store.ChangeDevice(d.ID, true, time.Now().Add(time.Hour), decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if w := call(s, rotated, "GET", "/api/device/v1/snapshot?cursor="+first.NextCursor, "", nil); w.Code != 410 {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestSnapshotByteBoundAndUnsupportedDisplay(t *testing.T) {
	s, token, _ := testConnector(t, 0)
	for i := 0; i < 10; i++ {
		r, err := s.engine.Store.Create(studio.Brief{Title: "Review", Workspace: ".", AppID: "workspace-inventory", TimeoutMinutes: 1}, "Inventory", "", false)
		if err != nil {
			t.Fatal(err)
		}
		card := &decision.Card{ID: fmt.Sprintf("large_%d", i), Kind: "choose", Reversibility: "external", Question: "Continue?", Context: decision.Context{Attempted: strings.Repeat("a", 3700), BlockedBy: "Needs approval."}, CostOfWrong: decision.Cost{Detail: strings.Repeat("c", 1600)}, Options: []decision.Option{{ID: "retry", Label: "Retry", Consequence: strings.Repeat("x", 1024)}, {ID: "cancel", Label: "Close", Consequence: strings.Repeat("y", 1024)}}}
		if err = s.engine.Store.Park(r.ID, card); err != nil {
			t.Fatal(err)
		}
	}
	page := snapshot(t, s, token)
	if len(page.Cards) >= 8 || page.PendingTotal != 10 || page.NextCursor == "" {
		t.Fatal("byte bound did not reduce page size", len(page.Cards))
	}
	count := len(page.Cards)
	for page.NextCursor != "" {
		w := call(s, token, "GET", "/api/device/v1/snapshot?cursor="+page.NextCursor, "", nil)
		if w.Code != 200 || w.Body.Len() > 49152 {
			t.Fatal(w.Code, w.Body.Len())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		count += len(page.Cards)
	}
	if count != 10 {
		t.Fatal("byte bound lost cards", count)
	}
	for _, text := range []string{"Unsupported glyph ✓", strings.Repeat("a", 4096)} {
		t.Run(fmt.Sprint(len(text)), func(t *testing.T) {
			s, token, _ := testConnector(t, 0)
			r, err := s.engine.Store.Create(studio.Brief{Title: "Review", Workspace: ".", AppID: "workspace-inventory", TimeoutMinutes: 1}, "Inventory", "", false)
			if err != nil {
				t.Fatal(err)
			}
			c := &decision.Card{ID: "unsupported", Kind: "choose", Reversibility: "external", Question: "Continue?", Context: decision.Context{Attempted: "Inspected", BlockedBy: text}, Options: []decision.Option{{ID: "retry", Label: "Retry", Consequence: "Runs again"}, {ID: "cancel", Label: "Close", Consequence: "Stays stopped"}}}
			if err = s.engine.Store.Park(r.ID, c); err != nil {
				t.Fatal(err)
			}
			w := call(s, token, "GET", "/api/device/v1/snapshot", "", nil)
			if w.Code != 422 || !strings.Contains(w.Body.String(), "display_unsupported") {
				t.Fatal(w.Code, w.Body.String())
			}
			current, _ := s.engine.Store.Get(r.ID)
			if current.Decision == nil || current.Decision.Resolution != nil {
				t.Fatal("unsupported projection lost console decision")
			}
		})
	}
}
func TestReadOnlyWrongTenantAndRevokedCannotTranscribeOrResolve(t *testing.T) {
	s, token, _ := testConnector(t, 1)
	c := snapshot(t, s, token).Cards[0]
	var calls atomic.Int32
	s.stt = transcribeFunc(func(context.Context, []byte, string) (string, error) { calls.Add(1); return "Close", nil })
	for _, mode := range []string{"read_only", "wrong_tenant", "revoked", "expired"} {
		t.Run(mode, func(t *testing.T) {
			p := decision.LocalOperator()
			permissions := studio.DevicePermissions{Read: true, Resolve: true, Voice: true}
			if mode == "read_only" {
				permissions.Resolve = false
				permissions.Voice = false
			}
			if mode == "wrong_tenant" {
				p.Tenant.Project = "other"
			}
			expiry := time.Now().Add(time.Hour)
			if mode == "expired" {
				expiry = time.Now().Add(20 * time.Millisecond)
			}
			d, badToken, err := s.engine.Store.PairDevice(studio.DevicePairInput{Name: mode, Permissions: permissions, ExpiresAt: expiry, STTDisclosure: "Local"}, p)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "revoked" {
				_, _, err = s.engine.Store.ChangeDevice(d.ID, false, time.Time{}, p)
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired" {
				time.Sleep(25 * time.Millisecond)
			}
			expected := 403
			if mode == "wrong_tenant" {
				expected = 404
			}
			w := call(s, badToken, "POST", voicePath(c), "audio/wav", wav())
			if w.Code != expected {
				t.Fatal(w.Code, w.Body.String())
			}
			cmd := studio.DeviceCommand{Version: 1, CommandID: "command", DecisionID: c.DecisionID, RunID: c.RunID, Revision: c.Revision, Choice: "cancel", Value: json.RawMessage("null")}
			w = call(s, badToken, "POST", "/api/device/v1/resolutions", "application/json", mustJSON(cmd))
			if w.Code != expected {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized audio reached STT")
	}
	r, _ := s.engine.Store.Get(c.RunID)
	if r.Decision.Resolution != nil {
		t.Fatal("unauthorized resolution")
	}
}

func TestVoiceTextDraftRequiresConfirmedSchemaValidAnswer(t *testing.T) {
	s, token, _ := testConnector(t, 0)
	a, secret, err := s.engine.Store.CreateAgentSession("codex", "Name request", ".", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"id":1,"method":"item/tool/requestUserInput","params":{"threadId":"thread","turnId":"turn","questions":[{"id":"name","question":"What name?","schema":{"type":"string","minLength":2,"maxLength":8,"pattern":"^[A-Za-z]+$"}}]}}`)
	source, err := s.engine.Store.IngestAgentRequest(a.ID, secret, raw)
	if err != nil {
		t.Fatal(err)
	}
	card := snapshot(t, s, token).Cards[0]
	if card.Input != "text" {
		t.Fatal(card.Input)
	}
	s.stt = transcribeFunc(func(context.Context, []byte, string) (string, error) { return "Beta", nil })
	draft := call(s, token, "POST", voicePath(card), "audio/wav", wav())
	if draft.Code != 200 || !strings.Contains(draft.Body.String(), `"value":"Beta"`) || !strings.Contains(draft.Body.String(), `"choice":""`) {
		t.Fatal(draft.Code, draft.Body.String())
	}
	pending, err := s.engine.Store.AgentRequestStatus(a.ID, secret, source.RequestID)
	if err != nil || pending.State != "pending" {
		t.Fatal("voice delivered an answer", pending, err)
	}
	s.stt = transcribeFunc(func(context.Context, []byte, string) (string, error) { return "123", nil })
	if w := call(s, token, "POST", voicePath(card), "audio/wav", wav()); w.Code != 422 {
		t.Fatal("invalid draft passed schema", w.Code)
	}
	cmd := studio.DeviceCommand{Version: 1, CommandID: "confirmed_text", DecisionID: card.DecisionID, RunID: card.RunID, Revision: card.Revision, Choice: "answer", Value: json.RawMessage(`"Beta"`)}
	confirmed := call(s, token, "POST", "/api/device/v1/resolutions", "application/json", mustJSON(cmd))
	if confirmed.Code != 202 {
		t.Fatal(confirmed.Code, confirmed.Body.String())
	}
	answer, err := s.engine.Store.AgentRequestStatus(a.ID, secret, source.RequestID)
	if err != nil || !strings.Contains(string(answer.Body), `"answers":["Beta"]`) {
		t.Fatal(answer, err)
	}
}
func TestConnectionDroppedAfterCommitReplaysReceipt(t *testing.T) {
	s, token, _ := testConnector(t, 1)
	c := snapshot(t, s, token).Cards[0]
	cmd := studio.DeviceCommand{Version: 1, CommandID: "dropped_connection", DecisionID: c.DecisionID, RunID: c.RunID, Revision: c.Revision, Choice: "cancel", Value: json.RawMessage("null")}
	body := mustJSON(cmd)
	var count atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			committed := httptest.NewRecorder()
			s.ServeHTTP(committed, r)
			if committed.Code != 202 {
				t.Error("request did not commit", committed.Code, committed.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		s.ServeHTTP(w, r)
	}))
	defer server.Close()
	request := func() (*http.Response, error) {
		r, _ := http.NewRequest("POST", server.URL+"/api/device/v1/resolutions", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		return server.Client().Do(r)
	}
	if res, err := request(); err == nil {
		res.Body.Close()
		t.Fatal("lost connection returned success")
	}
	response, err := request()
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var receipt studio.DeviceReceipt
	if response.StatusCode != 202 || json.NewDecoder(response.Body).Decode(&receipt) != nil || receipt.CommandID != cmd.CommandID || receipt.State != "accepted" {
		t.Fatal(response.Status, receipt)
	}
	run, _ := s.engine.Store.Get(c.RunID)
	resolutions := 0
	for _, event := range run.Events {
		if event.Kind == "approval_resolved" {
			resolutions++
		}
	}
	if resolutions != 1 || run.Decision.AppliedAt == nil {
		t.Fatal("lost acknowledgement replayed execution", resolutions)
	}
}
