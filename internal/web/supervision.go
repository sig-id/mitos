package web

import (
	"mitos/internal/desk"
	"net/http"
	"time"
)

func (s *Server) supervisionRoutes(mux *http.ServeMux) {
	m := s.engine.Desk
	mux.HandleFunc("POST /api/projects/{project}/profile-drafts", func(w http.ResponseWriter, r *http.Request) {
		var in desk.ProfileDraftRequest
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.StartProfileDraft(r.PathValue("project"), in)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/projects/{project}/profile-drafts/{draft}", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.ProfileDraft(r.PathValue("project"), r.PathValue("draft"))
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/projects/{project}/profile-drafts/{draft}/output", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		v, err := m.ReadProfileDraftOutput(r.PathValue("project"), r.PathValue("draft"))
		deskResult(w, v, err)
	})
	mux.HandleFunc("POST /api/projects/{project}/profile-drafts/{draft}/cancel", func(w http.ResponseWriter, r *http.Request) {
		deskResult(w, map[string]bool{"ok": true}, m.CancelProfileDraft(r.PathValue("project"), r.PathValue("draft")))
	})
	mux.HandleFunc("POST /api/projects/{project}/profile-drafts/{draft}/start", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Profile  desk.ProjectProfile `json:"profile"`
			Revision int                 `json:"revision"`
		}
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.ReviewAndStartProfileDraft(r.PathValue("project"), r.PathValue("draft"), in.Profile, in.Revision, "http://"+r.Host)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/projects/{project}/supervision", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Supervision(r.PathValue("project"))
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/projects/{project}/profile", func(w http.ResponseWriter, r *http.Request) {
		p, err := m.Project(r.PathValue("project"))
		deskResult(w, p.Profile, err)
	})
	mux.HandleFunc("POST /api/projects/{project}/profile", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Profile  desk.ProjectProfile `json:"profile"`
			Revision int                 `json:"revision"`
		}
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.SaveProfile(r.PathValue("project"), in.Profile, in.Revision)
		deskResult(w, v, err)
	})
	mux.HandleFunc("POST /api/projects/{project}/attention", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Author string `json:"author"`
			Body   string `json:"body"`
			Pause  bool   `json:"pause"`
		}
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.Broadcast(r.PathValue("project"), in.Author, in.Body, in.Pause)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/sessions/{session}/inbox", func(w http.ResponseWriter, r *http.Request) {
		deadline := time.Now().Add(20 * time.Second)
		for {
			v, err := m.Inbox(r.PathValue("session"))
			fresh := false
			for _, notice := range v {
				for _, target := range notice.Targets {
					if target.SessionID == r.PathValue("session") && (target.AcknowledgedAt == nil || notice.ReleasedAt != nil && !target.ReleaseQueued) {
						fresh = true
					}
				}
			}
			if err != nil || fresh || r.URL.Query().Get("wait") != "true" || time.Now().After(deadline) {
				deskResult(w, v, err)
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(300 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("POST /api/sessions/{session}/inbox/{notice}/delivered", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Release bool `json:"release"`
		}
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		if in.Release {
			deskResult(w, map[string]bool{"ok": true}, m.ReleaseDelivery(r.PathValue("notice"), r.PathValue("session")))
			return
		}
		deskResult(w, map[string]bool{"ok": true}, m.NoticeDelivery(r.PathValue("notice"), r.PathValue("session"), "context_queued", "Native adapter returned context; agent acknowledgment is still required."))
	})
	mux.HandleFunc("POST /api/sessions/{session}/inbox/{notice}/acknowledge", func(w http.ResponseWriter, r *http.Request) {
		deskResult(w, map[string]bool{"ok": true}, m.Acknowledge(r.PathValue("notice"), r.PathValue("session")))
	})
	mux.HandleFunc("POST /api/attention/{notice}/release", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Author string `json:"author"`
		}
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		if in.Author == "" {
			in.Author = "operator"
		}
		deskResult(w, map[string]bool{"ok": true}, m.ReleaseNotice(r.PathValue("notice"), in.Author))
	})
}
