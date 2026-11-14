package web

import (
	"net/http"

	"mitos/internal/studio"
)

func (s *Server) scheduleWorkflow(w http.ResponseWriter, r *http.Request) {
	var req studio.ScheduledGoal
	if !graphBody(w, r, &req) {
		return
	}
	view, err := s.engine.ScheduleGoal(req)
	if err != nil {
		graphFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}
