package studio

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"mitos/internal/watch"
)

// Watch is a runtime-evaluated subscription over the journal (spec 09). Its
// open groups are a pure fold of the journal: replay rebuilds exactly the
// admitted-but-undelivered events, so a restart between admission and its
// delivery receipt loses nothing and delivers nothing twice.
type Watch struct {
	ID            string                  `json:"id"`
	Spec          watch.Spec              `json:"spec"`
	State         string                  `json:"state"` // active, expired, cancelled
	CreatedAt     time.Time               `json:"created_at"`
	CreatedSeq    int64                   `json:"created_seq"`
	Renewals      int                     `json:"renewals"`
	EndedAt       *time.Time              `json:"ended_at,omitempty"`
	Groups        map[string]*watch.Group `json:"-"`
	LastDelivered map[string]time.Time    `json:"-"`
	LastActivity  time.Time               `json:"-"`
	Silent        bool                    `json:"-"`
}

// WatchDelivery is one receipt. Wake and escalate deliveries for the operator
// are Notifications items; repeated ones coalesce into the open item for the
// same watch and group key until it is resolved or repeat_interval_ms passes.
type WatchDelivery struct {
	ID         string         `json:"id"`
	WatchID    string         `json:"watch_id"`
	Owner      watch.Owner    `json:"owner"`
	Kind       string         `json:"kind"` // group, expired, silent
	Level      watch.Level    `json:"level"`
	GroupKey   string         `json:"group_key,omitempty"`
	FirstSeq   int64          `json:"first_seq,omitempty"`
	LastSeq    int64          `json:"last_seq,omitempty"`
	Seqs       []int64        `json:"seqs,omitempty"`
	Count      int            `json:"count"`
	Counts     map[string]int `json:"counts,omitempty"`
	Reasons    []string       `json:"reasons,omitempty"`
	Watches    []string       `json:"watches,omitempty"`
	Runs       []string       `json:"runs,omitempty"`
	At         time.Time      `json:"at"`
	ItemID     string         `json:"item_id,omitempty"`
	Supersedes string         `json:"supersedes,omitempty"`
	// Item state for deliveries that opened a Notifications item.
	ItemState  string     `json:"item_state,omitempty"` // open, resolved, superseded
	Updates    int        `json:"updates,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

type watchChange struct {
	Create   *Watch         `json:"create,omitempty"`
	Renew    *watchRenewal  `json:"renew,omitempty"`
	Delivery *WatchDelivery `json:"delivery,omitempty"`
	End      *watchEnd      `json:"end,omitempty"`
	Resolve  *watchResolve  `json:"resolve,omitempty"`
}
type watchRenewal struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
}
type watchEnd struct {
	ID    string `json:"id"`
	State string `json:"state"`
}
type watchResolve struct {
	DeliveryID string `json:"delivery_id"`
}

const maxOperatorWatches = 256

var errWatchLimit = errors.New("operator watch limit reached")

func (s *Store) applyWatch(e Event, c *watchChange) error {
	if s.watches == nil {
		s.watches = map[string]*Watch{}
		s.watchDeliveries = map[string]*WatchDelivery{}
		s.watchOrder = map[string][]string{}
		s.watchItems = map[string]string{}
	}
	if w := c.Create; w != nil {
		if s.watches[w.ID] != nil {
			return errors.New("duplicate watch")
		}
		saved := *w
		saved.Groups, saved.LastDelivered = map[string]*watch.Group{}, map[string]time.Time{}
		saved.CreatedSeq, saved.LastActivity = e.Seq, e.At
		s.watches[w.ID] = &saved
	}
	if r := c.Renew; r != nil {
		w := s.watches[r.ID]
		if w == nil || w.State != "active" {
			return errors.New("renewal of inactive watch")
		}
		w.Spec.Lease.ExpiresAt = r.ExpiresAt
		w.Renewals++
	}
	if d := c.Delivery; d != nil {
		if err := s.applyWatchDelivery(e, d); err != nil {
			return err
		}
	}
	if end := c.End; end != nil {
		w := s.watches[end.ID]
		if w == nil || w.State != "active" {
			return errors.New("end of inactive watch")
		}
		at := e.At
		w.State, w.EndedAt, w.Groups = end.State, &at, map[string]*watch.Group{}
	}
	if r := c.Resolve; r != nil {
		d := s.watchDeliveries[r.DeliveryID]
		if d == nil || d.ItemState != "open" {
			return errors.New("resolution of a closed watch item")
		}
		at := e.At
		d.ItemState, d.ResolvedAt = "resolved", &at
		delete(s.watchItems, d.WatchID+"\x00"+d.GroupKey)
	}
	return nil
}

func (s *Store) applyWatchDelivery(e Event, d *WatchDelivery) error {
	w := s.watches[d.WatchID]
	if w == nil || w.State != "active" || s.watchDeliveries[d.ID] != nil {
		return errors.New("delivery for inactive watch")
	}
	saved := *d
	saved.At = e.At
	if d.Kind == "group" {
		delivered := map[int64]bool{}
		for _, seq := range d.Seqs {
			delivered[seq] = true
		}
		// One owner receives an event once: drop it from every watch of that owner.
		for _, other := range s.watches {
			if other.State != "active" || other.Spec.Owner.Key() != d.Owner.Key() {
				continue
			}
			for key, g := range other.Groups {
				g.Entries = slices.DeleteFunc(g.Entries, func(x watch.Entry) bool { return delivered[x.Seq] })
				if len(g.Entries) == 0 {
					delete(other.Groups, key)
				}
			}
		}
		w.LastDelivered[d.GroupKey] = e.At
	}
	if d.Kind == "silent" {
		w.Silent = true
	}
	itemKey := d.WatchID + "\x00" + d.GroupKey
	switch {
	case d.ItemID != "":
		item := s.watchDeliveries[d.ItemID]
		if item == nil || item.ItemState != "open" {
			return errors.New("coalescing into a closed watch item")
		}
		item.Updates++
		item.Count += d.Count
		item.LastSeq = max(item.LastSeq, d.LastSeq)
		item.Level = watch.Max(item.Level, d.Level)
		for k, n := range d.Counts {
			if item.Counts == nil {
				item.Counts = map[string]int{}
			}
			item.Counts[k] += n
		}
	case d.ItemState == "open":
		if d.Supersedes != "" {
			if old := s.watchDeliveries[d.Supersedes]; old != nil && old.ItemState == "open" {
				old.ItemState = "superseded"
			}
		}
		s.watchItems[itemKey] = d.ID
	}
	s.watchDeliveries[d.ID] = &saved
	s.watchOrder[d.WatchID] = append(s.watchOrder[d.WatchID], d.ID)
	return nil
}

// watchRule assigns the runtime-owned must-deliver level for a run event.
func watchRule(e Event, r *Run, c change) (watch.Level, string) {
	switch c.Status {
	case "done", "failed", "cancelled":
		return watch.Notify, "run_" + c.Status
	case "parked":
		if c.Judgment != nil {
			// The judgment already has its operator card; notify the owner only.
			return watch.Notify, "judgment_parked"
		}
		if r.Decision != nil && (strings.HasPrefix(r.Decision.ReasonSignal, "decision_model_") || r.Decision.ReasonSignal == "model_call_limit") {
			return watch.Wake, "model_decision_parked"
		}
		return watch.Notify, "run_parked"
	}
	switch e.Kind {
	case "blocked_on_human":
		return watch.Wake, "blocked_on_human"
	case "observation_failed":
		return watch.Escalate, "observation_failed"
	case "quiescent":
		var edge struct {
			Threshold string `json:"threshold"`
		}
		if json.Unmarshal(c.Evidence, &edge) == nil && edge.Threshold == "block" {
			return watch.Wake, "quiescence_block"
		}
	}
	return "", ""
}

func (s *Store) runFact(e Event, r *Run, c change) watch.Fact {
	f := watch.Fact{Seq: e.Seq, At: e.At, Project: r.ProjectID, Task: r.TaskID, Run: r.ID, Type: e.Type, Kind: e.Kind}
	if r.GraphAttempt != nil {
		f.Graph = r.GraphAttempt.GraphID
		if f.Project == "" && s.graphs[f.Graph] != nil {
			f.Project = s.graphs[f.Graph].ProjectID
		}
	}
	f.Rule, f.Reason = watchRule(e, r, c)
	return f
}

// observeWatches folds one fact into every active watch. It runs inside apply,
// both live and during replay.
func (s *Store) observeWatches(f watch.Fact) {
	for _, w := range s.watches {
		if w.State != "active" || f.Seq <= w.CreatedSeq {
			continue
		}
		level, ok := w.Spec.Admit(f)
		if !ok {
			continue
		}
		key := w.Spec.Grouping.GroupKey(f)
		g := w.Groups[key]
		if g == nil {
			g = &watch.Group{Key: key}
			w.Groups[key] = g
		}
		g.Entries = append(g.Entries, watch.Entry{Seq: f.Seq, At: f.At, Run: f.Run, Kind: f.Kind, Level: level, Reason: f.Reason})
		w.LastActivity, w.Silent = f.At, false
	}
}

// CreateWatch validates and records an operator watch.
func (s *Store) CreateWatch(spec watch.Spec) (Watch, error) {
	now := time.Now().UTC()
	if err := spec.Validate(now); err != nil {
		return Watch{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	active := 0
	for _, w := range s.watches {
		if w.State == "active" && w.Spec.Owner.Key() == spec.Owner.Key() {
			active++
		}
	}
	if active >= maxOperatorWatches {
		return Watch{}, errWatchLimit
	}
	spec.Lease.ExpiresAt = spec.Lease.ExpiresAt.UTC()
	w := Watch{ID: newID("watch"), Spec: spec, State: "active", CreatedAt: now}
	if err := s.commit("", "effect", "watch_created", change{Watch: &watchChange{Create: &w}}); err != nil {
		return Watch{}, err
	}
	return s.watchView(s.watches[w.ID]).Watch, nil
}

func (s *Store) RenewWatch(id string, expiresAt time.Time) (Watch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.watches[id]
	if w == nil {
		return Watch{}, ErrNotFound
	}
	if w.State != "active" || w.Renewals >= w.Spec.Lease.MaxRenewals {
		return Watch{}, ErrConflict
	}
	if err := watch.ValidateExpiry(expiresAt, time.Now().UTC()); err != nil {
		return Watch{}, err
	}
	if err := s.commit("", "effect", "watch_renewed", change{Watch: &watchChange{Renew: &watchRenewal{ID: id, ExpiresAt: expiresAt.UTC()}}}); err != nil {
		return Watch{}, err
	}
	return s.watchView(w).Watch, nil
}

func (s *Store) CancelWatch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.watches[id]
	if w == nil {
		return ErrNotFound
	}
	if w.State != "active" {
		return ErrConflict
	}
	return s.commit("", "effect", "watch_cancelled", change{Watch: &watchChange{End: &watchEnd{ID: id, State: "cancelled"}}})
}

// ResolveWatchItem closes an open Notifications item so the next escalation for
// its watch and group key opens a new one.
func (s *Store) ResolveWatchItem(deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.watchDeliveries[deliveryID]
	if d == nil {
		return ErrNotFound
	}
	if d.ItemState != "open" {
		return ErrConflict
	}
	return s.commit("", "effect", "watch_item_resolved", change{Watch: &watchChange{Resolve: &watchResolve{DeliveryID: deliveryID}}})
}

// EvaluateWatches ends expired leases, reports silence and delivers due groups
// at now. Every decision is committed as a receipt; replay never re-evaluates.
func (s *Store) EvaluateWatches(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.watches))
	for id, w := range s.watches {
		if w.State == "active" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		w := s.watches[id]
		if w.State != "active" {
			continue
		}
		if !now.Before(w.Spec.Lease.ExpiresAt) {
			d := s.summaryDelivery(w, "expired", watch.Notify, now)
			if err := s.commit("", "effect", "watch_expired", change{Watch: &watchChange{Delivery: &d, End: &watchEnd{ID: id, State: "expired"}}}); err != nil {
				return err
			}
			continue
		}
		if w.Spec.MaxSilenceMS > 0 && !w.Silent && !now.Before(w.LastActivity.Add(time.Duration(w.Spec.MaxSilenceMS)*time.Millisecond)) {
			d := s.summaryDelivery(w, "silent", watch.Notify, now)
			if err := s.commit("", "effect", "watch_silent", change{Watch: &watchChange{Delivery: &d}}); err != nil {
				return err
			}
		}
		keys := make([]string, 0, len(w.Groups))
		for key := range w.Groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			g := w.Groups[key]
			if g == nil || !g.Due(now, w.LastDelivered[key], w.Spec.Grouping) {
				continue
			}
			d := s.groupDelivery(w, g, now)
			if err := s.commit("", "effect", "watch_delivered", change{Watch: &watchChange{Delivery: &d}}); err != nil {
				return err
			}
		}
	}
	return nil
}

// summaryDelivery reports a lease or silence notice with the count of events
// still waiting, at the rule level or the watch default if higher.
func (s *Store) summaryDelivery(w *Watch, kind string, rule watch.Level, now time.Time) WatchDelivery {
	d := WatchDelivery{ID: newID("delivery"), WatchID: w.ID, Owner: w.Spec.Owner, Kind: kind, Level: watch.Max(rule, w.Spec.DefaultLevel), Counts: map[string]int{}, Watches: []string{w.ID}}
	for _, g := range w.Groups {
		for _, e := range g.Entries {
			d.Count++
			d.Counts[e.Kind]++
		}
	}
	s.attachWatchItem(&d, w, now)
	return d
}

// groupDelivery builds the receipt for a due group. Entries that the owner's
// other watches also hold join this delivery at the highest level any of them
// assigned, so the owner receives each event once.
func (s *Store) groupDelivery(w *Watch, g *watch.Group, now time.Time) WatchDelivery {
	d := WatchDelivery{ID: newID("delivery"), WatchID: w.ID, Owner: w.Spec.Owner, Kind: "group", GroupKey: g.Key, Counts: map[string]int{}}
	levels := map[int64]watch.Level{}
	watches := map[string]bool{w.ID: true}
	for _, e := range g.Entries {
		levels[e.Seq] = e.Level
	}
	for _, other := range s.watches {
		if other.ID == w.ID || other.State != "active" || other.Spec.Owner.Key() != w.Spec.Owner.Key() {
			continue
		}
		for _, og := range other.Groups {
			for _, e := range og.Entries {
				if level, ok := levels[e.Seq]; ok {
					levels[e.Seq] = watch.Max(level, e.Level)
					watches[other.ID] = true
				}
			}
		}
	}
	runs, reasons := map[string]bool{}, map[string]bool{}
	for _, e := range g.Entries {
		d.Seqs = append(d.Seqs, e.Seq)
		d.Count++
		d.Counts[e.Kind]++
		d.Level = watch.Max(d.Level, levels[e.Seq])
		if e.Run != "" && len(runs) < 8 {
			runs[e.Run] = true
		}
		if e.Reason != "" {
			reasons[e.Reason] = true
		}
	}
	slices.Sort(d.Seqs)
	d.FirstSeq, d.LastSeq = d.Seqs[0], d.Seqs[len(d.Seqs)-1]
	d.Runs, d.Reasons, d.Watches = sortedKeys(runs), sortedKeys(reasons), sortedKeys(watches)
	s.attachWatchItem(&d, w, now)
	return d
}

// attachWatchItem decides whether a wake/escalate delivery for the operator
// opens a Notifications item or coalesces into the open one.
func (s *Store) attachWatchItem(d *WatchDelivery, w *Watch, now time.Time) {
	if d.Owner.Kind != "operator" || d.Level.Rank() < watch.Wake.Rank() {
		return
	}
	open := s.watchDeliveries[s.watchItems[w.ID+"\x00"+d.GroupKey]]
	if open != nil && open.ItemState == "open" {
		repeat := time.Duration(w.Spec.Grouping.RepeatIntervalMS) * time.Millisecond
		if now.Before(open.At.Add(repeat)) {
			d.ItemID = open.ID
			return
		}
		d.Supersedes = open.ID
	}
	d.ItemState = "open"
}

// WatchView is the read model: the record plus its derived cursor and pending groups.
type WatchView struct {
	Watch
	Cursor  int64            `json:"cursor"`
	Pending int              `json:"pending"`
	Groups  []watchGroupView `json:"groups"`
}
type watchGroupView struct {
	Key      string `json:"key"`
	Count    int    `json:"count"`
	FirstSeq int64  `json:"first_seq"`
}

func (s *Store) watchView(w *Watch) WatchView {
	v := WatchView{Watch: *w, Cursor: s.seq, Groups: []watchGroupView{}}
	v.Groups, v.LastDelivered, v.LastActivity = []watchGroupView{}, nil, time.Time{}
	v.Watch.Groups = nil
	for _, g := range w.Groups {
		first := g.Entries[0].Seq
		for _, e := range g.Entries {
			first = min(first, e.Seq)
		}
		v.Pending += len(g.Entries)
		v.Cursor = min(v.Cursor, first-1)
		v.Groups = append(v.Groups, watchGroupView{Key: g.Key, Count: len(g.Entries), FirstSeq: first})
	}
	sort.Slice(v.Groups, func(i, j int) bool { return v.Groups[i].Key < v.Groups[j].Key })
	return v
}

func (s *Store) Watches() []WatchView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []WatchView{}
	for _, w := range s.watches {
		out = append(out, s.watchView(w))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedSeq > out[j].CreatedSeq })
	return out
}

func (s *Store) Watch(id string) (WatchView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.watches[id]
	if w == nil {
		return WatchView{}, ErrNotFound
	}
	return s.watchView(w), nil
}

// WatchDeliveries returns a watch's receipts, newest first.
func (s *Store) WatchDeliveries(id string, limit int) ([]WatchDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watches[id] == nil {
		return nil, ErrNotFound
	}
	ids := s.watchOrder[id]
	out := []WatchDelivery{}
	for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, *s.watchDeliveries[ids[i]])
	}
	return out, nil
}

// WatchItem is an open operator Notifications item.
type WatchItem struct {
	WatchDelivery
	Description string `json:"description"`
	ProjectID   string `json:"project_id,omitempty"`
}

func (s *Store) OperatorWatchItems() []WatchItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []WatchItem{}
	for _, id := range s.watchItems {
		d := s.watchDeliveries[id]
		if d == nil || d.ItemState != "open" {
			continue
		}
		w := s.watches[d.WatchID]
		out = append(out, WatchItem{WatchDelivery: *d, Description: w.Spec.Description, ProjectID: w.Spec.Condition.Scope.Project})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// watchLoop evaluates watches on a short fixed tick. Deliveries carry their own
// receipts, so a missed tick only delays delivery; it cannot lose an event.
func (e *Engine) watchLoop() {
	defer e.wg.Done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case now := <-tick.C:
			if err := e.Store.EvaluateWatches(now.UTC()); err != nil && errors.Is(err, errJournalClosed) {
				return
			}
		}
	}
}
