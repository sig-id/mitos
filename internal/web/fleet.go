package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (s *Server) apiFleet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.engine.Store.FleetIndex())
}
func (s *Server) apiEvidence(w http.ResponseWriter, r *http.Request) {
	e, err := s.engine.Store.FleetEvidence(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, e)
}
func (s *Server) apiReleaseClaims(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirmed bool `json:"confirmed_stopped"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || !body.Confirmed || d.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "confirm that the previous execution was inspected and stopped with confirmed_stopped: true"})
		return
	}
	if err := s.engine.ReleaseInspectedClaims(r.PathValue("id")); err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]string{"run_id": r.PathValue("id"), "status": "claims_released"})
}
func (s *Server) releaseClaims(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil || r.FormValue("confirmed_stopped") != "true" {
		http.Error(w, "confirm that the previous execution was inspected and stopped", 400)
		return
	}
	if err := s.engine.ReleaseInspectedClaims(r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/runs/"+r.PathValue("id"))
}
func ownershipLines(value string) []string {
	var lines []string
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
