package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"mitos/internal/decision"
	"mitos/internal/device"
)

func (s *Server) agentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/agent-sessions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ProjectID     string `json:"project_id"`
			DeskSessionID string `json:"desk_session_id"`
			Adapter       string `json:"adapter"`
			Title         string `json:"title"`
			Workspace     string `json:"workspace"`
		}
		b, err := io.ReadAll(r.Body)
		if err != nil || device.DecodeObject(b, &in, []string{"adapter", "title", "workspace", "project_id", "desk_session_id"}) != nil {
			writeJSON(w, 400, map[string]string{"error": "malformed"})
			return
		}
		workspace, err := s.engine.Desk.ResolveDirectory(in.Workspace)
		if err != nil {
			writeJSON(w, 422, map[string]string{"error": "workspace_unavailable"})
			return
		}
		if in.DeskSessionID != "" {
			existing, err := s.engine.Desk.Session(in.DeskSessionID)
			if err != nil || existing.ProjectID != in.ProjectID || existing.Workspace != workspace || !existing.Bridge {
				writeJSON(w, 409, map[string]string{"error": "session_binding_mismatch"})
				return
			}
		} else if in.ProjectID != "" {
			writeJSON(w, 400, map[string]string{"error": "desk_session_required"})
			return
		}
		session, token, err := s.engine.Store.CreateBoundAgentSession(in.Adapter, in.Title, workspace, in.ProjectID, in.DeskSessionID, "", decision.LocalOperator())
		if err != nil {
			s.deviceAdminError(w, r, err)
			return
		}
		writeJSON(w, 201, map[string]any{"session": session, "token": token})
	})
	mux.HandleFunc("POST /api/agent-sessions/{session}/{action}", func(w http.ResponseWriter, r *http.Request) {
		token := agentBearer(r)
		id := r.PathValue("session")
		var err error
		switch r.PathValue("action") {
		case "end-thread":
			var in struct {
				Thread string `json:"thread"`
			}
			b, readErr := io.ReadAll(r.Body)
			if readErr != nil || device.DecodeObject(b, &in, []string{"thread"}) != nil {
				writeJSON(w, 400, map[string]string{"error": "malformed"})
				return
			}
			err = s.engine.Store.EndAgentThread(id, token, in.Thread)
		case "heartbeat":
			err = s.engine.Store.AgentHeartbeat(id, token)
		case "close":
			err = s.engine.Store.CloseAgentSession(id, token)
		case "requests":
			raw, readErr := io.ReadAll(r.Body)
			if readErr != nil || len(raw) > 24<<10 {
				writeJSON(w, 413, map[string]string{"error": "body_limit"})
				return
			}
			view, err := s.engine.Store.IngestAgentRequest(id, token, raw)
			if err != nil {
				s.deviceAdminError(w, r, err)
				return
			}
			s.engine.QueueAgentResolution(id, token, view.RequestID)
			writeJSON(w, 200, view)
			return
		default:
			writeJSON(w, 404, map[string]string{"error": "unavailable"})
			return
		}
		if err != nil {
			s.deviceAdminError(w, r, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/agent-sessions/{session}/requests/{request}", func(w http.ResponseWriter, r *http.Request) {
		view, err := s.engine.Store.AgentRequestStatus(r.PathValue("session"), agentBearer(r), r.PathValue("request"))
		if err != nil {
			s.deviceAdminError(w, r, err)
			return
		}
		if view.State == "pending" {
			s.engine.QueueAgentResolution(r.PathValue("session"), agentBearer(r), view.RequestID)
		}
		writeJSON(w, 200, view)
	})
	mux.HandleFunc("POST /api/agent-sessions/{session}/requests/{request}/{action}", func(w http.ResponseWriter, r *http.Request) {
		id, token, key := r.PathValue("session"), agentBearer(r), r.PathValue("request")
		var err error
		switch r.PathValue("action") {
		case "route":
			route, err := s.engine.RouteAgentRequest(r.Context(), id, token, key)
			if err != nil {
				s.deviceAdminError(w, r, err)
				return
			}
			writeJSON(w, 200, route)
			return
		case "invalidate":
			err = s.engine.Store.InvalidateAgentRequest(id, token, key)
		case "delivery":
			var in struct {
				Digest string `json:"digest"`
				State  string `json:"state"`
			}
			b, readErr := io.ReadAll(r.Body)
			if readErr != nil || device.DecodeObject(b, &in, []string{"digest", "state"}) != nil {
				writeJSON(w, 400, map[string]string{"error": "malformed"})
				return
			}
			err = s.engine.Store.AgentDelivery(id, token, key, in.Digest, in.State)
		default:
			writeJSON(w, 404, map[string]string{"error": "unavailable"})
			return
		}
		if err != nil {
			s.deviceAdminError(w, r, err)
			return
		}
		writeJSON(w, 200, json.RawMessage(`{"ok":true}`))
	})
}
func agentBearer(r *http.Request) string {
	if len(r.Header.Values("Authorization")) != 1 {
		return ""
	}
	a := r.Header.Get("Authorization")
	if !strings.HasPrefix(a, "Bearer ") || len(a) > 128 {
		return ""
	}
	return strings.TrimPrefix(a, "Bearer ")
}
