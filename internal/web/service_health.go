package web

import (
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) apiServiceHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MonitorID string `json:"monitor_id"`
		Sequence  uint64 `json:"sequence"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || body.MonitorID == "" || len(body.MonitorID) > 128 || body.Sequence == 0 {
		writeJSON(w, 400, map[string]string{"error": "send monitor_id and a positive sequence"})
		return
	}
	state, err := s.engine.Store.ServiceHeartbeat(r.PathValue("id"), body.MonitorID, body.Sequence)
	if err != nil {
		writeJSON(w, 409, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, state)
}
