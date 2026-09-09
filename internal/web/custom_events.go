package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"mitos/internal/studio"
)

func (s *Server) apiEmitEvent(w http.ResponseWriter, r *http.Request) {
	data, ok := ingressBody(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    string          `json:"name"`
		Key     string          `json:"idempotency_key"`
		Payload json.RawMessage `json:"payload"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || body.Payload == nil {
		writeJSON(w, 400, map[string]string{"error": "send name, idempotency_key, and payload"})
		return
	}
	view, err := s.engine.EmitServiceEvent(body.Name, body.Key, body.Payload)
	if err != nil {
		code := 400
		if errors.Is(err, studio.ErrConflict) {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) apiCustomEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.engine.Store.CustomEvents())
}
func (s *Server) apiCustomEvent(w http.ResponseWriter, r *http.Request) {
	seq, _ := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	view, err := s.engine.Store.CustomEvent(seq)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) emitEvent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	view, err := s.engine.EmitServiceEvent(r.FormValue("name"), r.FormValue("idempotency_key"), []byte(r.FormValue("payload")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/services/events/"+strconv.FormatInt(view.Seq, 10))
}
func (s *Server) customEvent(w http.ResponseWriter, r *http.Request) {
	seq, _ := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	view, err := s.engine.Store.CustomEvent(seq)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.base("custom-event", "Service event", "work")
	p.Live = false
	p.CustomEvent = &view
	s.render(w, r, p, 200)
}
