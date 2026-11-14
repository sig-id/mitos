package studio

import (
	"io/fs"
	"net/url"
	"strings"
	"time"
)

// FinalAnswer is authored for the original goal, separately from review receipts.
type ResultItem struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Task  string `json:"task"`
	Path  string `json:"path"`
	URL   string `json:"url"`
}
type FinalAnswer struct {
	Items       []ResultItem `json:"items,omitempty"`
	Title       string       `json:"title"`
	Markdown    string       `json:"markdown"`
	SourceTasks []string     `json:"source_tasks"`
}
type ResultSource struct {
	Task string `json:"task"`
	Run  string `json:"run"`
}
type GraphResult struct {
	Items     []ResultItem   `json:"items,omitempty"`
	Title     string         `json:"title"`
	Markdown  string         `json:"markdown"`
	Sources   []ResultSource `json:"sources"`
	Author    string         `json:"author"`
	CreatedAt time.Time      `json:"created_at"`
}
type PublishGraphResult struct {
	Revision int64 `json:"revision"`
	FinalAnswer
}

func bindGraphResult(g *WorkGraph, answer FinalAnswer, author string) (*GraphResult, error) {
	if strings.TrimSpace(answer.Title) == "" || len(answer.Title) > 240 || strings.TrimSpace(answer.Markdown) == "" || len(answer.Markdown) > 128<<10 || len(answer.SourceTasks) == 0 || len(answer.SourceTasks) > 64 {
		return nil, graphError("completion needs a bounded final answer to the original goal and accepted worker sources")
	}
	result := &GraphResult{Title: answer.Title, Markdown: answer.Markdown, Author: author, CreatedAt: time.Now().UTC()}
	seen := map[string]bool{}
	for _, id := range answer.SourceTasks {
		m := g.Members[id]
		if m == nil || m.OrchestratorFor != "" || m.Spec.Kind != "application" || m.Accepted == nil || m.Replacement != "" || m.Accepted.RunID == "" || seen[id] {
			return nil, graphError("final answer must cite distinct accepted, unreplaced workers")
		}
		seen[id] = true
		result.Sources = append(result.Sources, ResultSource{Task: id, Run: m.Accepted.RunID})
	}
	if len(answer.Items) > 48 {
		return nil, graphError("too many result items")
	}
	for _, item := range answer.Items {
		if strings.TrimSpace(item.Title) == "" || len(item.Title) > 240 {
			return nil, graphError("result item needs a bounded title")
		}
		switch item.Kind {
		case "file", "changes":
			if !seen[item.Task] || !fs.ValidPath(item.Path) || item.Path == "." || len(item.Path) > 2048 || item.URL != "" {
				return nil, graphError("result file must reference a saved source")
			}
		case "link":
			u, err := url.Parse(item.URL)
			if err != nil || len(item.URL) > 4096 || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || item.Task != "" || item.Path != "" {
				return nil, graphError("result link must be an HTTP URL")
			}
		default:
			return nil, graphError("unknown result item kind")
		}
	}
	result.Items = append([]ResultItem(nil), answer.Items...)
	return result, nil
}

// PublishResult publishes a final answer revision for settled work without rerunning
// agents or changing acceptance. Revision fencing prevents concurrent overwrites.
func (s *Store) PublishResult(id string, req PublishGraphResult) (WorkGraph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.graphs[id]
	if old == nil {
		return WorkGraph{}, ErrNotFound
	}
	if old.Status != "accepted" || old.Revision != req.Revision {
		return WorkGraph{}, ErrConflict
	}
	next := graphClone(*old)
	result, err := bindGraphResult(&next, req.FinalAnswer, "operator")
	if err != nil {
		return WorkGraph{}, err
	}
	next.Result = result
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	if err = s.commit("", "effect", "graph_result_published", change{Graph: &graphChange{Graph: next}}); err != nil {
		return WorkGraph{}, err
	}
	return graphClone(next), nil
}
