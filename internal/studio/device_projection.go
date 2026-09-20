package studio

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"mitos/internal/decision"
)

type DeviceOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}
type DeviceCard struct {
	DecisionID       string         `json:"decision_id"`
	RunID            string         `json:"run_id"`
	Revision         string         `json:"revision"`
	Title            string         `json:"title"`
	Question         string         `json:"question"`
	Context          string         `json:"context"`
	Impact           string         `json:"impact"`
	State            string         `json:"state"`
	Input            string         `json:"input"`
	ApplicationError string         `json:"application_error"`
	Options          []DeviceOption `json:"options"`
}
type DeviceRun struct {
	RunID   string `json:"run_id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}
type DeviceSnapshot struct {
	Version      int          `json:"version"`
	PendingTotal int          `json:"pending_total"`
	NextCursor   string       `json:"next_cursor"`
	Cards        []DeviceCard `json:"cards"`
	Runs         []DeviceRun  `json:"runs"`
}

func DeviceText(s string, limit int) bool {
	if len(s) > limit {
		return false
	}
	for _, c := range []byte(s) {
		if c != '\n' && c != '\t' && (c < 32 || c > 126) {
			return false
		}
	}
	return true
}
func deviceCardState(r *Run, c *decision.Card) string {
	if c.SupersededAt != nil {
		return "superseded"
	}
	if c.ApplicationError != "" {
		return "application_blocked"
	}
	if c.AppliedAt != nil {
		return "applied"
	}
	if c.Resolution != nil || c.Choice != "" {
		return "accepted"
	}
	if c.Due(time.Now()) {
		return "expired"
	}
	if r.Decision == nil || r.Decision.ID != c.ID || r.Status != "parked" {
		return "superseded"
	}
	return "pending"
}
func projectDeviceCard(d Device, r *Run, c *decision.Card) (DeviceCard, error) {
	out := DeviceCard{DecisionID: c.ID, RunID: r.ID, Revision: deviceRevision(d, r, c), Title: r.Brief.Title, Question: c.Question, State: deviceCardState(r, c), Input: c.InputProfile(), ApplicationError: c.ApplicationError, Options: []DeviceOption{}}
	out.Context = "Attempted: " + c.Context.Attempted + "\nBlocked by: " + c.Context.BlockedBy + "\nEvidence: " + strings.Join(c.Context.EvidenceRefs, "; ")
	policy := "No automatic default; no expiry."
	if c.Default != nil && c.TTLMS != nil {
		policy = fmt.Sprintf("Default: %s; expiry: %s.", *c.Default, c.Expiry())
	}
	paths, _ := json.Marshal(c.BlastRadius.Paths)
	destinations, _ := json.Marshal(c.BlastRadius.Destinations)
	out.Impact = fmt.Sprintf("Reversibility: %s.\nPaths: %s\nDestinations: %s\nSpend: %g USD.\nCost of wrong: %d ms; %g USD; recoverable: %t. %s\n%s", c.Reversibility, paths, destinations, c.BlastRadius.Spend, c.CostOfWrong.WastedWallMS, c.CostOfWrong.WastedUSD, c.CostOfWrong.Recoverable, c.CostOfWrong.Detail, policy)
	if c.Source != nil {
		// Native requests do not supply a trustworthy effect budget. Zero-valued
		// Go fields must not imply zero spending or no affected paths.
		out.Impact = fmt.Sprintf("Reversibility: %s.\nPaths, destinations and spend: not bounded by the connector; review the full native request.\nCost of wrong: unknown. %s\n%s", c.Reversibility, c.CostOfWrong.Detail, policy)
	}

	if !ValidDeviceID(c.ID) || !ValidDeviceID(r.ID) || len(c.Options) < 2 || len(c.Options) > 3 {
		return out, deviceError(422, "display_unsupported")
	}
	// Validate source path text too: JSON escaping must not conceal unsupported glyphs.
	for _, s := range append(append([]string{}, c.BlastRadius.Paths...), c.BlastRadius.Destinations...) {
		if !DeviceText(s, 2048) {
			return out, deviceError(422, "display_unsupported")
		}
	}
	for _, pair := range []struct {
		s string
		n int
	}{{out.Title, 160}, {out.Question, 800}, {out.Context, 4096}, {out.Impact, 2048}, {out.ApplicationError, 512}} {
		if !DeviceText(pair.s, pair.n) {
			return out, deviceError(422, "display_unsupported")
		}
	}
	seen := map[string]bool{}
	for _, o := range c.Options {
		if !ValidDeviceID(o.ID) || seen[o.ID] || !DeviceText(o.Label, 120) || !DeviceText(o.Consequence, 1024) {
			return out, deviceError(422, "display_unsupported")
		}
		seen[o.ID] = true
		out.Options = append(out.Options, DeviceOption{o.ID, o.Label, o.Consequence})
	}
	return out, nil
}
func deviceRunStatus(status string) (string, bool) {
	switch status {
	case "queued", "running", "blocked", "failed", "cancelled", "interrupted":
		return status, true
	case "parked":
		return "waiting_for_human", true
	case "done":
		return "succeeded", true
	case "stopping":
		return "running", true
	}
	return "", false
}

// A snapshot is a stable view. The HTTP connector paginates this view without
// moving positions when a concurrent decision changes the pending ordering.
func (s *Store) DeviceSnapshot(token string) (DeviceSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := DeviceSnapshot{Version: 1, Cards: []DeviceCard{}, Runs: []DeviceRun{}}
	d, err := s.authenticateDevice(token, "read")
	if err != nil {
		return out, err
	}
	for _, r := range s.runs {
		if r.Sample || runTenant(r) != d.Tenant || r.Decision != nil && r.Decision.Source != nil && s.agentSessions[r.Decision.Source.SessionID].Operator != d.Operator {
			continue
		}
		if r.Terminal() && time.Since(r.UpdatedAt) > 24*time.Hour {
			continue
		}
		status, ok := deviceRunStatus(r.Status)
		if !ok || !ValidDeviceID(r.ID) || !DeviceText(r.Brief.Title, 160) || !DeviceText(r.Summary, 1024) {
			return out, deviceError(422, "display_unsupported")
		}
		out.Runs = append(out.Runs, DeviceRun{r.ID, r.Brief.Title, status, r.Summary})
	}
	for _, c := range s.deviceHistory {
		r := s.runs[c.RunID]
		if r == nil || r.Sample || runTenant(r) != d.Tenant || c.Tenant != d.Tenant || c.Source != nil && s.agentSessions[c.Source.SessionID].Operator != d.Operator {
			continue
		}
		state := deviceCardState(r, &c)
		recent := c.RequestedAt
		if c.Resolution != nil {
			recent = c.Resolution.ResolvedAt
		}
		if c.AppliedAt != nil {
			recent = *c.AppliedAt
		}
		if c.SupersededAt != nil && c.SupersededAt.After(recent) {
			recent = *c.SupersededAt
		}
		if r.UpdatedAt.After(recent) {
			recent = r.UpdatedAt
		}
		if state != "pending" && time.Since(recent) > 24*time.Hour {
			continue
		}
		card, err := projectDeviceCard(d, r, &c)
		if err != nil {
			return out, err
		}
		if state == "pending" {
			out.PendingTotal++
		}
		out.Cards = append(out.Cards, card)
	}
	sort.Slice(out.Cards, func(i, j int) bool {
		a, b := out.Cards[i], out.Cards[j]
		if (a.State == "pending") != (b.State == "pending") {
			return a.State == "pending"
		}
		ca, cb := s.deviceHistory[a.RunID+"/"+a.DecisionID], s.deviceHistory[b.RunID+"/"+b.DecisionID]
		if !ca.RequestedAt.Equal(cb.RequestedAt) {
			return ca.RequestedAt.After(cb.RequestedAt)
		}
		return a.RunID+"/"+a.DecisionID < b.RunID+"/"+b.DecisionID
	})
	sort.Slice(out.Runs, func(i, j int) bool {
		a, b := s.runs[out.Runs[i].RunID], s.runs[out.Runs[j].RunID]
		if a.Terminal() != b.Terminal() {
			return !a.Terminal()
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	return out, nil
}
