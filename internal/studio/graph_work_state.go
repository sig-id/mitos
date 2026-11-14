package studio

import "time"

// WorkStateValue keeps inbox organization separate from execution outcomes.
// Older graphs and newly completed work need no migration to leave the open list.
func (g WorkGraph) WorkStateValue() string {
	if g.WorkState != "" {
		return g.WorkState
	}
	if g.Status == "accepted" || g.Status == "cancelled" {
		return "closed"
	}
	return "open"
}

type GraphWorkStateEdit struct {
	Key      string `json:"key"`
	Revision int64  `json:"revision"`
	State    string `json:"state"`
}

// SetGraphWorkState journals organization and cancellation together. Closing or
// archiving is available even after execution deadlines or allowances expire.
// Reopening restores visibility; it never reruns completed or stopped agents.
func (s *Store) SetGraphWorkState(id string, req GraphWorkStateEdit) (WorkGraph, error) {
	if !validGraphKey(req.Key) || (req.State != "open" && req.State != "closed" && req.State != "archived") {
		return WorkGraph{}, graphError("work state must be open, closed or archived and needs a request key")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.graphs[id]
	if old == nil {
		return WorkGraph{}, ErrNotFound
	}
	digest := graphDigest(struct {
		Action string
		Edit   GraphWorkStateEdit
	}{"work_state", req})
	if receipt, ok := old.Receipts[req.Key]; ok {
		if receipt.Digest != digest {
			return WorkGraph{}, ErrConflict
		}
		return graphClone(*old), nil
	}
	if req.Revision != old.Revision {
		return WorkGraph{}, ErrConflict
	}
	next := graphClone(*old)
	next.WorkState = req.State
	if req.State == "open" && next.Status == "active" {
		next.WorkState = ""
	}
	if req.State != "open" && next.Status == "active" {
		next.Status = "cancelled"
		for _, member := range next.Members {
			if member.Accepted == nil {
				member.CancelRequested = true
			}
		}
	}
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	next.Receipts[req.Key] = GraphReceipt{Key: req.Key, Digest: digest, Revision: next.Revision, At: next.UpdatedAt}
	gc := graphChange{Graph: next}
	s.projectGraph(&gc, old)
	if err := s.commit("", "effect", "graph_work_state_changed", change{Graph: &gc}); err != nil {
		return WorkGraph{}, err
	}
	return graphClone(next), nil
}
