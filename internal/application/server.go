// Package application is the separately served TLS application boundary. Its
// request handlers receive only a scoped application session, never a raw Store.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"mitos/internal/apiversion"
	"mitos/internal/decision"
	"mitos/internal/studio"
	"mitos/internal/workerapi"
)

type Server struct {
	worker       http.Handler
	authenticate func(string) (decision.Principal, error)
	as           func(decision.Principal) *studio.ApplicationSession
}
type principalKey struct{}

func New(engine *studio.Engine) *Server {
	return &Server{worker: workerapi.Server{Engine: engine}, authenticate: engine.Store.AuthenticateServicePrincipal, as: engine.Application}
}
func Principal(ctx context.Context) (decision.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(decision.Principal)
	return p, ok
}
func reply(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, err error) {
	status, code := 500, "backend_unavailable"
	switch {
	case errors.Is(err, studio.ErrUnauthenticated):
		status, code = 401, "unauthenticated"
	case errors.Is(err, studio.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, studio.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, studio.ErrInputsNotConsumed):
		status, code = 409, "inputs_not_consumed"
	case errors.Is(err, studio.ErrSuperseded):
		status, code = 409, "superseded_context"
	case errors.Is(err, studio.ErrVersionConflict):
		status, code = 409, "version_conflict"
	case errors.Is(err, studio.ErrConflict):
		status, code = 409, "request_conflict"
	case errors.Is(err, studio.ErrTenantCapacity):
		status, code = 429, "tenant_capacity_exceeded"
		w.Header().Set("Retry-After", "1")
	case errors.Is(err, studio.ErrTenantBudget):
		status, code = 422, "tenant_budget_exhausted"
	case errors.Is(err, studio.ErrRequestKeyExpired):
		status, code = 410, "request_key_expired"
	}
	reply(w, status, map[string]string{"error": code})
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/worker/") {
		s.worker.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.TLS == nil {
		reply(w, 403, map[string]string{"error": "tls_required"})
		return
	}
	if version, stated := apiversion.Requested(r); !stated || !apiversion.IsSupported(version) {
		reply(w, 400, map[string]any{"error": "unsupported_api_version", "supported_versions": apiversion.Supported})
		return
	}
	w.Header().Set(apiversion.Header, apiversion.Current)
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || len(auth[0]) != len("Bearer ")+64 {
		fail(w, studio.ErrUnauthenticated)
		return
	}
	p, err := s.authenticate(strings.TrimPrefix(auth[0], "Bearer "))
	if err != nil {
		fail(w, err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	s.serve(w, r, s.as(p))
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, session *studio.ApplicationSession) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if r.Method == "POST" && r.URL.Path == "/api/inputs" {
		var in studio.TaskInputRequest
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
			reply(w, 400, map[string]string{"error": "invalid_input"})
			return
		}
		receipt, err := session.AppendInput("", in)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 201, receipt)
		return
	}

	if len(parts) == 4 && parts[0] == "api" && parts[1] == "tasks" && parts[3] == "inputs" {
		if r.Method == "GET" {
			after, limit, err := pageParameters(r)
			if err != nil {
				reply(w, 400, map[string]string{"error": "invalid_page"})
				return
			}
			page, err := session.Inputs(parts[2], after, limit)
			if err != nil {
				fail(w, err)
				return
			}
			pageHeaders(w, page.Next, "", page.HasMore)
			reply(w, 200, page.Inputs)
			return
		}
		if r.Method == "POST" {
			var in studio.TaskInputRequest
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			if d.Decode(&in) != nil || d.Decode(new(any)) != io.EOF {
				reply(w, 400, map[string]string{"error": "invalid_input"})
				return
			}
			receipt, err := session.AppendInput(parts[2], in)
			if err != nil {
				fail(w, err)
				return
			}
			reply(w, 201, receipt)
			return
		}
	}

	if r.Method == "GET" && r.URL.Path == "/api/capabilities" {
		reply(w, 200, map[string]any{"api_versions": apiversion.Supported, "structured_work": studio.StructuredCapabilities(), "access": "authenticated TLS application listener", "operations": map[string]string{"admit": "POST /api/tasks", "status": "GET /api/runs/{id}", "events": "GET /api/runs/{id}/events", "result": "GET /api/runs/{id}/result", "cancel": "POST /api/runs/{id}/cancel", "decision": "POST /api/runs/{id}/decision"}, "hosted": map[string]bool{"service_principal_auth": true, "tenant_isolation": true, "postgresql": false, "remote_workers": true, "mqtt_work_claims": true, "conversation_waits": false}})
		return
	}
	if r.Method == "POST" && r.URL.Path == "/api/tasks" {
		var brief studio.Brief
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&brief); err != nil || dec.Decode(new(any)) != io.EOF {
			reply(w, 400, map[string]string{"error": "invalid_brief"})
			return
		}
		run, err := session.Admit(brief)
		if err != nil {
			if errors.Is(err, studio.ErrConflict) || errors.Is(err, studio.ErrRequestKeyExpired) || errors.Is(err, studio.ErrUnauthenticated) || errors.Is(err, studio.ErrForbidden) || errors.Is(err, studio.ErrTenantCapacity) || errors.Is(err, studio.ErrTenantBudget) || errors.Is(err, studio.ErrSuperseded) {
				fail(w, err)
			} else {
				reply(w, 422, map[string]string{"error": "invalid_structured_work", "detail": err.Error()})
			}
			return
		}
		task, err := session.Task(run.TaskID)
		if err != nil {
			fail(w, err)
			return
		}
		w.Header().Set("Location", "/api/tasks/"+task.ID)
		reply(w, 201, task)
		return
	}
	if r.Method == "GET" && (r.URL.Path == "/api/runs" || r.URL.Path == "/api/events" || r.URL.Path == "/api/receipts") {
		after, limit, err := pageParameters(r)
		if err != nil {
			reply(w, 400, map[string]string{"error": "invalid_page"})
			return
		}
		if r.URL.Path == "/api/runs" {
			page, err := session.RunPage(after, r.URL.Query().Get("after_id"), limit)
			if err != nil {
				fail(w, err)
				return
			}
			pageHeaders(w, page.Next, page.NextID, page.HasMore)
			reply(w, 200, page.Runs)
		} else if r.URL.Path == "/api/receipts" {
			page, err := session.Receipts(after, limit)
			if err != nil {
				fail(w, err)
				return
			}
			pageHeaders(w, page.Next, "", page.HasMore)
			reply(w, 200, page.Receipts)
		} else {
			page, err := session.Feed(after, limit)
			if err != nil {
				fail(w, err)
				return
			}
			pageHeaders(w, page.Next, "", page.HasMore)
			reply(w, 200, page.Events)
		}
		return
	}
	if len(parts) == 3 && parts[0] == "api" && parts[1] == "tasks" && r.Method == "GET" {
		task, err := session.Task(parts[2])
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, task)
		return
	}
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "runs" {
		id := parts[2]
		if len(parts) == 3 && r.Method == "GET" {
			run, err := session.Get(id)
			if err != nil {
				fail(w, err)
				return
			}
			reply(w, 200, run)
			return
		}
		if len(parts) == 4 && r.Method == "GET" {
			switch parts[3] {
			case "events":
				after, limit, err := pageParameters(r)
				if err != nil {
					reply(w, 400, map[string]string{"error": "invalid_page"})
					return
				}
				page, err := session.EventPage(id, after, limit)
				if err != nil {
					fail(w, err)
					return
				}
				pageHeaders(w, page.Next, "", page.HasMore)
				reply(w, 200, page.Events)
				return
			case "result":
				result, err := session.Result(id)
				if err != nil {
					fail(w, err)
					return
				}
				_, _ = w.Write(result)
				return
			}
		}
		if len(parts) == 4 && parts[3] == "cancel" && r.Method == "POST" {
			if err := session.Cancel(id); err != nil {
				fail(w, err)
				return
			}
			reply(w, 202, map[string]string{"status": "cancellation_requested"})
			return
		}
		if len(parts) == 4 && parts[3] == "decision" && r.Method == "POST" {
			var in struct {
				DecisionID string `json:"decision_id"`
				Choice     string `json:"choice"`
				Note       string `json:"note"`
			}
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF || in.DecisionID == "" || in.Choice == "" {
				reply(w, 400, map[string]string{"error": "invalid_resolution"})
				return
			}
			result, err := session.Resolve(id, in.DecisionID, in.Choice, in.Note)
			if err != nil {
				fail(w, err)
				return
			}
			reply(w, 202, map[string]string{"status": "resolution_applied", "run_id": result})
			return
		}
		if len(parts) == 5 && parts[3] == "artifacts" && r.Method == "GET" {
			data, err := session.Artifact(id, parts[4])
			if err != nil {
				fail(w, err)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(data)
			return
		}
	}
	reply(w, 404, map[string]string{"error": "not_found"})
}

func pageParameters(r *http.Request) (int64, int, error) {
	after := int64(0)
	limit := studio.ReadDefaultLimit
	var err error
	if raw := r.URL.Query().Get("after"); raw != "" {
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, 0, studio.ErrInvalidPage
		}
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, studio.ErrInvalidPage
		}
	}
	if after < 0 || limit < 1 || limit > studio.ReadMaxLimit {
		return 0, 0, studio.ErrInvalidPage
	}
	return after, limit, nil
}
func pageHeaders(w http.ResponseWriter, next int64, id string, more bool) {
	w.Header().Set("Mitos-Next-After", strconv.FormatInt(next, 10))
	w.Header().Set("Mitos-Has-More", strconv.FormatBool(more))
	if id != "" {
		w.Header().Set("Mitos-Next-Run", id)
	}
}
