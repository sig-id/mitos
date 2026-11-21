package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"mitos/internal/studio"
	"mitos/internal/watch"
)

// watchRoutes expose watches (spec 09 §6). The operator creates watches for
// any owner; responsibility and attempt owners act through granted tools.
func (s *Server) watchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/watch-inboxes/{kind}/{id}", func(w http.ResponseWriter, r *http.Request) {
		owner := watch.Owner{Kind: r.PathValue("kind"), ID: r.PathValue("id")}
		if owner.Kind != watch.OwnerResponsibility && owner.Kind != watch.OwnerAttempt {
			writeJSON(w, 400, map[string]string{"error": "owner kind must be responsibility or attempt"})
			return
		}
		writeJSON(w, 200, s.engine.Store.WatchInbox(owner))
	})
	mux.HandleFunc("POST /api/watches", func(w http.ResponseWriter, r *http.Request) {
		var spec watch.Spec
		if !watchBody(w, r, &spec) {
			return
		}
		v, err := s.engine.Store.CreateWatch(spec)
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 201, v)
	})
	mux.HandleFunc("GET /api/watches", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, s.engine.Store.Watches())
	})
	mux.HandleFunc("GET /api/watches/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.Watch(r.PathValue("id"))
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/watches/{id}/renew", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ExpiresAt time.Time `json:"expires_at"`
		}
		if !watchBody(w, r, &in) {
			return
		}
		v, err := s.engine.Store.RenewWatch(r.PathValue("id"), in.ExpiresAt)
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/watches/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := s.engine.Store.CancelWatch(r.PathValue("id")); err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"state": "cancelled"})
	})
	mux.HandleFunc("GET /api/watches/{id}/deliveries", func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 500 {
				writeJSON(w, 400, map[string]string{"error": "limit must be 1–500"})
				return
			}
			limit = n
		}
		v, err := s.engine.Store.WatchDeliveries(r.PathValue("id"), limit)
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/watches/{id}/items", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.WatchItems(r.PathValue("id"))
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/watches/{id}/triage", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.TriageRequests(r.PathValue("id"))
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("GET /api/watches/{id}/report", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.Store.WatchReport(r.PathValue("id"))
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/deliveries/{id}/outcome", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Outcome string `json:"outcome"` // acted, dismissed or deferred; the owner settles its own delivery
			Mark    string `json:"mark"`    // should_have_woken or needless_wake; operator calibration
		}
		if !watchBody(w, r, &in) {
			return
		}
		v, err := s.engine.Store.RecordDeliveryMark(r.PathValue("id"), in.Outcome, in.Mark)
		if err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, v)
	})
	mux.HandleFunc("POST /api/watch-items/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		if err := s.engine.Store.ResolveWatchItem(r.PathValue("id")); err != nil {
			watchError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"state": "resolved"})
	})
}

// watchBody decodes a bounded JSON body and refuses unknown fields: a watch
// field this runtime does not understand must not be silently ignored.
func watchBody(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid watch request: " + err.Error()})
		return false
	}
	return true
}

func watchError(w http.ResponseWriter, err error) {
	code := 400
	switch {
	case errors.Is(err, studio.ErrNotFound):
		code = 404
	case errors.Is(err, studio.ErrConflict):
		code = 409
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
