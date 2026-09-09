package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"mitos/internal/workpool"
)

func (s *Server) tasks(w http.ResponseWriter, r *http.Request) {
	p := s.base("tasks", "Tasks and scheduling", "work")
	p.Tasks = s.engine.Store.Tasks()
	p.Live = false
	s.render(w, r, p, 200)
}
func (s *Server) apiTasks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.engine.Store.Tasks())
}
func (s *Server) apiTask(w http.ResponseWriter, r *http.Request) {
	t, err := s.engine.Store.Task(r.PathValue("id"))
	if err != nil {
		writeJSON(w, 404, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, t)
}
func (s *Server) apiPools(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.engine.Store.Pools())
}
func (s *Server) apiConfigurePool(w http.ResponseWriter, r *http.Request) {
	var p workpool.Pool
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil || d.Decode(new(any)) != io.EOF || p.ID != r.PathValue("id") {
		writeJSON(w, 400, map[string]string{"error": "send one pool configuration whose ID matches the path"})
		return
	}
	if err := s.engine.Store.ConfigurePool(p); err != nil {
		writeJSON(w, 422, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, p)
}
func (s *Server) configurePool(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, err)
		return
	}
	concurrency, err := strconv.Atoi(r.FormValue("concurrency"))
	if err != nil {
		s.fail(w, r, errors.New("concurrency must be a number"))
		return
	}
	reserved, err := strconv.Atoi(r.FormValue("reserved"))
	if err != nil {
		s.fail(w, r, errors.New("reserved capacity must be a number"))
		return
	}
	p := workpool.Pool{ID: r.PathValue("id"), Discipline: r.FormValue("discipline"), Concurrency: concurrency, Reserved: reserved}
	if err = s.engine.Store.ConfigurePool(p); err != nil {
		s.fail(w, r, err)
		return
	}
	redirect(w, r, "/tasks")
}
func formScheduling(r *http.Request) (workpool.Request, error) {
	result := workpool.Request{Pool: r.FormValue("pool"), Join: workpool.Join{Rule: r.FormValue("join")}}
	for _, id := range ownershipLines(r.FormValue("after_tasks")) {
		result.Deps = append(result.Deps, workpool.Dependency{From: id, Guard: "done"})
	}
	for _, selection := range ownershipLines(r.FormValue("dependency_inputs")) {
		if err := workpool.AddInput(&result.Deps, selection); err != nil {
			return result, err
		}
	}
	if value := r.FormValue("priority"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return result, errors.New("priority must be a number")
		}
		result.Priority = n
	}
	if value := r.FormValue("quorum"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			return result, errors.New("quorum must be a number")
		}
		result.Join.K = n
	}
	if value := r.FormValue("deadline"); value != "" {
		at, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return result, errors.New("task deadline must include a timezone in RFC3339 format")
		}
		result.Deadline = &at
	}
	result.Normalize()
	return result, result.Validate()
}
