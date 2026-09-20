package device

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"mitos/internal/studio"
)

// Transcriber receives validated mono signed little-endian PCM16 at 16 kHz.
// Providers must honor cancellation. Neither audio nor draft text is persisted.
type Transcriber interface {
	Transcribe(context.Context, []byte, string) (string, error)
}

// CommandTranscriber runs an explicitly configured executable without a shell.
// Raw PCM arrives on stdin, UTF-8 transcript on stdout. Provider credentials and
// routing are local configuration; stderr is deliberately not logged.
type CommandTranscriber struct{ Path string }

func (p CommandTranscriber) Transcribe(ctx context.Context, pcm []byte, locale string) (string, error) {
	cmd := exec.CommandContext(ctx, p.Path)
	cmd.Env = append(os.Environ(), "MITOS_STT_SAMPLE_RATE=16000", "MITOS_STT_CHANNELS=1", "MITOS_STT_FORMAT=s16le", "MITOS_STT_LOCALE="+locale)
	cmd.Stdin = bytes.NewReader(pcm)
	var output boundedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return output.String(), nil
}

type boundedOutput struct{ buffer bytes.Buffer }

func (b *boundedOutput) String() string { return b.buffer.String() }
func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 1024 {
		return 0, errors.New("transcript too large")
	}
	return b.buffer.Write(p)
}
func parseWAV(b []byte) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, problem(422, "audio_unsupported") }
	if len(b) < 44 || len(b) > 384044 || string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" || string(b[12:16]) != "fmt " || string(b[36:40]) != "data" {
		return bad()
	}
	u16 := func(i int) uint16 { return binary.LittleEndian.Uint16(b[i : i+2]) }
	u32 := func(i int) uint32 { return binary.LittleEndian.Uint32(b[i : i+4]) }
	samples := len(b) - 44
	if int(u32(4)) != len(b)-8 || u32(16) != 16 || u16(20) != 1 || u16(22) != 1 || u32(24) != 16000 || u32(28) != 32000 || u16(32) != 2 || u16(34) != 16 || int(u32(40)) != samples || samples < 6400 || samples > 384000 || samples%2 != 0 {
		return bad()
	}
	return b[44:], nil
}
func (s *Server) voice(w http.ResponseWriter, r *http.Request, token string) {
	ctx, cancel := context.WithTimeout(r.Context(), s.voiceTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	// Bound upload plus inference, not only provider execution.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(19 * time.Second))
	query, err := strictQuery(r, []string{"decision_id", "run_id", "revision"}, true)
	if err != nil {
		fail(w, err)
		return
	}
	for _, v := range query {
		if !studio.ValidDeviceID(v) {
			fail(w, problem(400, "malformed"))
			return
		}
	}
	card, err := s.engine.Store.DeviceDraftCard(token, query["run_id"], query["decision_id"], query["revision"])
	if err != nil {
		fail(w, err)
		return
	}
	if s.stt == nil {
		fail(w, problem(503, "stt_unavailable"))
		return
	}
	body, err := readBody(w, r, "audio/wav", 384044)
	if err != nil {
		fail(w, err)
		return
	}
	pcm, err := parseWAV(body)
	if err != nil {
		clear(body)
		fail(w, err)
		return
	}
	if _, err = s.engine.Store.DeviceDraftCard(token, query["run_id"], query["decision_id"], query["revision"]); err != nil {
		clear(body)
		fail(w, err)
		return
	}
	select {
	case s.sttSlots <- struct{}{}:
	default:
		clear(body)
		fail(w, problem(429, "rate_limited"))
		return
	}

	type result struct {
		text string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { clear(body); <-s.sttSlots }()
		text, err := s.stt.Transcribe(ctx, pcm, "en")
		done <- result{text, err}
	}()
	var resultText string
	select {
	case <-ctx.Done():
		fail(w, problem(503, "stt_unavailable"))
		return
	case result := <-done:
		if result.err != nil || ctx.Err() != nil {
			fail(w, problem(503, "stt_unavailable"))
			return
		}
		resultText = strings.TrimSpace(result.text)
	}
	if resultText == "" || !studio.DeviceText(resultText, 1024) {
		fail(w, problem(422, "transcript_unsupported"))
		return
	}
	// STT cannot authorize a choice. Exact offered labels/IDs can populate a draft;
	// ambiguous or free-form speech deliberately leaves choice empty.
	choice := ""
	matches := 0
	for _, o := range card.Options {
		if strings.EqualFold(resultText, o.ID) || strings.EqualFold(resultText, o.Label) {
			choice = o.ID
			matches++
		}
	}
	if matches != 1 {
		choice = ""
	}
	var value *string
	rawValue := json.RawMessage("null")
	if card.Input == "text" {
		value = &resultText
		rawValue, _ = json.Marshal(resultText)
		choice = ""
	}
	if err = s.engine.Store.ValidateDeviceDraftValue(token, card.RunID, card.DecisionID, card.Revision, rawValue); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, struct {
		Version    int     `json:"version"`
		DecisionID string  `json:"decision_id"`
		RunID      string  `json:"run_id"`
		Revision   string  `json:"revision"`
		Choice     string  `json:"choice"`
		Transcript string  `json:"transcript"`
		Value      *string `json:"value"`
	}{1, card.DecisionID, card.RunID, card.Revision, choice, resultText, value})
}
