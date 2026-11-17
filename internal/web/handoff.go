package web

import (
	"encoding/json"
	"errors"
	"io"
	"mitos/agents"
	"mitos/internal/studio"
	"net/http"
)

func (s *Server) handoffRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/handoff", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"version": studio.HandoffVersion, "tools": studio.HandoffTools(), "read_bytes": studio.HandoffReadLimit, "response_bytes": studio.HandoffResponseLimit, "retrieval_bytes_per_attempt": studio.HandoffRetrievalBudget, "token_enforcement": "unsupported", "retention": "until_store_deleted", "proposals": map[string]any{"capability": studio.ProposalCapability, "per_task": studio.HandoffProposalsPerTask, "tasks_per_proposal": studio.HandoffTasksPerProposal, "yields_per_task": studio.HandoffYieldsPerTask, "review_operations": []string{"accept_proposal", "reject_proposal"}, "task_traits": map[string]any{"required": []string{"size", "intelligence"}, "natures": agents.Natures, "sizes": studio.TaskSizes, "intelligence": studio.TaskIntelligences}}})
	})
	// Agent requests carry an existing leased session token; producer identity and
	// workspace come from that binding, not body fields supplied by the model.
	mux.HandleFunc("POST /api/agent-sessions/{session}/handoff/{operation}", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
		if err != nil {
			graphFailure(w, err)
			return
		}
		value, err := s.engine.HandoffCall(r.PathValue("session"), agentBearer(r), r.PathValue("operation"), json.RawMessage(raw))
		if err != nil {
			var auth *studio.DeviceError
			if errors.As(err, &auth) {
				s.deviceAdminError(w, r, err)
			} else {
				graphFailure(w, err)
			}
			return
		}
		writeJSON(w, 200, value)
	})
	// The local operator uses the same bounded artifact projections, including for
	// backend-created workflows. No separate frontend registration is needed.
	mux.HandleFunc("POST /api/projects/{project}/runs/{run}/evidence/{operation}", func(w http.ResponseWriter, r *http.Request) {
		var raw json.RawMessage
		if !graphBody(w, r, &raw) {
			return
		}
		value, err := s.engine.RunEvidence(r.PathValue("project"), r.PathValue("run"), r.PathValue("operation"), raw)
		if err != nil {
			graphFailure(w, err)
			return
		}
		writeJSON(w, 200, value)
	})
}
