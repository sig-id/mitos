package web

import (
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"mitos/internal/desk"
	"mitos/internal/studio"
)

type projectWorkRow struct {
	Revision     int64     `json:"revision"`
	Workspace    string    `json:"workspace"`
	Branch       string    `json:"branch,omitempty"`
	Labels       []string  `json:"labels,omitempty"`
	AttemptMS    *int64    `json:"attempt_ms"`
	CostUSD      *float64  `json:"cost_usd"` // Estimated from journaled harness usage against published list prices.
	UnpricedRuns int       `json:"unpriced_runs,omitempty"`
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
	Open         int      `json:"open"`
	Closed       int      `json:"closed"`
	Archived     int      `json:"archived"`
	Underway     int      `json:"underway"`
	ActiveAgents int      `json:"active_agents"`
	CostUSD      *float64 `json:"cost_usd"`
	UnpricedRuns int      `json:"unpriced_runs,omitempty"`
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
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "graph" && kind != "task" {
		writeJSON(w, 400, map[string]string{"error": "kind must be graph or task"})
		return
	}
	order := r.URL.Query().Get("sort")
	if order != "" && order != "newest" && order != "oldest" && order != "updated" {
		writeJSON(w, 400, map[string]string{"error": "sort must be newest, oldest or updated"})
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
	p, err := s.engine.Desk.Project(project)
	if err != nil {
		deskResult(w, nil, err)
		return
	}
	costs := s.engine.Store.WorkCosts(project)
	now := time.Now().UTC()
	branch := desk.CheckoutBranch(r.Context(), p.Directory)
	rows := []projectWorkRow{}
	stats := projectWorkStats{}
	// Aggregate before filtering and pagination so totals always cover the project.
	graphs, _ := s.engine.Store.GraphSummaries(project, "", int(^uint(0)>>1))
	for _, g := range graphs {
		graphCost := costs.Graphs[g.ID]
		rows = append(rows, projectWorkRow{
			ID: g.ID, Kind: "graph", Title: g.Title, Revision: g.Revision,
			State: g.WorkState, Status: projectGraphStatus(g), Workspace: g.Workspace,
			Agents: g.Agents, ActiveAgents: g.ActiveAgents, AttemptMS: &g.AttemptMS,
			CostUSD: graphCost.CostUSD, UnpricedRuns: graphCost.UnpricedRuns,
			CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
		})
		stats.ActiveAgents += g.ActiveAgents
	}
	sessions := map[string]int{}
	byID := map[string]desk.Session{}
	sessionTime := map[string]time.Duration{}
	unknownTime := map[string]bool{}
	for _, session := range work.Sessions {
		byID[session.ID] = session
		active := session.Status == "starting" || session.Status == "running" || session.Status == "stopping"
		if active {
			stats.ActiveAgents++
			sessions[session.TaskID]++
		}
		end := session.EndedAt
		if end == nil && active {
			end = &now
		}
		if end == nil || session.CreatedAt.IsZero() {
			unknownTime[session.TaskID] = true
			continue
		}
		sessionTime[session.TaskID] += max(0, end.Sub(session.CreatedAt))
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
		session := byID[task.CurrentSessionID]
		var attemptMS *int64
		if !unknownTime[task.ID] {
			ms := sessionTime[task.ID].Milliseconds()
			attemptMS = &ms
		}
		taskCost := costs.Tasks[task.ID]
		rows = append(rows, projectWorkRow{
			ID: task.ID, Kind: "task", Title: task.Title, Revision: int64(task.Revision),
			State: state, Status: task.Status, Workspace: session.Workspace, Branch: session.WorktreeBranch,
			Agents: agents, ActiveAgents: sessions[task.ID], AttemptMS: attemptMS,
			CostUSD: taskCost.CostUSD, UnpricedRuns: taskCost.UnpricedRuns,
			CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt,
		})
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	idQuery := strings.TrimPrefix(query, "#")
	filtered := []projectWorkRow{}
	for _, row := range rows {
		if row.Workspace == "" {
			row.Workspace = p.Directory
		}
		if row.Branch == "" && row.Workspace == p.Directory {
			row.Branch = branch
		}
		row.Labels = work.Labels[row.ID]
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
		if row.State != state || (kind != "" && row.Kind != kind) {
			continue
		}
		_, suffix, _ := strings.Cut(row.ID, "_")
		matchesID := idQuery != "" && (strings.HasPrefix(row.ID, idQuery) || strings.HasPrefix(suffix, idQuery))
		matchesCheckout := strings.Contains(strings.ToLower(row.Branch), query) || strings.Contains(strings.ToLower(filepath.Base(row.Workspace)), query)
		matchesLabel := slices.ContainsFunc(row.Labels, func(name string) bool { return strings.Contains(strings.ToLower(name), query) })
		if strings.Contains(strings.ToLower(row.Title), query) || matchesID || matchesCheckout || matchesLabel {
			filtered = append(filtered, row)
		}
	}
	slices.SortFunc(filtered, func(a, b projectWorkRow) int {
		c := b.CreatedAt.Compare(a.CreatedAt)
		switch order {
		case "oldest":
			c = -c
		case "updated":
			c = b.UpdatedAt.Compare(a.UpdatedAt)
		}
		if c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	total := len(filtered)
	offset = min(offset, total)
	end := min(offset+50, total)
	labelSet := map[string]bool{}
	for _, names := range work.Labels {
		for _, name := range names {
			labelSet[name] = true
		}
	}
	stats.CostUSD = costs.Total.CostUSD
	stats.UnpricedRuns = costs.Total.UnpricedRuns
	names := make([]string, 0, len(labelSet))
	for name := range labelSet {
		names = append(names, name)
	}
	slices.Sort(names)
	writeJSON(w, 200, map[string]any{"labels": names, "server": s.engine.Store.Home(), "observed_at": now, "stats": stats, "work": filtered[offset:end], "total": total, "offset": offset, "next_offset": end})
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

func (s *Server) editOverviewLabels(w http.ResponseWriter, r *http.Request) {
	var edit struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
		Label  string   `json:"label"`
	}
	if err := deskJSON(r, &edit); err != nil {
		deskResult(w, nil, err)
		return
	}
	if len(edit.IDs) == 0 || len(edit.IDs) > 50 {
		writeJSON(w, 400, map[string]string{"error": "select 1–50 work items"})
		return
	}
	project := r.PathValue("project")
	work, err := s.engine.Desk.Work(project)
	if err != nil {
		deskResult(w, nil, err)
		return
	}
	owned := map[string]bool{}
	graphs, _ := s.engine.Store.GraphSummaries(project, "", int(^uint(0)>>1))
	for _, g := range graphs {
		owned[g.ID] = true
	}
	for _, t := range work.Tasks {
		owned[t.ID] = true
	}
	for _, id := range edit.IDs {
		if !owned[id] {
			deskResult(w, nil, desk.ErrNotFound)
			return
		}
	}
	deskResult(w, map[string]bool{"saved": true}, s.engine.Desk.EditWorkLabels(project, edit.IDs, edit.Action, edit.Label))
}
