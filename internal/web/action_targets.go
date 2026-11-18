package web

import (
	"mitos/internal/decision"
	"mitos/internal/studio"
	"net/http"
)

func (s *Server) actionTargetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/action-targets", func(w http.ResponseWriter, r *http.Request) {
		var in studio.ActionTargetInput
		if deskJSON(r, &in) != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_action_target"})
			return
		}
		target, err := s.engine.Store.ConfigureActionTarget(in, decision.LocalOperator())
		if err != nil {
			writeJSON(w, 422, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, target)
	})
}
