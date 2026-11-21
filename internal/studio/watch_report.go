package studio

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"mitos/internal/watch"
)

// Delivery outcomes and calibration marks (spec 09 §5, gate W4). Every
// delivery accepts at most one owner outcome (acted, dismissed or deferred),
// and the operator may mark a delivered or digested group should_have_woken or
// needless_wake. The records are evidence for tuning a provider's triage
// thresholds by hand; the runtime never adjusts thresholds from them. A
// receipt is immutable, so outcomes and marks live beside it, joined into the
// read models, never written into the stored receipt.

// operatorMarks is the closed set of operator calibration marks.
var operatorMarks = []string{"should_have_woken", "needless_wake"}

// watchMark is the journal record of one outcome or mark on a delivery.
type watchMark struct {
	DeliveryID string `json:"delivery_id"`
	Outcome    string `json:"outcome,omitempty"` // acted, dismissed or deferred
	Mark       string `json:"mark,omitempty"`    // should_have_woken or needless_wake
}

// WatchMark is the read model of a recorded outcome or mark.
type WatchMark struct {
	DeliveryID string    `json:"delivery_id"`
	Outcome    string    `json:"outcome,omitempty"`
	Mark       string    `json:"mark,omitempty"`
	At         time.Time `json:"at"`
}

func (s *Store) applyDeliveryMark(e Event, m *watchMark) error {
	d := s.watchDeliveries[m.DeliveryID]
	if d == nil {
		return errors.New("mark for a delivery that does not exist")
	}
	if !slices.Contains(deliveryOutcomes, m.Outcome) && m.Outcome != "" ||
		!slices.Contains(operatorMarks, m.Mark) && m.Mark != "" ||
		(m.Outcome == "") == (m.Mark == "") {
		return errors.New("mark needs exactly one outcome or mark value")
	}
	if prior := s.watchMarks[m.DeliveryID]; prior != nil {
		if prior.Outcome != m.Outcome || prior.Mark != m.Mark {
			return errors.New("conflicting mark for a delivery")
		}
		return nil
	}
	s.watchMarks[m.DeliveryID] = &WatchMark{DeliveryID: m.DeliveryID, Outcome: m.Outcome, Mark: m.Mark, At: e.At}
	return nil
}

// RecordDeliveryMark records an owner outcome or an operator calibration mark
// on one delivery (spec 09 §5, §6 POST /api/deliveries/{id}/outcome). Exactly
// one of outcome and mark must be set. An outcome records what the owner did:
// over the HTTP API the caller is the operator, so only operator-owned
// deliveries take an outcome here; responsibility and attempt owners settle
// their own deliveries through the granted delivery_outcome tool, which fences
// on the current orchestrator generation. A mark is operator calibration
// evidence and applies to any delivery. Repeating the same value is
// idempotent; a different one is refused.
func (s *Store) RecordDeliveryMark(deliveryID, outcome, mark string) (WatchDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if (outcome == "") == (mark == "") {
		return WatchDelivery{}, errors.New("record exactly one outcome or mark")
	}
	d := s.watchDeliveries[deliveryID]
	if d == nil {
		return WatchDelivery{}, ErrNotFound
	}
	if outcome != "" {
		if !slices.Contains(deliveryOutcomes, outcome) {
			return WatchDelivery{}, errors.New("outcome must be acted, dismissed or deferred")
		}
		if d.Owner.Kind != watch.OwnerOperator {
			return WatchDelivery{}, errors.New("over the API only the operator records owner outcomes; responsibility and attempt owners use the delivery_outcome tool")
		}
	} else if !slices.Contains(operatorMarks, mark) {
		return WatchDelivery{}, errors.New("mark must be should_have_woken or needless_wake")
	}
	if prior := s.watchMarks[deliveryID]; prior != nil {
		if prior.Outcome != outcome || prior.Mark != mark {
			return WatchDelivery{}, errMarkConflict(prior)
		}
		return cloneDeliveryWithMarks(s, d), nil
	}
	m := watchMark{DeliveryID: deliveryID, Outcome: outcome, Mark: mark}
	if err := s.commit("", "effect", "watch_delivery_mark", change{Watch: &watchChange{Mark: &m}}); err != nil {
		return WatchDelivery{}, err
	}
	return cloneDeliveryWithMarks(s, s.watchDeliveries[deliveryID]), nil
}

func errMarkConflict(prior *WatchMark) error {
	if prior.Outcome != "" {
		return fmt.Errorf("%w: delivery already has outcome %s", ErrConflict, prior.Outcome)
	}
	return fmt.Errorf("%w: delivery already has mark %s", ErrConflict, prior.Mark)
}

// cloneDeliveryWithMarks joins a receipt with its outcome (the mark record,
// or the inbox entry the delivery_outcome tool settled) and its operator mark.
func cloneDeliveryWithMarks(s *Store, d *WatchDelivery) WatchDelivery {
	out := cloneDelivery(d)
	if m := s.watchMarks[d.ID]; m != nil {
		if m.Outcome != "" {
			out.Outcome = m.Outcome
		}
		out.Mark = m.Mark
	}
	if out.Outcome == "" {
		if entry := s.inboxEntries[d.ID]; entry != nil {
			out.Outcome = entry.Outcome
		}
	}
	return out
}

// WatchTriageUsage summarises the triage calls one watch made.
type WatchTriageUsage struct {
	Requests  int  `json:"requests"` // every question the watch asked
	Pending   int  `json:"pending"`
	Settled   int  `json:"settled"`
	Abandoned int  `json:"abandoned"` // delivered at the default without an answer
	Degraded  bool `json:"degraded"`  // the allowance ran out; one watch_degraded notice
}

// WatchReport is the calibration read model of one watch (spec 09 §5): counts
// by level, by level source and fallback reason, the outcomes and marks its
// deliveries collected, and the triage calls it used. The derived ratios are
// nil until their denominators exist, so a report never invents precision.
type WatchReport struct {
	WatchID     string         `json:"watch_id"`
	Description string         `json:"description"`
	Owner       watch.Owner    `json:"owner"`
	State       string         `json:"state"`
	Deliveries  int            `json:"deliveries"`
	Levels      map[string]int `json:"levels"`
	Sources     map[string]int `json:"sources"`
	Fallbacks   map[string]int `json:"fallbacks"`
	Outcomes    map[string]int `json:"outcomes"`
	Marks       map[string]int `json:"marks"`

	// Wake precision: of the wake and escalate deliveries the operator
	// marked, the share that were not needless_wake.
	WakeDeliveries int      `json:"wake_deliveries"`
	MarkedWake     int      `json:"marked_wake"`
	NeedlessWake   int      `json:"needless_wake"`
	WakePrecision  *float64 `json:"wake_precision,omitempty"`

	// Fallback rate: of the groups triage graded (an accepted answer or a
	// fallback), the share that fell back to the default.
	TriagedGroups  int      `json:"triaged_groups"`
	FallbackGroups int      `json:"fallback_groups"`
	FallbackRate   *float64 `json:"fallback_rate,omitempty"`

	Triage WatchTriageUsage `json:"triage_usage"`
}

// WatchReport exposes one watch's calibration evidence.
func (s *Store) WatchReport(id string) (WatchReport, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w := s.watches[id]
	if w == nil {
		return WatchReport{}, ErrNotFound
	}
	r := WatchReport{
		WatchID: w.ID, Description: w.Spec.Description, Owner: w.Spec.Owner, State: w.State,
		Levels: map[string]int{}, Sources: map[string]int{}, Fallbacks: map[string]int{},
		Outcomes: map[string]int{}, Marks: map[string]int{},
	}
	for _, id := range s.watchOrder[w.ID] {
		d := cloneDeliveryWithMarks(s, s.watchDeliveries[id])
		r.Deliveries++
		r.Levels[string(d.Level)]++
		r.Sources[d.Source]++
		if d.Source == watch.SourceFallback {
			r.Fallbacks[d.Fallback]++
		}
		if d.Outcome != "" {
			r.Outcomes[d.Outcome]++
		}
		if d.Mark != "" {
			r.Marks[d.Mark]++
			if d.Level.Rank() >= watch.Wake.Rank() {
				r.MarkedWake++
				if d.Mark == "needless_wake" {
					r.NeedlessWake++
				}
			}
		}
		if d.Level.Rank() >= watch.Wake.Rank() {
			r.WakeDeliveries++
		}
		switch d.Source {
		case watch.SourceTriage, watch.SourceFallback:
			r.TriagedGroups++
			if d.Source == watch.SourceFallback {
				r.FallbackGroups++
			}
		}
	}
	if r.MarkedWake > 0 {
		p := 1 - float64(r.NeedlessWake)/float64(r.MarkedWake)
		r.WakePrecision = &p
	}
	if r.TriagedGroups > 0 {
		f := float64(r.FallbackGroups) / float64(r.TriagedGroups)
		r.FallbackRate = &f
	}
	for _, t := range s.triageRequests {
		if t.WatchID != w.ID {
			continue
		}
		r.Triage.Requests++
		switch t.State {
		case "pending":
			r.Triage.Pending++
		case "settled":
			r.Triage.Settled++
		case "abandoned":
			r.Triage.Abandoned++
		}
	}
	r.Triage.Degraded = w.TriageDegraded
	return r, nil
}
