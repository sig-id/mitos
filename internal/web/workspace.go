package web

import (
	"net/http"
	"strconv"
	"time"

	"mitos/internal/desk"
	"mitos/internal/studio"
)

type operatingPage struct {
	Navigation navigation
	Project    desk.Project
	Home       studio.HomeServer
	Section    string
	TaskID     string
}

func (s *Server) operatingPage(w http.ResponseWriter, r *http.Request) {
	p := operatingPage{Home: s.engine.Store.Home(), Section: "overview", TaskID: r.URL.Query().Get("task")}
	if id := r.PathValue("project"); id != "" {
		project, err := s.engine.Desk.Project(id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p.Project = project
		p.Section = "project"
	}
	if r.URL.Path == "/inbox" {
		p.Section = "inbox"
	}
	if r.URL.Path == "/connections" {
		p.Section = "connections"
	}
	p.Navigation = s.navigation(p.Section, p.Project.ID)
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
	_ = s.templates.ExecuteTemplate(w, "operating", p)
}
func (s *Server) workspaceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /projects/{project}", s.operatingPage)
	mux.HandleFunc("GET /inbox", s.operatingPage)
	mux.HandleFunc("GET /connections", s.operatingPage)
	mux.HandleFunc("GET /api/apps", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.engine.Apps) })
	mux.HandleFunc("GET /api/server", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.engine.Store.Home()) })
	mux.HandleFunc("GET /api/workspace-home", func(w http.ResponseWriter, r *http.Request) {
		type summary struct {
			Project desk.Project   `json:"project"`
			Counts  map[string]int `json:"counts"`
			Unbound int            `json:"unbound"`
		}
		type item struct {
			RunID     string `json:"run_id"`
			ProjectID string `json:"project_id"`
			Title     string `json:"title"`
			Question  string `json:"question"`
			TaskID    string `json:"task_id,omitempty"`
		}
		projects := []summary{}
		inbox := []item{}
		for _, p := range s.engine.Desk.Projects() {
			v, err := s.engine.Desk.Work(p.ID)
			if err != nil {
				continue
			}
			row := summary{Project: p, Counts: map[string]int{}}
			for _, t := range v.Tasks {
				row.Counts[t.Status]++
				if t.Status == "reported_complete" || t.Status == "needs_inspection" {
					question := "Review the submitted report against its completion criteria. Settle the executor before accepting."
					if t.Status == "needs_inspection" {
						question = "Inspect and settle the previous process before assigning a replacement."
					}
					inbox = append(inbox, item{ProjectID: p.ID, TaskID: t.ID, Title: t.Title, Question: question})
				}
			}
			for _, session := range v.Sessions {
				if session.TaskID == "" {
					row.Unbound++
				}
			}
			projects = append(projects, row)
		}
		for _, run := range s.engine.Store.List() {
			if run.Decision != nil && run.Decision.ResolvedAt == nil {
				inbox = append(inbox, item{RunID: run.ID, ProjectID: run.Brief.ProjectID, Title: run.Brief.Title, Question: run.Decision.Question})
			}
		}
		writeJSON(w, 200, map[string]any{"server": s.engine.Store.Home(), "observed_at": time.Now().UTC(), "projects": projects, "decisions": inbox})
	})
	mux.HandleFunc("GET /api/projects/{project}/work", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.engine.ProjectWork(r.PathValue("project"))
		if err != nil {
			deskResult(w, nil, err)
			return
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		offset = max(0, min(offset, len(v.Work.Tasks)))
		total := len(v.Work.Tasks)
		end := min(offset+200, total)
		selected := r.URL.Query().Get("task")
		tasks := append([]desk.WorkAssignment{}, v.Work.Tasks[offset:end]...)
		if selected != "" {
			found := false
			for _, t := range tasks {
				if t.ID == selected {
					found = true
				}
			}
			if !found {
				for _, t := range v.Work.Tasks {
					if t.ID == selected {
						tasks = append(tasks, t)
					}
				}
			}
		}
		for i := range tasks {
			t := &tasks[i]
			t.RequestKey = ""
			t.RequestDigest = ""
			if t.ID != selected {
				t.PreviousReports = nil
			}
			if t.Result != nil && t.ID != selected {
				copy := *t.Result
				copy.Body = ""
				t.Result = &copy
			}
		}
		v.Work.Tasks = tasks
		visibleTasks, visibleGoals, currentSessions := map[string]bool{}, map[string]bool{}, map[string]bool{}
		for _, task := range tasks {
			visibleTasks[task.ID], visibleGoals[task.GoalID], currentSessions[task.CurrentSessionID] = true, true, true
		}
		goals := []desk.Goal{}
		for _, goal := range v.Work.Goals {
			if visibleGoals[goal.ID] {
				goals = append(goals, goal)
			}
		}
		v.Work.Goals = goals
		responsibilities := []desk.Responsibility{}
		for _, responsibility := range v.Work.Responsibilities {
			if visibleTasks[responsibility.TaskID] {
				responsibilities = append(responsibilities, responsibility)
			}
		}
		v.Work.Responsibilities = responsibilities
		// Responses contain metadata, never terminal bytes. Old execution history is
		// selected by task; unrelated sessions are limited to the latest 200.
		sessions := []desk.Session{}
		historyCount, unboundCount := 0, 0
		for i := len(v.Work.Sessions) - 1; i >= 0; i-- {
			session := v.Work.Sessions[i]
			include := currentSessions[session.ID]
			if selected != "" && session.TaskID == selected && historyCount < 200 {
				include = true
				historyCount++
			}
			if session.TaskID == "" && unboundCount < 200 {
				include = true
				unboundCount++
			}
			if include {
				// Assignment bodies belong to the selected task, not every status row.
				session.InitialPrompt = ""
				session.Continuation = nil
				sessions = append(sessions, session)
			}
		}
		for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
			sessions[i], sessions[j] = sessions[j], sessions[i]
		}
		v.Work.Sessions = sessions
		writeJSON(w, 200, map[string]any{"server": v.Server, "observed_at": v.ObservedAt, "project": v.Project, "work": v.Work, "batch_tasks": v.BatchTasks, "total": total, "next_offset": end})
	})
	mux.HandleFunc("POST /api/projects/{project}/work", func(w http.ResponseWriter, r *http.Request) {
		var in desk.NewWork
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := s.engine.Desk.CreateWork(r.PathValue("project"), in)
		deskResult(w, v, err)
	})
	mux.HandleFunc("POST /api/projects/{project}/work/{task}/{action}", func(w http.ResponseWriter, r *http.Request) {
		var in desk.WorkAction
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := s.engine.Desk.UpdateWork(r.PathValue("project"), r.PathValue("task"), r.PathValue("action"), in)
		deskResult(w, v, err)
	})
	mux.HandleFunc("POST /api/projects/{project}/responsibilities/{responsibility}/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Body string `json:"body"`
		}
		if err := deskJSON(r, &in); err != nil {
			deskResult(w, nil, err)
			return
		}
		v, err := s.engine.Desk.MessageResponsibility(r.PathValue("project"), r.PathValue("responsibility"), in.Body)
		deskResult(w, v, err)
	})
}
