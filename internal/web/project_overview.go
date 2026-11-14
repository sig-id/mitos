package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"mitos/internal/studio"
)

type projectWorkRow struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Title        string    `json:"title"`
	State        string    `json:"state"`
	Status       string    `json:"status"`
	Agents       int       `json:"agents"`
	ActiveAgents int       `json:"active_agents"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type projectWorkStats struct {
	Open         int `json:"open"`
	Closed       int `json:"closed"`
	Archived     int `json:"archived"`
	Underway     int `json:"underway"`
	ActiveAgents int `json:"active_agents"`
	// No billing totals are recorded by the current execution providers.
	CostUSD *float64 `json:"cost_usd"`
}

func (s *Server) projectOverview(w http.ResponseWriter, r *http.Request) {
	project := r.PathValue("project")
	work, err := s.engine.Desk.Work(project)
	if err != nil {
		deskResult(w, nil, err)
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "archived" {
		writeJSON(w, 400, map[string]string{"error": "state must be open, closed or archived"})
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeJSON(w, 400, map[string]string{"error": "offset must be a nonnegative integer"})
			return
		}
	}
	rows := []projectWorkRow{}
	stats := projectWorkStats{}
	// Aggregate before filtering and pagination so cards always cover the project.
	graphs, _ := s.engine.Store.GraphSummaries(project, "", int(^uint(0)>>1))
	for _, g := range graphs {
		rows = append(rows, projectWorkRow{ID: g.ID, Kind: "graph", Title: g.Title, State: g.WorkState, Status: projectGraphStatus(g), Agents: g.Agents, ActiveAgents: g.ActiveAgents, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt})
		stats.ActiveAgents += g.ActiveAgents
	}
	sessions := map[string]int{}
	for _, session := range work.Sessions {
		// Native sessions are separate from coordinated graph executions.
		if session.Status == "starting" || session.Status == "running" || session.Status == "stopping" {
			stats.ActiveAgents++
			sessions[session.TaskID]++
		}
	}
	for _, task := range work.Tasks {
		state := "open"
		if task.Status == "accepted" || task.Status == "cancelled" {
			state = "closed"
		}
		agents := 0
		if task.CurrentSessionID != "" {
			agents = 1
		}
		rows = append(rows, projectWorkRow{ID: task.ID, Kind: "task", Title: task.Title, State: state, Status: task.Status, Agents: agents, ActiveAgents: sessions[task.ID], CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt})
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	filtered := []projectWorkRow{}
	for _, row := range rows {
		switch row.State {
		case "open":
			stats.Open++
		case "closed":
			stats.Closed++
		case "archived":
			stats.Archived++
		}
		if row.ActiveAgents > 0 {
			stats.Underway++
		}
		if row.State == state && strings.Contains(strings.ToLower(row.Title), query) {
			filtered = append(filtered, row)
		}
	}
	slices.SortFunc(filtered, func(a, b projectWorkRow) int {
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	total := len(filtered)
	offset = min(offset, total)
	end := min(offset+50, total)
	writeJSON(w, 200, map[string]any{"server": s.engine.Store.Home(), "observed_at": time.Now().UTC(), "stats": stats, "work": filtered[offset:end], "total": total, "offset": offset, "next_offset": end})
}

func projectGraphStatus(g studio.GraphSummary) string {
	if g.Counts["stopping"] > 0 {
		return "stopping"
	}
	if g.Status != "active" {
		return g.Status
	}
	for _, state := range []string{"needs_attention", "failed", "waiting_input", "reviewing_approval", "resuming", "running", "review", "blocked", "queued"} {
		if g.Counts[state] > 0 {
			return state
		}
	}
	return "idle"
}
