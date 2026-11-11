package web

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"mitos/internal/studio"
)

func graphFailure(w http.ResponseWriter, err error) {
	code := 422
	if errors.Is(err, studio.ErrNotFound) {
		code = 404
	}
	if errors.Is(err, studio.ErrConflict) {
		code = 409
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
func graphBody(w http.ResponseWriter, r *http.Request, v any) bool {
	b, err := io.ReadAll(r.Body)
	if err == nil {
		err = studio.DecodeGraphJSON(b, v)
	}
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return false
	}
	return true
}
func (s *Server) graphRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/graphs/applications", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.engine.GraphApplications()) })
	mux.HandleFunc("GET /api/graphs/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.GraphView(r.PathValue("id"), r.URL.Query().Get("node"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/graphs/{id}/nodes/{task}/runs/{run}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.GraphRun(r.PathValue("id"), r.PathValue("task"), r.PathValue("run"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, v)
	})

	mux.HandleFunc("GET /api/graphs/capabilities", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"profile": studio.GraphProfile, "complete_profile": false, "adapters": []string{"application", "graph_decision"}, "planning": []string{"defined", "adaptive", "hybrid"}, "atomic_amendments": true, "native_worker_output": true, "automatic_dispatch": true, "controller_proposals": true, "decision_selection": true, "interactive_pty_dispatch": false, "cross_graph_imports": false, "service_graph_templates": false, "enforced_confinement": false, "capacity": 2})
	})
	mux.HandleFunc("GET /api/graphs", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 100 {
				writeJSON(w, 400, map[string]string{"error": "limit must be 1..100"})
				return
			}
			limit = n
		}
		rows, next := s.engine.Store.GraphSummaries(r.URL.Query().Get("project_id"), r.URL.Query().Get("after"), limit)
		writeJSON(w, 200, map[string]any{"graphs": rows, "next": next, "truncated": next != ""})
	})
	mux.HandleFunc("POST /api/graphs", func(w http.ResponseWriter, r *http.Request) {
		var req studio.CreateGraph
		if !graphBody(w, r, &req) {
			return
		}
		g, err := s.engine.CreateGraph(req)
		if err != nil {
			graphFailure(w, err)
			return
		}
		w.Header().Set("Location", "/api/graphs/"+g.ID)
		writeJSON(w, 201, g)
	})
	mux.HandleFunc("GET /api/graphs/{id}", func(w http.ResponseWriter, r *http.Request) {
		g, err := s.engine.Store.Graph(r.PathValue("id"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, g)
	})
	mux.HandleFunc("GET /api/graphs/{id}/blockers", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.GraphBlockers(r.PathValue("id"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	for _, action := range []string{"preview", "amendments"} {
		mux.HandleFunc("POST /api/graphs/{id}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var p studio.GraphProposal
			if !graphBody(w, r, &p) {
				return
			}
			if p.GraphID != r.PathValue("id") {
				writeJSON(w, 400, map[string]string{"error": "graph_id must match the route"})
				return
			}
			var g studio.WorkGraph
			var receipt studio.GraphReceipt
			var err error
			if action == "preview" {
				g, receipt, err = s.engine.PreviewGraph(p)
			} else {
				g, receipt, err = s.engine.AmendGraph(p)
			}
			if err != nil {
				graphFailure(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"graph": g, "receipt": receipt, "preview": action == "preview"})
		})
	}
	mux.HandleFunc("POST /api/graphs/{id}/decisions", func(w http.ResponseWriter, r *http.Request) {
		var req studio.GraphDecisionRequest
		if !graphBody(w, r, &req) {
			return
		}
		if req.Proposal.GraphID != r.PathValue("id") {
			writeJSON(w, 400, map[string]string{"error": "graph_id must match the route"})
			return
		}
		d, err := s.engine.RequestGraphDecision(req)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 202, d)
	})
	mux.HandleFunc("POST /api/graphs/{id}/decisions/{key}/apply", func(w http.ResponseWriter, r *http.Request) {
		var empty struct{}
		if !graphBody(w, r, &empty) {
			return
		}
		receipt, err := s.engine.ApplyGraphDecision(r.PathValue("id"), r.PathValue("key"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, receipt)
	})
}
