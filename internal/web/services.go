package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"mitos/internal/studio"
)

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	view, err := s.engine.Store.Services()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.base("services", "Services", "work")
	p.Live = false
	p.Services = &view
	p.ServiceLifecycles = s.engine.Store.ServiceLifecycles()
	p.CustomEvents = s.engine.Store.CustomEvents()
	data, _ := json.MarshalIndent(view.Catalog, "", "  ")
	p.ServicesJSON = string(data)
	s.render(w, r, p, 200)
}
func (s *Server) apiServices(w http.ResponseWriter, r *http.Request) {
	view, err := s.engine.Store.Services()
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) previewServices(w http.ResponseWriter, r *http.Request) {
	var c studio.ServiceCatalog
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil || d.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "send one service catalog"})
		return
	}
	view, err := s.engine.PreviewServices(c)
	if err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) apiConfigureServices(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Expected *int64                `json:"expected_revision"`
		Catalog  studio.ServiceCatalog `json:"catalog"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || body.Expected == nil || *body.Expected < 0 || d.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "send a catalog and expected_revision"})
		return
	}
	view, err := s.engine.ConfigureServices(body.Catalog, *body.Expected)
	if err != nil {
		status := 422
		if errors.Is(err, studio.ErrConflict) {
			status = 409
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, view)
}
func (s *Server) configureServices(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	revision, err := strconv.ParseInt(r.FormValue("expected_revision"), 10, 64)
	if err != nil || revision < 0 {
		s.fail(w, r, errors.New("invalid catalog revision"))
		return
	}
	var c studio.ServiceCatalog
	d := json.NewDecoder(strings.NewReader(r.FormValue("catalog")))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil || d.Decode(new(any)) != io.EOF {
		s.fail(w, r, errors.New("send one valid service catalog"))
		return
	}
	if _, err = s.engine.ConfigureServices(c, revision); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/services")
}
func (s *Server) apiTriggerService(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"idempotency_key"`
		Cycle string `json:"cycle,omitempty"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "send one idempotency_key"})
		return
	}
	run, err := s.engine.TriggerService(r.PathValue("id"), body.Key, body.Cycle)
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, run)
}
func (s *Server) triggerService(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	run, err := s.engine.TriggerService(r.PathValue("id"), r.FormValue("idempotency_key"), r.FormValue("cycle"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/runs/"+run.ID)
}
