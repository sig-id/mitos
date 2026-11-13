package web

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"

	"mitos/internal/agentpolicy"
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
	mux.HandleFunc("GET /agents", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, r, s.base("agents", "Agent library", "agents"), 200)
	})
	mux.HandleFunc("GET /api/agent-library", func(w http.ResponseWriter, r *http.Request) {
		library, err := s.engine.AgentLibrary()
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, library)
	})
	mux.HandleFunc("POST /api/agent-library", func(w http.ResponseWriter, r *http.Request) {
		var req studio.AgentEdit
		if !graphBody(w, r, &req) {
			return
		}
		d, err := s.engine.SaveAgent(req)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, d)
	})

	mux.HandleFunc("GET /api/projects/{project}/setup/conversation", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.engine.SetupConversation(r.PathValue("project"), r.URL.Query().Get("target"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, c)
	})
	mux.HandleFunc("POST /api/projects/{project}/setup/conversation", func(w http.ResponseWriter, r *http.Request) {
		var req studio.SetupAsk
		if !graphBody(w, r, &req) {
			return
		}
		c, err := s.engine.AskSetup(r.PathValue("project"), req)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 202, c)
	})
	mux.HandleFunc("POST /api/projects/{project}/setup/conversation/stop", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Target string `json:"target"`
		}
		if !graphBody(w, r, &req) {
			return
		}
		if err := s.engine.CancelSetupConversation(r.PathValue("project"), req.Target); err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"stopped": true})
	})

	mux.HandleFunc("GET /api/preferences", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Preferences("")
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("PUT /api/preferences", func(w http.ResponseWriter, r *http.Request) {
		var v studio.RuntimePreferences
		if !graphBody(w, r, &v) {
			return
		}
		saved, err := s.engine.SavePreferences("", v)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, saved)
	})
	mux.HandleFunc("PUT /api/projects/{project}/agent-policy", func(w http.ResponseWriter, r *http.Request) {
		var p agentpolicy.Policy
		if !graphBody(w, r, &p) {
			return
		}
		saved, err := s.engine.SaveAgentPolicy(r.PathValue("project"), p)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, saved)
	})
	mux.HandleFunc("GET /api/projects/{project}/setup", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.ProjectSetup(r.PathValue("project"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("PUT /api/projects/{project}/instructions", func(w http.ResponseWriter, r *http.Request) {
		var v studio.InstructionsEdit
		if !graphBody(w, r, &v) {
			return
		}
		saved, err := s.engine.SaveInstructions(r.PathValue("project"), v)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, saved)
	})
	mux.HandleFunc("PUT /api/projects/{project}/preferences", func(w http.ResponseWriter, r *http.Request) {
		var v studio.RuntimePreferences
		if !graphBody(w, r, &v) {
			return
		}
		saved, err := s.engine.SavePreferences(r.PathValue("project"), v)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, saved)
	})
	mux.HandleFunc("GET /api/graphs/agents", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.engine.AutomaticAgents()) })
	mux.HandleFunc("POST /api/graphs/automatic", func(w http.ResponseWriter, r *http.Request) {
		var req studio.AutomaticGoal
		if !graphBody(w, r, &req) {
			return
		}
		g, err := s.engine.StartAutomaticGoal(req)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 201, g)
	})
	mux.HandleFunc("POST /api/graphs/{id}/automatic", func(w http.ResponseWriter, r *http.Request) {
		var req studio.AutomaticStart
		if !graphBody(w, r, &req) {
			return
		}
		g, err := s.engine.StartExistingAutomatic(r.PathValue("id"), req)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, g)
	})

	mux.HandleFunc("POST /api/graphs/{id}/result", func(w http.ResponseWriter, r *http.Request) {
		var req studio.PublishGraphResult
		if !graphBody(w, r, &req) {
			return
		}
		g, err := s.engine.Store.PublishResult(r.PathValue("id"), req)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, g)
	})
	mux.HandleFunc("GET /api/graphs/applications", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.engine.GraphApplications()) })
	mux.HandleFunc("GET /api/graphs/{id}/snapshot", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.GraphView(r.PathValue("id"), r.URL.Query().Get("node"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/graphs/{id}/nodes/{task}/runs/{run}/files", func(w http.ResponseWriter, r *http.Request) {
		list := s.engine.Store.GraphRunFiles
		if r.URL.Query().Get("scope") == "workspace" {
			list = s.engine.Store.GraphRunWorkspaceFiles
		}
		files, err := list(r.PathValue("id"), r.PathValue("task"), r.PathValue("run"))
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, files)
	})
	mux.HandleFunc("GET /api/graphs/{id}/nodes/{task}/runs/{run}/file", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("path")
		body, err := s.engine.Store.GraphRunFile(r.PathValue("id"), r.PathValue("task"), r.PathValue("run"), name)
		if err != nil {
			graphFailure(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(body)
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
		writeJSON(w, 200, map[string]any{"profile": studio.GraphProfile, "complete_profile": false, "adapters": []string{"application", "graph_decision"}, "planning": []string{"defined", "adaptive", "hybrid"}, "atomic_amendments": true, "native_worker_output": true, "automatic_dispatch": true, "automatic_goal_planning": true, "completed_result_continuations": true, "intent_shaped_results": true, "native_agent_runtimes": []string{"codex"}, "editable_agent_library": true, "agent_specialization": true, "controller_proposals": true, "decision_selection": true, "interactive_pty_dispatch": false, "cross_graph_imports": false, "service_graph_templates": false, "enforced_confinement": false, "capacity": 2})
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
