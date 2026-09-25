package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"mitos/internal/agentcli"
	"mitos/internal/desk"
)

type deskPage struct {
	Projects    []desk.Project
	Project     desk.Project
	Sessions    []desk.Session
	Session     desk.Session
	Supervising bool
	Root        string
}

func (s *Server) deskPage(w http.ResponseWriter, r *http.Request) {
	// xterm generates dynamic layout styles; scripts remain restricted to local assets.
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
	p := deskPage{Projects: s.engine.Desk.Projects(), Root: s.engine.Root}
	project := r.URL.Query().Get("project")
	if project == "" && len(p.Projects) > 0 {
		project = p.Projects[0].ID
	}
	if project != "" {
		var err error
		p.Project, err = s.engine.Desk.Project(project)
		if err != nil {
			http.Error(w, "Project not found", 404)
			return
		}
	}
	p.Sessions = s.engine.Desk.Sessions(project)
	if id := r.URL.Query().Get("session"); id != "" {
		var err error
		p.Session, err = s.engine.Desk.Session(id)
		if err != nil || p.Session.ProjectID != project {
			http.Error(w, "Session not found in this project", 404)
			return
		}
	} else if len(p.Sessions) > 0 {
		p.Session = p.Sessions[len(p.Sessions)-1]
		for _, candidate := range p.Sessions {
			if candidate.Role == "coordinator" {
				p.Session = candidate
			}
		}
	}
	p.Supervising = r.URL.Query().Get("session") == "" || p.Session.Role == "coordinator"
	if r.URL.Query().Get("view") == "terminal" {
		p.Supervising = false
	}
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, "desk", p); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}
func deskJSON(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected exactly one JSON object")
	}
	return nil
}
func deskResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		code := 400
		if errors.Is(err, desk.ErrNotFound) {
			code = 404
		}
		if errors.Is(err, desk.ErrConflict) {
			code = 409
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, v)
}
func (s *Server) deskRoutes(mux *http.ServeMux) {
	m := s.engine.Desk
	workspace := func(r *http.Request) (string, error) {
		if id := r.URL.Query().Get("project"); id != "" {
			p, err := m.Project(id)
			return p.Directory, err
		}
		return s.engine.Root, nil
	}
	mux.HandleFunc("GET /api/agents", func(w http.ResponseWriter, r *http.Request) {
		root, err := workspace(r)
		if err != nil {
			deskResult(w, nil, err)
			return
		}
		deskResult(w, m.Agents.List(root), nil)
	})
	mux.HandleFunc("GET /api/agents/{agent}", func(w http.ResponseWriter, r *http.Request) {
		root, err := workspace(r)
		if err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.Agents.Inspect(r.PathValue("agent"), r.URL.Query().Get("profile"), root)
		deskResult(w, v, err)
	})
	mux.HandleFunc("POST /api/agents/refresh", func(w http.ResponseWriter, r *http.Request) {
		m.Agents.Refresh(r.Context())
		deskResult(w, m.Agents.List(s.engine.Root), nil)
	})
	mux.HandleFunc("GET /workspace", s.deskPage)
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) { deskResult(w, m.Projects(), nil) })
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name      string `json:"name"`
			Directory string `json:"directory"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.CreateProject(b.Name, b.Directory)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/projects/{project}/sessions", func(w http.ResponseWriter, r *http.Request) {
		if _, err := m.Project(r.PathValue("project")); err != nil {
			deskResult(w, nil, err)
			return
		}
		deskResult(w, m.Sessions(r.PathValue("project")), nil)
	})
	mux.HandleFunc("POST /api/projects/{project}/sessions", func(w http.ResponseWriter, r *http.Request) {
		var b agentcli.Request
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.StartAgent(r.PathValue("project"), b, "http://"+r.Host)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/sessions/{session}", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Session(r.PathValue("session"))
		deskResult(w, v, err)
	})
	for _, kind := range []string{"resume", "fork", "handover"} {
		mux.HandleFunc("POST /api/sessions/{session}/"+kind, func(w http.ResponseWriter, r *http.Request) {
			var b desk.ContinuationRequest
			if err := deskJSON(r, &b); err != nil {
				deskResult(w, nil, err)
				return
			}
			v, err := m.Continue(r.PathValue("session"), kind, b, "http://"+r.Host)
			deskResult(w, v, err)
		})
	}
	mux.HandleFunc("POST /api/sessions/{session}/native-session", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			NativeSessionID string `json:"native_session_id"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.LinkNativeSession(r.PathValue("session"), b.NativeSessionID)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/sessions/{session}/output", func(w http.ResponseWriter, r *http.Request) {
		offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil {
			deskResult(w, nil, errors.New("provide an output offset"))
			return
		}
		v, err := m.ReadOutput(r.PathValue("session"), offset)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/sessions/{session}/stream", s.terminalStream)
	mux.HandleFunc("POST /api/sessions/{session}/input", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Key  string `json:"key"`
			Data string `json:"data"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		deskResult(w, map[string]bool{"accepted": true}, m.Input(r.PathValue("session"), b.Key, b.Data))
	})
	mux.HandleFunc("POST /api/sessions/{session}/prompt", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Key       string `json:"key"`
			Text      string `json:"text"`
			Bracketed bool   `json:"bracketed"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		err := m.Prompt(r.PathValue("session"), b.Key, b.Text, b.Bracketed)
		deskResult(w, map[string]bool{"accepted": err == nil}, err)
	})
	mux.HandleFunc("POST /api/sessions/{session}/resize", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Rows uint16 `json:"rows"`
			Cols uint16 `json:"cols"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		deskResult(w, map[string]bool{"resized": true}, m.Resize(r.PathValue("session"), b.Rows, b.Cols))
	})
	mux.HandleFunc("POST /api/sessions/{session}/stop", func(w http.ResponseWriter, r *http.Request) {
		deskResult(w, map[string]bool{"stopped": true}, m.Stop(r.PathValue("session")))
	})
	mux.HandleFunc("POST /api/sessions/{session}/scratch/clean", func(w http.ResponseWriter, r *http.Request) {
		deskResult(w, map[string]bool{"removed": true}, m.CleanScratch(r.PathValue("session")))
	})
	mux.HandleFunc("POST /api/sessions/{session}/settle", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Confirmed bool `json:"confirmed_stopped"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		deskResult(w, map[string]bool{"settled": true}, m.Settle(r.PathValue("session"), b.Confirmed))
	})
	mux.HandleFunc("GET /api/projects/{project}/topics", func(w http.ResponseWriter, r *http.Request) {
		deskResult(w, m.Topics(r.PathValue("project"), r.URL.Query().Get("q"), r.URL.Query().Get("follower")), nil)
	})
	mux.HandleFunc("POST /api/projects/{project}/topics", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Title   string   `json:"title"`
			Summary string   `json:"summary"`
			Paths   []string `json:"paths"`
			Author  string   `json:"author"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		if b.Author == "" {
			b.Author = "operator"
		}
		v, err := m.CreateTopic(r.PathValue("project"), b.Author, b.Title, b.Summary, b.Paths)
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/topics/{topic}", func(w http.ResponseWriter, r *http.Request) {
		after := 0
		if r.URL.Query().Has("after") {
			var err error
			after, err = strconv.Atoi(r.URL.Query().Get("after"))
			if err != nil {
				deskResult(w, nil, err)
				return
			}
		}
		v, err := m.Topic(r.PathValue("topic"), after)
		deskResult(w, v, err)
	})
	mux.HandleFunc("POST /api/topics/{topic}/posts", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Body     string `json:"body"`
			Author   string `json:"author"`
			Revision int    `json:"revision"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		if b.Author == "" {
			b.Author = "operator"
		}
		v, err := m.Post(r.PathValue("topic"), b.Author, b.Body, b.Revision)
		deskResult(w, v, err)
	})
	mux.HandleFunc("POST /api/topics/{topic}/follow", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Session string `json:"session"`
			Cursor  int    `json:"cursor"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		if b.Session == "" {
			b.Session = "operator"
		}
		deskResult(w, map[string]bool{"following": true}, m.Follow(r.PathValue("topic"), b.Session, b.Cursor))
	})
	mux.HandleFunc("GET /api/projects/{project}/resources", func(w http.ResponseWriter, r *http.Request) {
		v, err := m.Resources(r.Context(), r.PathValue("project"), r.URL.Query().Get("details") == "true")
		deskResult(w, v, err)
	})
	mux.HandleFunc("GET /api/projects/{project}/jobs", func(w http.ResponseWriter, r *http.Request) { deskResult(w, m.Jobs(r.PathValue("project")), nil) })
	mux.HandleFunc("POST /api/projects/{project}/jobs", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Author string   `json:"author"`
			Args   []string `json:"args"`
		}
		if err := deskJSON(r, &b); err != nil {
			deskResult(w, nil, err)
			return
		}
		if b.Author == "" {
			b.Author = "operator"
		}
		v, err := m.Build(r.PathValue("project"), b.Author, b.Args)
		deskResult(w, v, err)
	})
	for _, action := range []string{"join", "release"} {
		mux.HandleFunc("POST /api/jobs/{job}/"+action, func(w http.ResponseWriter, r *http.Request) {
			var b struct {
				Author    string `json:"author"`
				Confirmed bool   `json:"confirmed_stopped"`
			}
			if err := deskJSON(r, &b); err != nil {
				deskResult(w, nil, err)
				return
			}
			if b.Author == "" {
				b.Author = "operator"
			}
			if strings.HasSuffix(r.URL.Path, "/join") {
				v, err := m.Join(r.PathValue("job"), b.Author)
				deskResult(w, v, err)
			} else {
				deskResult(w, map[string]bool{"released": true}, m.Release(r.PathValue("job"), b.Author, b.Confirmed))
			}
		})
	}
	mux.HandleFunc("GET /api/jobs/{job}/output", func(w http.ResponseWriter, r *http.Request) {
		offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		if err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := m.ReadOutput(r.PathValue("job"), offset)
		deskResult(w, v, err)
	})
}
