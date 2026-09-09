package web

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"mitos/internal/autonomy"
	"mitos/internal/decision"
	"mitos/internal/fleet"
	"mitos/internal/service"
	"mitos/internal/studio"
	"mitos/internal/workpool"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	engine    *studio.Engine
	templates *template.Template
	handler   http.Handler
	demo      bool
}
type page struct {
	ServiceLifecycles                                      []studio.ServiceLifecycleView
	ServiceLifecycle                                       *studio.ServiceLifecycleView
	CustomEvents                                           []studio.CustomEventSummary
	CustomEvent                                            *studio.CustomEventView
	Services                                               *studio.ServiceView
	ServicesJSON                                           string
	Tasks                                                  []studio.Task
	Task                                                   studio.Task
	Pools                                                  []workpool.Pool
	Metrics                                                *studio.AutonomyReport
	Title, Section, View, Error, Query, Filter, Date, Root string
	Runs, Active, Finished, Attention                      []studio.Run
	Run                                                    studio.Run
	Apps                                                   []studio.Application
	App                                                    studio.Application
	Brief                                                  studio.Brief
	Live, Demo                                             bool
	Total, Verified                                        int
	Duration                                               string
}

func New(engine *studio.Engine, demo bool) (*Server, error) {
	funcs := template.FuncMap{
		"requestKey": rand.Text,
		"lines":      func(values []string) string { return strings.Join(values, "\n") },
		"metric": func(value *float64) string {
			if value == nil {
				return "Not measured"
			}
			return fmt.Sprintf("%.2f", *value)
		},
		"metricSeconds": func(value *float64) string {
			if value == nil {
				return "Not measured"
			}
			return fmt.Sprintf("%.2f s", *value/1000)
		},
		"metricPercent": func(value *float64) string {
			if value == nil {
				return "Not measured"
			}
			return fmt.Sprintf("%.1f%%", *value*100)
		},

		"recent": func(r []studio.Run) []studio.Run { return r[:min(5, len(r))] }, "icon": icon, "status": status, "duration": duration, "when": func(t time.Time) string { return t.Local().Format("Jan 2 · 15:04") },
		"short": func(s string) string {
			if len(s) > 15 {
				return s[len(s)-8:]
			}
			return s
		},
		"bytes": func(n int64) string {
			if n < 1024 {
				return fmt.Sprintf("%d B", n)
			}
			if n < 1<<20 {
				return fmt.Sprintf("%.1f KB", float64(n)/1024)
			}
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		},
		"eventTitle": func(s string) string { return strings.ReplaceAll(s, "_", " ") },
		"eventSummary": func(e studio.Event) string {
			var d struct {
				Summary string `json:"summary"`
			}
			_ = json.Unmarshal(e.Data, &d)
			return d.Summary
		},
	}
	t, err := template.New("studio").Funcs(funcs).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s := &Server{engine: engine, templates: t, demo: demo}
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /{$}", s.overview)
	mux.HandleFunc("GET /web", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/", http.StatusSeeOther) })
	mux.HandleFunc("GET /autonomy", func(w http.ResponseWriter, r *http.Request) {
		report, err := studio.ReportAutonomy(s.engine.Store.List())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		page := s.base("autonomy", "Autonomy evidence", "overview")
		page.Live = false
		page.Metrics = &report
		s.render(w, r, page, 200)
	})
	mux.HandleFunc("GET /work", s.work)
	mux.HandleFunc("GET /tasks", s.tasks)
	mux.HandleFunc("GET /services", s.services)
	mux.HandleFunc("GET /api/service-events", s.apiCustomEvents)
	mux.HandleFunc("POST /api/service-events", s.apiEmitEvent)
	mux.HandleFunc("GET /api/service-events/{seq}", s.apiCustomEvent)
	mux.HandleFunc("POST /services/events", s.emitEvent)
	mux.HandleFunc("GET /services/events/{seq}", s.customEvent)
	mux.HandleFunc("GET /api/service-lifecycle", s.apiServiceLifecycles)
	mux.HandleFunc("GET /api/service-lifecycle/{seq}", s.apiServiceLifecycle)
	mux.HandleFunc("POST /api/services/{id}/lifecycle", s.apiServiceLifecycleAction)
	mux.HandleFunc("POST /services/{id}/lifecycle", s.serviceLifecycleAction)
	mux.HandleFunc("GET /services/lifecycle/{seq}", s.serviceLifecycle)
	mux.HandleFunc("GET /api/services", s.apiServices)
	mux.HandleFunc("POST /api/services/preview", s.previewServices)
	mux.HandleFunc("PUT /api/services", s.apiConfigureServices)
	mux.HandleFunc("POST /services/catalog", s.configureServices)
	mux.HandleFunc("POST /api/services/{id}/trigger", s.apiTriggerService)
	mux.HandleFunc("POST /api/services/{id}/signals", s.apiServiceSignal)
	mux.HandleFunc("POST /services/{id}/trigger", s.triggerService)
	mux.HandleFunc("POST /api/runs/{id}/heartbeat", s.apiServiceHeartbeat)
	mux.HandleFunc("GET /api/tasks", s.apiTasks)
	mux.HandleFunc("POST /api/tasks", s.apiLaunch)
	mux.HandleFunc("GET /api/tasks/{id}", s.apiTask)
	mux.HandleFunc("GET /api/pools", s.apiPools)
	mux.HandleFunc("PUT /api/pools/{id}", s.apiConfigurePool)
	mux.HandleFunc("POST /pools/{id}", s.configurePool)
	mux.HandleFunc("GET /decisions", s.decisions)
	mux.HandleFunc("GET /apps", s.apps)
	mux.HandleFunc("GET /apps/{id}", s.application)
	mux.HandleFunc("GET /new", s.newBrief)
	mux.HandleFunc("POST /runs", s.launch)
	mux.HandleFunc("GET /runs/{id}", s.run)
	mux.HandleFunc("GET /runs/{id}/report", s.report)
	mux.HandleFunc("POST /runs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /runs/{id}/retry", s.retry)
	mux.HandleFunc("POST /runs/{id}/decisions/{decision}", s.resolve)
	mux.HandleFunc("GET /runs/{id}/artifacts/{name}", s.artifact)
	mux.HandleFunc("GET /api/autonomy", func(w http.ResponseWriter, r *http.Request) {
		report, err := studio.ReportAutonomy(s.engine.Store.List())
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, report)
	})
	mux.HandleFunc("GET /api/fleet", s.apiFleet)
	mux.HandleFunc("GET /api/runs/{id}/evidence", s.apiEvidence)
	mux.HandleFunc("POST /api/runs/{id}/claims/release", s.apiReleaseClaims)
	mux.HandleFunc("POST /runs/{id}/claims/release", s.releaseClaims)
	mux.HandleFunc("GET /api/interrupts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(studio.ReportInterrupts(s.engine.Store.List()))
	})
	mux.HandleFunc("GET /api/decisions", func(w http.ResponseWriter, r *http.Request) {
		cards := []decision.Card{}
		for _, run := range s.engine.Store.List() {
			if run.Status == "parked" && run.Decision != nil && run.Decision.Choice == "" {
				cards = append(cards, *run.Decision)
			}
		}
		writeJSON(w, 200, cards)
	})
	mux.HandleFunc("GET /api/runs/{id}/decision", func(w http.ResponseWriter, r *http.Request) {
		run, err := s.engine.Store.Get(r.PathValue("id"))
		if err != nil || run.Decision == nil {
			writeJSON(w, 404, map[string]string{"error": "decision not found"})
			return
		}
		writeJSON(w, 200, run.Decision)
	})
	mux.HandleFunc("POST /api/runs/{id}/decisions/{decision}", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Choice string `json:"choice"`
			Note   string `json:"note"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || decoder.Decode(new(any)) != io.EOF {
			writeJSON(w, 400, map[string]string{"error": "send one choice and optional note"})
			return
		}
		via := "api"
		if r.Header.Get("X-Mitos-Client") == "cli" {
			via = "cli"
		}
		result, err := s.engine.ResolveAs(r.PathValue("id"), r.PathValue("decision"), body.Choice, body.Note, via, decision.LocalOperator())
		if err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, map[string]string{"run_id": result, "decision_id": r.PathValue("decision")})
	})
	mux.HandleFunc("GET /api/runs", s.apiRuns)
	mux.HandleFunc("POST /api/runs", s.apiLaunch)
	mux.HandleFunc("GET /api/runs/{id}", s.apiRun)
	mux.HandleFunc("GET /api/runs/{id}/events", s.apiEvents)
	mux.HandleFunc("GET /api/runs/{id}/checkpoints", func(w http.ResponseWriter, r *http.Request) {
		run, err := s.engine.Store.Get(r.PathValue("id"))
		if err != nil {
			writeJSON(w, 404, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 200, run.Checkpoints)
	})
	mux.HandleFunc("POST /api/runs/{id}/rewind", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			CheckpointID  string `json:"checkpoint_id"`
			Scope         string `json:"scope"`
			Memo          string `json:"memo"`
			ReasonSignal  string `json:"reason_signal"`
			FromSegment   *int   `json:"from_segment"`
			AllowNonGreen bool   `json:"allow_non_green"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil || body.FromSegment == nil || *body.FromSegment < 0 || decoder.Decode(new(any)) != io.EOF {
			writeJSON(w, 400, map[string]string{"error": "send one valid rewind request with from_segment"})
			return
		}
		request := autonomy.Rewind{CheckpointID: body.CheckpointID, Scope: body.Scope, Memo: body.Memo, ReasonSignal: body.ReasonSignal, FromSegment: *body.FromSegment, AllowNonGreen: body.AllowNonGreen}
		via := "api"
		if r.Header.Get("X-Mitos-Client") == "cli" {
			via = "cli"
		}
		rewind, err := s.engine.RewindAs(r.PathValue("id"), request, via, decision.LocalOperator())
		if err != nil {
			writeJSON(w, 409, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, 202, rewind)
	})
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		if r.Method == "POST" && pattern == "" && service.ValidWebhookPath(r.URL.Path) {
			s.serviceWebhook(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	s.handler = s.headers(http.NewCrossOriginProtection().Handler(dispatch))
	return s, nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }
func (s *Server) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			http.Error(w, "This Studio is available on localhost only.", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		limit := int64(32 << 10)
		if r.Method == "POST" && (r.URL.Path == "/api/service-events" || service.ValidWebhookPath(r.URL.Path) || strings.HasPrefix(r.URL.Path, "/api/services/") && strings.HasSuffix(r.URL.Path, "/signals")) {
			limit = studio.MaxIngressBytes
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
func (s *Server) base(view, title, section string) page {
	p := page{View: view, Title: title, Section: section, Apps: s.engine.Apps, Root: s.engine.Root, Date: time.Now().Format("Monday, 2 January"), Demo: s.demo}
	p.Pools = s.engine.Store.Pools()
	p.Runs = s.engine.Store.List()
	p.Total = len(p.Runs)
	var elapsed time.Duration
	for _, r := range p.Runs {
		if r.Active() {
			p.Active = append(p.Active, r)
			p.Live = view == "overview" || view == "decisions"
		}
		if r.Status == "parked" && r.Decision != nil && r.Decision.Choice == "" {
			p.Attention = append(p.Attention, r)
		}
		if r.Status == "done" {
			p.Finished = append(p.Finished, r)
			elapsed += r.Duration()
		}
		if r.Verified {
			p.Verified++
		}
	}
	sort.Slice(p.Attention, func(i, j int) bool { return p.Attention[i].UpdatedAt.Before(p.Attention[j].UpdatedAt) })
	p.Duration = duration(elapsed)
	return p
}
func (s *Server) render(w http.ResponseWriter, r *http.Request, p page, code int) {
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, "layout", p); err != nil {
		http.Error(w, "The page could not be rendered.", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write(b.Bytes())
}
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	code := 400
	if errors.Is(err, studio.ErrNotFound) {
		code = 404
	}
	if errors.Is(err, studio.ErrConflict) {
		code = 409
	}
	p := s.base("error", "Something needs attention", "work")
	p.Error = err.Error()
	s.render(w, r, p, code)
}
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(200)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}
func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	p := s.base("overview", "Overview", "overview")
	s.render(w, r, p, 200)
}
func (s *Server) work(w http.ResponseWriter, r *http.Request) {
	p := s.base("work", "Work", "work")
	p.Query = r.URL.Query().Get("q")
	p.Filter = r.URL.Query().Get("status")
	filtered := []studio.Run{}
	for _, run := range p.Runs {
		match := p.Filter == "" || run.Status == p.Filter || (p.Filter == "active" && run.Active())
		if match && strings.Contains(strings.ToLower(run.Brief.Title+" "+run.Brief.Workspace+" "+run.AppName), strings.ToLower(p.Query)) {
			filtered = append(filtered, run)
		}
	}
	p.Runs = filtered
	s.render(w, r, p, 200)
}
func (s *Server) decisions(w http.ResponseWriter, r *http.Request) {
	p := s.base("decisions", "Decisions", "decisions")
	s.render(w, r, p, 200)
}
func (s *Server) apps(w http.ResponseWriter, r *http.Request) {
	p := s.base("apps", "Applications", "apps")
	s.render(w, r, p, 200)
}
func (s *Server) application(w http.ResponseWriter, r *http.Request) {
	app, ok := s.engine.App(r.PathValue("id"))
	if !ok {
		s.fail(w, r, studio.ErrNotFound)
		return
	}
	p := s.base("application", app.Name, "apps")
	p.App = app
	s.render(w, r, p, 200)
}
func (s *Server) newBrief(w http.ResponseWriter, r *http.Request) {
	p := s.base("new", "New work", "work")
	p.Live = false
	p.Brief = studio.Brief{AppID: r.URL.Query().Get("app"), Workspace: s.engine.Root, TimeoutMinutes: 10}
	if p.Brief.AppID == "" {
		p.Brief.AppID = "workspace-inventory"
	}
	s.render(w, r, p, 200)
}
func (s *Server) launch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, errors.New("the brief is too large or could not be read"))
		return
	}
	minutes, _ := strconv.Atoi(r.FormValue("timeout_minutes"))
	b := studio.Brief{Title: r.FormValue("title"), Intent: r.FormValue("intent"), Workspace: r.FormValue("workspace"), AppID: r.FormValue("app_id"), TimeoutMinutes: minutes}
	b.Ownership = fleet.Ownership{Paths: ownershipLines(r.FormValue("owned_paths")), Leases: ownershipLines(r.FormValue("leases"))}
	scheduling, err := formScheduling(r)
	b.Scheduling = scheduling
	var run studio.Run
	if err == nil {
		run, err = s.engine.Launch(b, "")
	}
	if err != nil {
		p := s.base("new", "New work", "work")
		p.Live = false
		p.Brief = b
		p.Error = err.Error()
		s.render(w, r, p, 422)
		return
	}
	redirect(w, r, "/runs/"+run.ID)
}
func (s *Server) run(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.Store.Get(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.base("run", run.Brief.Title, "work")
	p.Run = run
	p.Task, _ = s.engine.Store.Task(run.TaskID)
	p.Live = run.Active()
	s.render(w, r, p, 200)
}
func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.Store.Get(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p := s.base("report", "Work report", "work")
	p.Run = run
	p.Live = false
	s.render(w, r, p, 200)
}
func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.engine.Cancel(id); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/runs/"+id)
}
func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.Retry(r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/runs/"+run.ID)
}
func (s *Server) resolve(w http.ResponseWriter, r *http.Request) {
	id, err := s.engine.Resolve(r.PathValue("id"), r.PathValue("decision"), r.FormValue("choice"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/runs/"+id)
}
func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	b, err := s.engine.Store.Artifact(r.PathValue("id"), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_, _ = w.Write(b)
}
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.engine.Store.List())
}
func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.Store.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, run)
}
func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.Store.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, run.Events)
}
func (s *Server) apiLaunch(w http.ResponseWriter, r *http.Request) {
	var b studio.Brief
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || dec.Decode(new(any)) != io.EOF {
		writeJSON(w, 400, map[string]string{"error": "send a valid JSON brief"})
		return
	}
	run, err := s.engine.Launch(b, "")
	if err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Location", "/api/runs/"+run.ID)
	if r.URL.Path == "/api/tasks" {
		task, err := s.engine.Store.Task(run.TaskID)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Location", "/api/tasks/"+task.ID)
		writeJSON(w, 201, task)
		return
	}
	writeJSON(w, 201, run)
}
func status(s string) string {
	switch s {
	case "running":
		return "In progress"
	case "stopping":
		return "Stopping"
	case "queued":
		return "Queued"
	case "parked":
		return "Needs you"
	case "done":
		return "Complete"
	case "failed":
		return "Failed"
	case "cancelled":
		return "Stopped"
	}
	return s
}
func duration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func icon(name string) template.HTML {
	paths := map[string]string{
		"overview": `<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>`,
		"work":     `<path d="M4 7h16v13H4zM9 7V4h6v3M4 12h16M10 12v3h4v-3"/>`,
		"decision": `<path d="M12 3 2 20h20L12 3Z"/><path d="M12 9v4m0 3v.5"/>`,
		"apps":     `<path d="m12 3 9 5-9 5-9-5 9-5Zm-9 9 9 5 9-5M3 16l9 5 9-5"/>`,
		"arrow":    `<path d="M5 12h14m-5-5 5 5-5 5"/>`,
		"plus":     `<path d="M12 5v14M5 12h14"/>`,
		"check":    `<path d="m5 12 4 4L19 6"/>`,
		"clock":    `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
		"file":     `<path d="M13 3H5v18h14V9l-6-6Zm0 0v6h6M8 13h8M8 17h5"/>`,
		"folder":   `<path d="M3 6h7l2 3h9v11H3V6Z"/>`,
		"search":   `<circle cx="10" cy="10" r="6"/><path d="m15 15 5 5"/>`,
		"link":     `<path d="m10 13 4-4m-6 7-2 2a4 4 0 0 1-6-6l5-5a4 4 0 0 1 6 0m2 0 2-2a4 4 0 0 1 6 6l-5 5a4 4 0 0 1-6 0" transform="translate(1 0)"/>`,
		"download": `<path d="M12 3v12m-4-4 4 4 4-4M4 16v5h16v-5"/>`,
		"stop":     `<rect x="5" y="5" width="14" height="14" rx="2"/>`,
		"retry":    `<path d="M4 10a8 8 0 1 1 0 5M4 4v6h6"/>`,
		"leaf":     `<path d="M5 19C-2 4 15 7 20 3c2 15-8 20-15 16Zm0 0 9-9"/>`,
		"terminal": `<rect x="3" y="4" width="18" height="16" rx="2"/><path d="m7 9 3 3-3 3m6 0h4"/>`,
	}
	return template.HTML(`<svg class="icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">` + paths[name] + `</svg>`)
}
