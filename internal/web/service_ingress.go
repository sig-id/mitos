package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"mitos/internal/studio"
)

func ingressBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	content, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || content != "application/json" {
		writeJSON(w, 415, map[string]string{"error": "send application/json"})
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, studio.MaxIngressBytes+1))
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			writeJSON(w, 413, map[string]string{"error": "delivery JSON exceeds 64 KiB"})
			return nil, false
		}
		writeJSON(w, 400, map[string]string{"error": "cannot read delivery body"})
		return nil, false
	}
	if len(data) > studio.MaxIngressBytes {
		writeJSON(w, 413, map[string]string{"error": "delivery JSON exceeds 64 KiB"})
		return nil, false
	}
	normalized, err := studio.NormalizeIngressPayload(data)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return nil, false
	}
	return normalized, true
}
func ingressResponse(w http.ResponseWriter, run studio.Run, err error) {
	if err != nil {
		code := 409
		if errors.Is(err, studio.ErrNotFound) {
			code = 404
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, run)
}
func (s *Server) apiServiceSignal(w http.ResponseWriter, r *http.Request) {
	data, ok := ingressBody(w, r)
	if !ok {
		return
	}
	var body struct {
		Source     string          `json:"source"`
		DeliveryID string          `json:"delivery_id"`
		Cycle      string          `json:"cycle,omitempty"`
		Payload    json.RawMessage `json:"payload"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || body.Payload == nil {
		writeJSON(w, 400, map[string]string{"error": "send source, delivery_id, payload, and optional cycle"})
		return
	}
	run, err := s.engine.DeliverSignal(r.PathValue("id"), body.Source, body.DeliveryID, body.Cycle, body.Payload)
	ingressResponse(w, run, err)
}
func (s *Server) serviceWebhook(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != r.URL.Path || r.URL.RawQuery != "" || len(r.Header.Values("Idempotency-Key")) != 1 || len(r.Header.Values("X-Mitos-Cycle")) > 1 {
		writeJSON(w, 400, map[string]string{"error": "use a canonical path, one Idempotency-Key, and optional X-Mitos-Cycle"})
		return
	}
	data, ok := ingressBody(w, r)
	if !ok {
		return
	}
	run, err := s.engine.DeliverWebhook(r.URL.Path, r.Header.Get("Idempotency-Key"), r.Header.Get("X-Mitos-Cycle"), data)
	ingressResponse(w, run, err)
}
