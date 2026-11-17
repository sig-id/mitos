package web

import (
	"mitos/internal/studio"
	"net/http"
)

func (s *Server) runtimePolicyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/runtime-policy", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.engine.Store.RuntimePolicy()) })
	mux.HandleFunc("POST /api/runtime-policy", func(w http.ResponseWriter, r *http.Request) {
		var policy studio.RuntimePolicy
		if err := deskJSON(r, &policy); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_runtime_policy"})
			return
		}
		if err := s.engine.Store.ConfigureRuntimePolicy(policy); err != nil {
			writeJSON(w, 422, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, s.engine.Store.RuntimePolicy())
	})
}
