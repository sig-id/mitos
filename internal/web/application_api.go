package web

import (
	"errors"
	"net/http"

	"mitos/internal/apiversion"
	"mitos/internal/studio"
)

// Application-facing operations of the versioned /api surface. Admission uses
// POST /api/tasks (or /api/runs) with a structured brief; these add discovery,
// the typed result read and cancellation.
func (s *Server) applicationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"api_versions":       apiversion.Supported,
			"version_header":     apiversion.Header,
			"websocket_protocol": apiversion.WebSocketProtocol,
			"operations": map[string]string{
				"admit":  "POST /api/tasks",
				"status": "GET /api/runs/{id}",
				"events": "GET /api/runs/{id}/events",
				"result": "GET /api/runs/{id}/result",
				"cancel": "POST /api/runs/{id}/cancel",
			},
			"structured_work":    studio.StructuredCapabilities(),
			"agent_capabilities": studio.RuntimeCapabilityProfiles(),
			// Hosted delivery gates S2–S4 are not implemented by this build.
			"hosted": map[string]bool{"service_principal_auth": false, "tenant_isolation": false, "postgresql": false, "remote_workers": false, "mqtt_work_claims": false, "conversation_waits": false},
			"access": "loopback only; same-user access is not multi-tenant authorization",
		})
	})
	mux.HandleFunc("GET /api/runs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		data, err := s.engine.StructuredResult(r.PathValue("id"))
		switch {
		case errors.Is(err, studio.ErrNotFound):
			writeJSON(w, 404, map[string]string{"error": "no structured result for this run"})
		case errors.Is(err, studio.ErrConflict):
			writeJSON(w, 409, map[string]string{"error": "result_not_accepted", "detail": "The run has not finished with an accepted typed result."})
		case err != nil:
			writeJSON(w, 500, map[string]string{"error": err.Error()})
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(data)
		}
	})
	mux.HandleFunc("POST /api/runs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := s.engine.Cancel(id); err != nil {
			status := 422
			if errors.Is(err, studio.ErrNotFound) {
				status = 404
			} else if errors.Is(err, studio.ErrConflict) {
				status = 409
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		run, err := s.engine.Store.Get(id)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 202, run)
	})
}
