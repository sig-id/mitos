package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"mitos/internal/studio"
)

func (s *Server) lifecycleAction(id, action, key string) (studio.ServiceLifecycleView, error) {
	if action == "stop" {
		return s.engine.StopService(id, key)
	}
	if action == "restart" {
		return s.engine.RestartService(id, key)
	}
	if action == "resume" {
		return s.engine.ResumeService(id, key)
	}
	return studio.ServiceLifecycleView{}, errors.New("choose stop, restart, or resume")
}
func (s *Server) apiServiceLifecycleAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Key    string `json:"idempotency_key"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF || (body.Action != "stop" && body.Action != "resume" && body.Action != "restart") {
		writeJSON(w, 400, map[string]string{"error": "send action (stop, restart, or resume) and idempotency_key"})
		return
	}
	view, err := s.lifecycleAction(r.PathValue("id"), body.Action, body.Key)
	if err != nil {
		code := 409
		if errors.Is(err, studio.ErrNotFound) {
			code = 404
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) serviceLifecycleAction(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	view, err := s.lifecycleAction(r.PathValue("id"), r.FormValue("action"), r.FormValue("idempotency_key"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/services/lifecycle/"+strconv.FormatInt(view.Seq, 10))
}
func (s *Server) apiServiceLifecycles(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.engine.Store.ServiceLifecycles())
}
func (s *Server) apiServiceLifecycle(w http.ResponseWriter, r *http.Request) {
	seq, _ := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	view, err := s.engine.Store.ServiceLifecycle(seq)
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) serviceLifecycle(w http.ResponseWriter, r *http.Request) {
	seq, _ := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	view, err := s.engine.Store.ServiceLifecycle(seq)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.base("service-lifecycle", "Service lifecycle", "work")
	p.Live = false
	p.ServiceLifecycle = &view
	s.render(w, r, p, 200)
}
