package web

import (
	"net/http"
	"time"

	"mitos/internal/decision"
	"mitos/internal/studio"
)

func (s *Server) principalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/workers", func(w http.ResponseWriter, r *http.Request) {
		var in studio.WorkerCredential
		if deskJSON(r, &in) != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_worker"})
			return
		}
		in.Local = false
		p, token, err := s.engine.Store.CreateWorkerCredential(in, decision.LocalOperator())
		if err != nil {
			writeJSON(w, 422, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, map[string]any{"worker": p, "token": token})
	})
	mux.HandleFunc("DELETE /api/workers/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.engine.Store.RevokeWorker(r.PathValue("id"), decision.LocalOperator()); err != nil {
			writeJSON(w, 404, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, 200, map[string]bool{"revoked": true})
	})

	mux.HandleFunc("GET /api/service-principals", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.engine.Store.ServicePrincipals(decision.LocalOperator()))
	})
	mux.HandleFunc("POST /api/service-principals", func(w http.ResponseWriter, r *http.Request) {
		var in studio.ServicePrincipalInput
		if err := deskJSON(r, &in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_principal"})
			return
		}
		principal, token, err := s.engine.Store.CreateServicePrincipal(in, decision.LocalOperator())
		if err != nil {
			writeJSON(w, 422, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 201, map[string]any{"principal": principal, "token": token})
	})
	mux.HandleFunc("POST /api/service-principals/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ExpiresAt time.Time `json:"expires_at"`
		}
		if err := deskJSON(r, &in); err != nil {
			writeJSON(w, 400, map[string]string{"error": "invalid_expiry"})
			return
		}
		principal, token, err := s.engine.Store.ChangeServicePrincipal(r.PathValue("id"), true, in.ExpiresAt, decision.LocalOperator())
		if err != nil {
			writeJSON(w, 422, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]any{"principal": principal, "token": token})
	})
	mux.HandleFunc("DELETE /api/service-principals/{id}", func(w http.ResponseWriter, r *http.Request) {
		principal, _, err := s.engine.Store.ChangeServicePrincipal(r.PathValue("id"), false, time.Time{}, decision.LocalOperator())
		if err != nil {
			writeJSON(w, 404, map[string]string{"error": "not_found"})
			return
		}
		writeJSON(w, 200, principal)
	})
}
