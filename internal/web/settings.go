package web

import (
	"bytes"
	"mitos/internal/studio"
	"net/http"
)

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	if project != "" {
		if _, err := s.engine.Desk.Project(project); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	p := struct{ Navigation navigation }{s.navigation("settings", project)}
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, "settings", p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}

func (s *Server) configurationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", s.settingsPage)
	for _, path := range []string{"/api/settings", "/api/projects/{project}/settings"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			project := r.PathValue("project")
			if project != "" {
				if _, err := s.engine.Desk.Project(project); err != nil {
					deskResult(w, nil, err)
					return
				}
			}
			config, global := s.engine.Store.ConfigurationSnapshot(project)
			writeJSON(w, 200, map[string]any{"configuration": config, "defaults": global})
		})
		mux.HandleFunc("PUT "+path, func(w http.ResponseWriter, r *http.Request) {
			var config studio.SystemConfiguration
			if !graphBody(w, r, &config) {
				return
			}
			if project := r.PathValue("project"); project != "" {
				if _, err := s.engine.Desk.Project(project); err != nil {
					deskResult(w, nil, err)
					return
				}
				p := s.engine.Store.AgentPolicy(project)
				if p.Revision != config.Revision {
					graphFailure(w, studio.ErrConflict)
					return
				}
				p.DecisionProvider, p.DecisionApplication, p.AgentLaunch = config.DecisionProvider, config.DecisionApplication, config.Agents
				saved, err := s.engine.SaveAgentPolicy(project, p)
				if err != nil {
					graphFailure(w, err)
					return
				}
				config.Revision = saved.Revision
			} else {
				var err error
				config, err = s.engine.SaveSystemConfiguration(config)
				if err != nil {
					graphFailure(w, err)
					return
				}
			}
			writeJSON(w, 200, config)
		})
	}
	mux.HandleFunc("POST /api/settings/decision-provider/test", s.testDecisionProvider)
}
