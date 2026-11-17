package studio

import (
	"encoding/json"
	"errors"
	"sort"
)

const ReadDefaultLimit = 50
const ReadMaxLimit = 200
const ReadPageBytes = 16 << 20

var ErrInvalidPage = errors.New("invalid_page: after must be nonnegative and limit must be 1..200")
var ErrReadItemTooLarge = errors.New("read_item_too_large: inspect retained evidence offline")

type RunPage struct {
	Runs    []Run  `json:"runs"`
	Next    int64  `json:"next"`
	NextID  string `json:"next_id,omitempty"`
	HasMore bool   `json:"has_more"`
}
type EventPage struct {
	Events  []Event `json:"events"`
	Next    int64   `json:"next"`
	HasMore bool    `json:"has_more"`
}

func validReadPage(after int64, limit int) error {
	if after < 0 || limit < 1 || limit > ReadMaxLimit {
		return ErrInvalidPage
	}
	return nil
}
func runSequence(r *Run) int64 {
	if len(r.Events) > 0 {
		return r.Events[0].Seq
	}
	return 0
}
func boundedRun(r *Run) Run { copy := *r; copy.Events = nil; return cloneRun(&copy) }

// Keep at most limit+1 pointers while selecting the next page. Run bodies and
// event histories are not cloned until they are selected for the bounded reply.
func (s *Store) runPageLocked(view *ScopedStore, after int64, afterID string, limit int) (RunPage, error) {
	if err := validReadPage(after, limit); err != nil {
		return RunPage{}, err
	}
	if view != nil {
		if err := s.authorizeServiceLocked(view.principal, false); err != nil {
			return RunPage{}, err
		}
	}
	selected := make([]*Run, 0, limit+1)
	less := func(a, b *Run) bool {
		sa, sb := runSequence(a), runSequence(b)
		return sa < sb || sa == sb && a.ID < b.ID
	}
	for _, r := range s.runs {
		if view != nil && (runTenant(r) != view.principal.Tenant || r.Brief.Structured == nil) {
			continue
		}
		seq := runSequence(r)
		if seq < after || seq == after && (afterID == "" || r.ID <= afterID) {
			continue
		}
		at := sort.Search(len(selected), func(i int) bool { return less(r, selected[i]) })
		if at > limit {
			continue
		}
		selected = append(selected, nil)
		copy(selected[at+1:], selected[at:])
		selected[at] = r
		if len(selected) > limit+1 {
			selected = selected[:limit+1]
		}
	}
	page := RunPage{Runs: []Run{}, Next: after, NextID: afterID}
	bytes := 0
	for i, r := range selected {
		if i == limit {
			page.HasMore = true
			break
		}
		run := boundedRun(r)
		raw, err := json.Marshal(run)
		if err != nil {
			return RunPage{}, err
		}
		if len(raw) > ReadPageBytes {
			return RunPage{}, ErrReadItemTooLarge
		}
		if bytes+len(raw) > ReadPageBytes {
			page.HasMore = true
			break
		}
		page.Runs = append(page.Runs, run)
		bytes += len(raw)
		page.Next = runSequence(r)
		page.NextID = r.ID
	}
	return page, nil
}
func (s *Store) RunPage(after int64, afterID string, limit int) (RunPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runPageLocked(nil, after, afterID, limit)
}
func (v *ScopedStore) RunPage(after int64, afterID string, limit int) (RunPage, error) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	return v.store.runPageLocked(v, after, afterID, limit)
}
func (a *ApplicationSession) RunPage(after int64, afterID string, limit int) (RunPage, error) {
	return a.view.RunPage(after, afterID, limit)
}

func eventPage(events []Event, after int64, limit int) (EventPage, error) {
	if err := validReadPage(after, limit); err != nil {
		return EventPage{}, err
	}
	page := EventPage{Events: []Event{}, Next: after}
	bytes := 0
	start := sort.Search(len(events), func(i int) bool { return events[i].Seq > after })
	for _, e := range events[start:] {
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		raw, err := json.Marshal(e)
		if err != nil {
			return EventPage{}, err
		}
		if len(raw) > ReadPageBytes {
			return EventPage{}, ErrReadItemTooLarge
		}
		if bytes+len(raw) > ReadPageBytes {
			page.HasMore = true
			break
		}
		page.Events = append(page.Events, graphClone(e))
		page.Next = e.Seq
		bytes += len(raw)
	}
	return page, nil
}
func (s *Store) EventPage(id string, after int64, limit int) (EventPage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil {
		return EventPage{}, ErrNotFound
	}
	return eventPage(r.Events, after, limit)
}
func (v *ScopedStore) EventPage(id string, after int64, limit int) (EventPage, error) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	r, err := v.runLocked(id, false)
	if err != nil {
		return EventPage{}, err
	}
	return eventPage(r.Events, after, limit)
}
func (a *ApplicationSession) EventPage(id string, after int64, limit int) (EventPage, error) {
	return a.view.EventPage(id, after, limit)
}
func (v *ScopedStore) Feed(after int64, limit int) (EventPage, error) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	if err := v.store.authorizeServiceLocked(v.principal, false); err != nil {
		return EventPage{}, err
	}
	return eventPage(v.store.tenantEvents[scopedRequestKey(v.principal.Tenant, "")], after, limit)
}
func (a *ApplicationSession) Feed(after int64, limit int) (EventPage, error) {
	return a.view.Feed(after, limit)
}

func (s *Store) indexApplicationEvent(e Event, c change) {
	var r *Run
	if c.Run != nil {
		r = c.Run
	} else {
		r = s.runs[e.RunID]
	}
	if r == nil || r.Brief.Structured == nil {
		return
	}
	if s.tenantEvents == nil {
		s.tenantEvents = map[string][]Event{}
	}
	key := scopedRequestKey(runTenant(r), "")
	s.tenantEvents[key] = append(s.tenantEvents[key], e)
}

type ApplicationReceipt struct {
	Seq          int64  `json:"seq"`
	TaskID       string `json:"task_id"`
	RunID        string `json:"run_id"`
	Key          string `json:"key"`
	PrincipalID  string `json:"principal_id"`
	RequestHash  string `json:"request_hash"`
	State        string `json:"state"`
	ResultSHA256 string `json:"result_sha256,omitempty"`
}
type ReceiptPage struct {
	Receipts []ApplicationReceipt `json:"receipts"`
	Next     int64                `json:"next"`
	HasMore  bool                 `json:"has_more"`
}

// Scan at most one bounded feed page. Advancing across non-receipt progress
// events keeps empty pages useful and bounds filtering work per request.
func (v *ScopedStore) Receipts(after int64, limit int) (ReceiptPage, error) {
	v.store.mu.RLock()
	defer v.store.mu.RUnlock()
	if err := validReadPage(after, limit); err != nil {
		return ReceiptPage{}, err
	}
	if err := v.store.authorizeServiceLocked(v.principal, false); err != nil {
		return ReceiptPage{}, err
	}
	feed, err := eventPage(v.store.tenantEvents[scopedRequestKey(v.principal.Tenant, "")], after, limit)
	if err != nil {
		return ReceiptPage{}, err
	}
	page := ReceiptPage{Receipts: []ApplicationReceipt{}, Next: feed.Next, HasMore: feed.HasMore}
	for _, e := range feed.Events {
		var c change
		if err = json.Unmarshal(e.Data, &c); err != nil {
			return ReceiptPage{}, err
		}
		r := v.store.runs[e.RunID]
		if r == nil || r.Brief.Structured == nil {
			continue
		}
		state := ""
		if c.Run != nil {
			state = "admitted"
		} else if c.Status == "done" && c.Verified {
			state = "accepted"
		} else if c.Status == "failed" || c.Status == "cancelled" {
			state = c.Status
		}
		if state == "" {
			continue
		}
		receipt := ApplicationReceipt{Seq: e.Seq, TaskID: r.TaskID, RunID: r.ID, Key: r.Brief.Structured.Key, PrincipalID: r.PrincipalID, RequestHash: r.Brief.Structured.RequestHash, State: state}
		if state == "accepted" {
			for _, a := range r.Artifacts {
				if a.Name == structuredResultArtifact {
					receipt.ResultSHA256 = a.SHA256
				}
			}
		}
		page.Receipts = append(page.Receipts, receipt)
	}
	return page, nil
}
func (a *ApplicationSession) Receipts(after int64, limit int) (ReceiptPage, error) {
	return a.view.Receipts(after, limit)
}

func (s *Store) RunDetail(id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil {
		return Run{}, ErrNotFound
	}
	return boundedRun(r), nil
}
