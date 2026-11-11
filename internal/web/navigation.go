package web

import (
	"mitos/internal/desk"
	"mitos/internal/studio"
)

// Navigation is shared by every HTML surface, including native session views.
type navigation struct {
	View      string
	Home      studio.HomeServer
	Projects  []desk.Project
	ProjectID string
	Section   string
}

func (s *Server) navigation(section, project string) navigation {
	return navigation{Home: s.engine.Store.Home(), Projects: s.engine.Desk.Projects(), ProjectID: project, Section: section}
}
