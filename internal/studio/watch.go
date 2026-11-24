package studio

import (
	"encoding/json"
	"errors"
	"maps"
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
	ID    string     `json:"id"`
	Spec  watch.Spec `json:"spec"`
	State string     `json:"state"` // active, expired, cancelled, settled, revoked
	// Grant is the scope the owner held at creation; Spec.Condition.Scope is
	// within it. Revocation compares the owner's current grant against it.
	Grant watch.Scope `json:"grant"`
	// Template names the kernel template this watch was created from, when
	// it was (#126); template creations are deduplicated per graph.
	Template      string                  `json:"template,omitempty"`
	CreatedBy     *watchCreator           `json:"created_by,omitempty"`
	CreatedAt     time.Time               `json:"created_at"`
	CreatedSeq    int64                   `json:"created_seq"`
	Renewals      int                     `json:"renewals"`
	EndedAt       *time.Time              `json:"ended_at,omitempty"`
	Groups        map[string]*watch.Group `json:"-"`
	LastDelivered map[string]time.Time    `json:"-"`
	LastActivity  time.Time               `json:"-"`
	Silent        bool                    `json:"-"`
	// TriageCalls counts this watch's triage requests over its whole life;
	// TriageDegraded records its single allowance-exhausted notice.
	TriageCalls    int  `json:"triage_calls,omitempty"`
	TriageDegraded bool `json:"triage_degraded,omitempty"`
}

// WatchDelivery is one immutable receipt: which events reached an owner, at
// which level, and where the runtime routed them. Route is feed (the
// operator's delivery feed), inbox (queued for a responsibility or attempt;
// see InboxEntry) or folded (counted into that owner's digest, never queued).
// ItemID names the Notifications item the receipt opened or updated.
// Generation is the responsibility generation current when it was written,
// and Trigger the graph wake registration written in the same record.
type WatchDelivery struct {
	ID         string         `json:"id"`
	WatchID    string         `json:"watch_id"`
	Owner      watch.Owner    `json:"owner"`
	Kind       string         `json:"kind"`              // group, expired, silent, revoked, degraded, digest, operator_message
	Message    string         `json:"message,omitempty"` // operator-authored body for a direct delivery
	Level      watch.Level    `json:"level"`
	Route      string         `json:"route"`
	GroupKey   string         `json:"group_key,omitempty"`
	FirstSeq   int64          `json:"first_seq,omitempty"`
	LastSeq    int64          `json:"last_seq,omitempty"`
	Seqs       []int64        `json:"seqs,omitempty"`
	Count      int            `json:"count"`
	Counts     map[string]int `json:"counts,omitempty"`
	Reasons    []string       `json:"reasons,omitempty"`
	Watches    []string       `json:"watches,omitempty"`
	Runs       []string       `json:"runs,omitempty"`
	Generation int64          `json:"generation,omitempty"`
	Trigger    int64          `json:"trigger,omitempty"`
	ItemID     string         `json:"item_id,omitempty"`
	// Source says where Level came from: a rule, the watch default, an
	// accepted triage answer, or a fallback to the default (Fallback says why).
	Source   string `json:"source"`
	Fallback string `json:"fallback,omitempty"`
	TriageID string `json:"triage_id,omitempty"`
	// Outcome and Mark are calibration records attached after delivery (spec 09
	// §5), joined into this view: the owner outcome the delivery_outcome tool
	// settled, or the outcome or mark recorded over the API. The stored receipt
	// never changes.
	Outcome string    `json:"outcome,omitempty"`
	Mark    string    `json:"mark,omitempty"`
	At      time.Time `json:"at"`
}

const (
	routeFeed   = "feed"
	routeInbox  = "inbox"
	routeFolded = "folded"
)

// WatchItem is an operator Notifications item. One delivery opens it; later
// deliveries for the same watch and group key update it until it is resolved,
// or until repeat_interval_ms has passed and a new item supersedes it.
type WatchItem struct {
	ID         string         `json:"id"`
	WatchID    string         `json:"watch_id"`
	Owner      watch.Owner    `json:"owner"`
	GroupKey   string         `json:"group_key,omitempty"`
	Kind       string         `json:"kind"`
	State      string         `json:"state"` // open, resolved, superseded
	Level      watch.Level    `json:"level"`
	Count      int            `json:"count"`
	Counts     map[string]int `json:"counts,omitempty"`
	Reasons    []string       `json:"reasons,omitempty"`
	FirstSeq   int64          `json:"first_seq,omitempty"`
	LastSeq    int64          `json:"last_seq,omitempty"`
	Deliveries []string       `json:"deliveries"`
	Supersedes string         `json:"supersedes,omitempty"`
	OpenedAt   time.Time      `json:"opened_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ResolvedAt *time.Time     `json:"resolved_at,omitempty"`
}

// routedDelivery is one receipt with the records its routing creates in the
// same journal event: a newly opened item and the owner's inbox entry.
type routedDelivery struct {
	Delivery WatchDelivery `json:"delivery"`
	Item     *WatchItem    `json:"item,omitempty"`
	Inbox    *InboxEntry   `json:"inbox,omitempty"`
}

type watchChange struct {
	Creates     []*Watch                 `json:"creates,omitempty"`
	Renew       *watchRenewal            `json:"renew,omitempty"`
	Memberships []supervisionWatchChange `json:"memberships,omitempty"`
	Deliveries  []routedDelivery         `json:"deliveries,omitempty"`
	End         *watchEnd                `json:"end,omitempty"`
	Resolve     *watchResolve            `json:"resolve,omitempty"`
	Outcome     *watchOutcome            `json:"outcome,omitempty"`
	Mark        *watchMark               `json:"mark,omitempty"`
	Triage      *TriageRequest           `json:"triage,omitempty"`
	Triaged     *triageOutcome           `json:"triaged,omitempty"`
}
type watchRenewal struct {
	ID        string    `json:"id"`
	ExpiresAt time.Time `json:"expires_at"`
	// Kernel marks a supervision lease renewal by the runtime, which does
	// not consume the owner's renewal allowance (#126).
	Kernel bool `json:"kernel,omitempty"`
}
type watchEnd struct {
	ID    string `json:"id"`
	State string `json:"state"`
}
type watchResolve struct {
	ItemID string `json:"item_id"`
}

var errWatchLimit = errors.New("owner watch limit reached")

func (s *Store) applyWatch(e Event, c *watchChange) error {
	if s.watches == nil {
		s.watches = map[string]*Watch{}
		s.watchDeliveries = map[string]*WatchDelivery{}
		s.watchOrder = map[string][]string{}
		s.operatorMessages = map[string][]string{}
		s.watchItems = map[string]*WatchItem{}
		s.watchOpenItems = map[string]string{}
		s.watchInboxes = map[string]*watchInbox{}
		s.inboxEntries = map[string]*InboxEntry{}
		s.triageRequests = map[string]*TriageRequest{}
		s.triagePending = map[string]string{}
		s.triageUsed = map[string]int{}
		s.watchMarks = map[string]*WatchMark{}
	}
	for _, w := range c.Creates {
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
		if !r.Kernel {
			w.Renewals++
		}
	}
	for _, m := range c.Memberships {
		w := s.watches[m.Watch]
		if w == nil {
			return errors.New("membership change for unknown watch")
		}
		for _, tid := range m.Remove {
			// A settled or revoked watch keeps its record; removing a member
			// from it is still meaningful bookkeeping.
			w.Spec.Condition.Members = slices.DeleteFunc(w.Spec.Condition.Members, func(id string) bool { return id == tid })
		}
		if len(m.Add) > 0 {
			if w.State != "active" {
				return errors.New("membership change for inactive watch")
			}
			for _, tid := range m.Add {
				if !slices.Contains(w.Spec.Condition.Members, tid) {
					w.Spec.Condition.Members = append(w.Spec.Condition.Members, tid)
				}
			}
			slices.Sort(w.Spec.Condition.Members)
		}
	}
	if t := c.Triage; t != nil {
		if err := s.applyTriageRequest(e, t); err != nil {
			return err
		}
	}
	if o := c.Triaged; o != nil {
		if err := s.applyTriageOutcome(e, o); err != nil {
			return err
		}
	}
	for i := range c.Deliveries {
		if err := s.applyWatchDelivery(e, &c.Deliveries[i]); err != nil {
			return err
		}
	}
	if o := c.Outcome; o != nil {
		if err := s.applyDeliveryOutcome(e, o); err != nil {
			return err
		}
	}
	if m := c.Mark; m != nil {
		if err := s.applyDeliveryMark(e, m); err != nil {
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
		s.abandonTriage(w.ID)
	}
	if r := c.Resolve; r != nil {
		item := s.watchItems[r.ItemID]
		if item == nil || item.State != "open" {
			return errors.New("resolution of a closed watch item")
		}
		at := e.At
		item.State, item.ResolvedAt = "resolved", &at
		delete(s.watchOpenItems, item.WatchID+"\x00"+item.GroupKey)
	}
	return nil
}

func (s *Store) applyWatchDelivery(e Event, rd *routedDelivery) error {
	d := &rd.Delivery
	w := s.watches[d.WatchID]
	// A digest spans the owner's watches and may outlive the one it names.
	direct := d.WatchID == "" && d.Owner.Kind == watch.OwnerResponsibility && (d.Kind == "operator_message" || d.Kind == "digest")
	if !direct && (w == nil || w.State != "active" && d.Kind != "digest") || s.watchDeliveries[d.ID] != nil {
		return errors.New("delivery for inactive watch")
	}
	if (d.Route == routeInbox) != (rd.Inbox != nil) || d.Route != routeFeed && d.Route != routeInbox && d.Route != routeFolded {
		return errors.New("delivery route does not match its records")
	}
	saved := *d
	saved.At = e.At
	if d.Kind == "group" {
		delivered := map[int64]bool{}
		for _, seq := range d.Seqs {
			delivered[seq] = true
		}
		// One owner receives an event once: drop it from every watch of that
		// owner and advance the delivery clock of every group it consumed, so
		// the grouping interval holds whichever of those watches delivers next.
		for _, other := range s.watches {
			if other.State != "active" || other.Spec.Owner.Key() != d.Owner.Key() {
				continue
			}
			for key, g := range other.Groups {
				consumed := false
				g.Entries = slices.DeleteFunc(g.Entries, func(x watch.Entry) bool {
					if delivered[x.Seq] {
						consumed = true
						return true
					}
					return false
				})
				if consumed {
					other.LastDelivered[key] = e.At
				}
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
	if d.Kind == "degraded" && slices.Contains(d.Reasons, triageExhausted) {
		w.TriageDegraded = true
	}
	if err := s.applyWatchItem(e, &saved, rd.Item); err != nil {
		return err
	}
	s.watchDeliveries[d.ID] = &saved
	if d.Kind == "operator_message" {
		s.operatorMessages[d.Owner.ID] = append(s.operatorMessages[d.Owner.ID], d.ID)
	} else if d.WatchID != "" {
		s.watchOrder[d.WatchID] = append(s.watchOrder[d.WatchID], d.ID)
	}
	s.applyInbox(e, &saved, rd.Inbox)
	return nil
}

// applyWatchItem opens the item a delivery created, or folds the delivery
// into the open item it names.
func (s *Store) applyWatchItem(e Event, d *WatchDelivery, opened *WatchItem) error {
	if d.ItemID == "" {
		if opened != nil {
			return errors.New("item without its delivery")
		}
		return nil
	}
	if opened != nil {
		if opened.ID != d.ItemID || s.watchItems[opened.ID] != nil {
			return errors.New("invalid watch item")
		}
		item := graphClone(*opened)
		item.State, item.Deliveries, item.OpenedAt, item.UpdatedAt = "open", []string{d.ID}, e.At, e.At
		if old := s.watchItems[item.Supersedes]; old != nil && old.State == "open" {
			old.State = "superseded"
		}
		s.watchItems[item.ID] = &item
		s.watchOpenItems[item.WatchID+"\x00"+item.GroupKey] = item.ID
		return nil
	}
	item := s.watchItems[d.ItemID]
	if item == nil || item.State != "open" {
		return errors.New("coalescing into a closed watch item")
	}
	item.Deliveries = append(item.Deliveries, d.ID)
	item.Count += d.Count
	item.LastSeq = max(item.LastSeq, d.LastSeq)
	item.Level = watch.Max(item.Level, d.Level)
	item.UpdatedAt = e.At
	for k, n := range d.Counts {
		if item.Counts == nil {
			item.Counts = map[string]int{}
		}
		item.Counts[k] += n
	}
	for _, r := range d.Reasons {
		if !slices.Contains(item.Reasons, r) {
			item.Reasons = append(item.Reasons, r)
		}
	}
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
	case "attempt_overrun":
		return watch.Wake, "attempt_overrun"
	case "attempt_stalled":
		return watch.Wake, "attempt_stalled"
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
	project := r.ProjectID
	if project == "" {
		// Runs created through Store carry their project on the brief; the
		// top-level field stays empty until a graph attempt binds one.
		project = r.Brief.ProjectID
	}
	f := watch.Fact{Seq: e.Seq, At: e.At, Project: project, Task: r.TaskID, Run: r.ID, Type: e.Type, Kind: e.Kind}
	if r.GraphAttempt != nil {
		f.Graph = r.GraphAttempt.GraphID
		if f.Project == "" && s.graphs[f.Graph] != nil {
			f.Project = s.graphs[f.Graph].ProjectID
		}
	}
	f.Rule, f.Reason = watchRule(e, r, c)
	f.Summary = factSummary(c.Summary, c.Stage)
	return f
}

// graphFact projects a graph-level journal event that carries no run: the
// graph and its project scope the fact.
func (s *Store) graphFact(e Event, c *graphChange) watch.Fact {
	return watch.Fact{Seq: e.Seq, At: e.At, Project: c.Graph.ProjectID, Graph: c.Graph.ID, Type: e.Type, Kind: e.Kind}
}

// agentRequestScopes attributes an agent request to the run its fact belongs
// to, as exactly one fact per journal event: several facts would share one
// sequence, and delivery deduplication would silently consume all but the
// first-delivered group (issue #130). An executing attempt's session is bound
// to its run, so the question carries that run's project, graph, task and run,
// and attempt, responsibility and project watches all see it. An unbound
// interactive session attributes to its first review run, deterministically.
func (s *Store) agentRequestScopes(q *AgentRequest) []watch.Fact {
	if a, ok := s.agentSessions[q.SessionID]; ok && a.RunID != "" {
		if r := s.runs[a.RunID]; r != nil {
			project := r.ProjectID
			if project == "" {
				project = r.Brief.ProjectID
			}
			f := watch.Fact{Project: project, Run: r.ID, Task: r.TaskID}
			if r.GraphAttempt != nil {
				f.Graph = r.GraphAttempt.GraphID
				if f.Project == "" {
					if g := s.graphs[f.Graph]; g != nil {
						f.Project = g.ProjectID
					}
				}
			}
			return []watch.Fact{f}
		}
	}
	if len(q.Runs) == 0 {
		if a, ok := s.agentSessions[q.SessionID]; ok {
			return []watch.Fact{{Project: a.ProjectID}}
		}
		return nil
	}
	return []watch.Fact{{Project: q.Runs[0].ProjectID, Run: q.Runs[0].ID}}
}

// agentRequestFacts projects agent request events into watch facts (spec 09
// §2). A question that needs a human answer is a must-deliver wake (reason
// agent_awaiting_answer); a request automatic review can answer never wakes
// anyone until routing falls back to a human. A request's end is a notify on
// the same attribution, so the open group can close. ended lists requests this
// event newly ended (session close, expiry or thread end).
func (s *Store) agentRequestFacts(e Event, c change, ended []string) []watch.Fact {
	var requests []AgentRequest
	reason := ""
	question := false
	wake := false
	switch {
	case e.Kind == "agent_approval_requested" && c.AgentRequest != nil && !c.AgentRequest.Invalidated:
		q := *c.AgentRequest
		requests = []AgentRequest{q}
		question = true
		// Wake only when human resolution is actually required, projected the
		// same way the activity surface projects it; a delegated or classified
		// resolver answers without an operator.
		wake = s.agentRequestNeedsHuman(&q)
	case e.Kind == "agent_request_routed" || e.Kind == "agent_resolution_needs_operator":
		if c.AgentRoute != nil {
			if q, ok := s.agentRequests[c.AgentRoute.Key]; ok {
				requests = []AgentRequest{q}
				question = true
				// Routing decides: a classifier or policy revision can still
				// land the request on an operator after ingest projected
				// otherwise, and a failed automatic review falls back to one.
				// A request already woken is not woken again — the fold
				// remembers the actual prior wake, not a recomputed
				// projection, so a policy change in between neither skips
				// nor repeats it — and the two events share no sequence, so
				// a second wake could not be deduplicated away.
				wake = c.AgentRoute.Route.Resolver == "human" && !s.agentWakes[c.AgentRoute.Key]
			}
		}
	case c.AgentRequest != nil && c.AgentRequest.Invalidated:
		requests = []AgentRequest{*c.AgentRequest}
		reason = "agent_request_ended"
	default:
		for _, key := range ended {
			if q, ok := s.agentRequests[key]; ok {
				requests = append(requests, q)
			}
		}
		reason = "agent_request_ended"
	}
	facts := []watch.Fact{}
	for _, q := range requests {
		summary := factSummary(c.Summary, c.Stage)
		if summary == "" {
			if question && len(q.Request.Cards) > 0 {
				summary = factSummary(q.Request.Cards[0].Question, "")
			} else if !question {
				summary = "The waiting request ended without an answer."
			}
		}
		for _, scope := range s.agentRequestScopes(&q) {
			scope.Seq, scope.At, scope.Type, scope.Kind = e.Seq, e.At, e.Type, e.Kind
			scope.Summary = summary
			switch {
			case wake:
				scope.Rule, scope.Reason = watch.Wake, "agent_awaiting_answer"
				s.rememberAgentWake(&q)
			case reason != "":
				scope.Rule, scope.Reason = watch.Notify, reason
			}
			facts = append(facts, scope)
		}
	}
	return facts
}

// rememberAgentWake records that a request has delivered its
// agent_awaiting_answer wake. Fold state over the journal, rebuilt identically
// on replay; caller holds the store lock.
func (s *Store) rememberAgentWake(q *AgentRequest) {
	if s.agentWakes == nil {
		s.agentWakes = map[string]bool{}
	}
	s.agentWakes[q.SessionID+"/"+q.Request.Key] = true
}

// agentRequestNeedsHuman projects whether an ingested request blocks on a
// human answer, exactly as the activity surface projects it, from the current
// policy alone: at ingest no routing decision exists yet, so this is also the
// shape a routing event compares against to wake only on a change. Caller
// holds the store lock.
func (s *Store) agentRequestNeedsHuman(q *AgentRequest) bool {
	session, ok := s.agentSessions[q.SessionID]
	if !ok {
		// Without the session record nothing can answer automatically.
		return true
	}
	route, _ := s.projectRequestRoute(session, *q, nil)
	return route.Resolver == "human"
}

// agentAnswerFact projects the answer to an agent request as one attributed
// fact (notify, reason agent_answered) instead of the review run's own parked
// fact: both would share this journal event's sequence, and delivery dedup
// would silently consume whichever group delivered second (issue #130).
// Caller holds the store lock; ok is false for ordinary decisions.
func (s *Store) agentAnswerFact(e Event, r *Run, c change) ([]watch.Fact, bool) {
	if e.Kind != "approval_resolved" || c.AgentAnswer == nil || r.Decision == nil || r.Decision.Source == nil {
		return nil, false
	}
	q, ok := s.agentRequests[r.Decision.Source.SessionID+"/"+r.Decision.Source.RequestID]
	if !ok {
		return nil, false
	}
	facts := []watch.Fact{}
	for _, scope := range s.agentRequestScopes(&q) {
		scope.Seq, scope.At, scope.Type, scope.Kind = e.Seq, e.At, e.Type, e.Kind
		scope.Summary = factSummary(c.Summary, c.Stage)
		scope.Rule, scope.Reason = watch.Notify, "agent_answered"
		facts = append(facts, scope)
	}
	return facts, true
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
		// A kernel-judgment hold already has its operator card, so its notice
		// stays at notify: neither the watch default nor an overlapping watch
		// may raise it into a second Notifications item (spec 09 §3).
		if f.Reason == "judgment_parked" {
			level = watch.Notify
		}
		key := w.Spec.Grouping.GroupKey(f)
		g := w.Groups[key]
		if g == nil {
			g = &watch.Group{Key: key}
			w.Groups[key] = g
		}
		g.Entries = append(g.Entries, watch.Entry{Seq: f.Seq, At: f.At, Run: f.Run, Kind: f.Kind, Level: level, Reason: f.Reason, Summary: f.Summary})
		w.LastActivity, w.Silent = f.At, false
	}
}

// CreateWatch validates and records a watch created by the operator. A
// responsibility or attempt owner's scope is clamped to that owner's grant.
func (s *Store) CreateWatch(spec watch.Spec) (Watch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createWatchLocked(spec, nil)
}

func (s *Store) createWatchLocked(spec watch.Spec, by *watchCreator) (Watch, error) {
	templateName := ""
	if by != nil {
		templateName = by.Template
	}
	now := time.Now().UTC()
	grant, err := s.ownerGrant(spec.Owner)
	if err != nil {
		return Watch{}, err
	}
	// Clamp: an unset project or graph narrows to the owner's grant; a
	// conflicting value is refused.
	scope := &spec.Condition.Scope
	for _, f := range []struct {
		field *string
		value string
	}{{&scope.Project, grant.Project}, {&scope.Graph, grant.Graph}} {
		if *f.field == "" {
			*f.field = f.value
		}
	}
	// Member tasks are kernel-maintained (#126); an author never sets them.
	if spec.Condition.HasMembers() {
		return Watch{}, errors.New("condition.members is maintained by the kernel")
	}
	// Validate after clamping: an owner watch may leave its scope to the grant.
	if err := spec.Validate(now); err != nil {
		return Watch{}, err
	}
	// A responsibility watch with no kinds filter and a wake-or-higher
	// default would wake on every event; terminal states are delivered by
	// rule regardless. Clamp it to notify (#126).
	if spec.Owner.Kind == watch.OwnerResponsibility && len(spec.Condition.Kinds) == 0 && spec.DefaultLevel.Rank() >= watch.Wake.Rank() {
		spec.DefaultLevel = watch.Notify
	}
	if !scope.Within(grant) {
		return Watch{}, errors.New("condition.scope must stay within the owner's graph")
	}
	active := 0
	for _, w := range s.watches {
		if w.State == "active" && w.Spec.Owner.Key() == spec.Owner.Key() {
			active++
		}
	}
	if active >= spec.Owner.MaxActive() {
		return Watch{}, errWatchLimit
	}
	spec.Lease.ExpiresAt = spec.Lease.ExpiresAt.UTC()
	w := Watch{ID: newID("watch"), Spec: spec, State: "active", CreatedAt: now, Grant: grant, Template: templateName, CreatedBy: by}
	if err := s.commit("", "effect", "watch_created", change{Watch: &watchChange{Creates: []*Watch{&w}}}); err != nil {
		return Watch{}, err
	}
	return s.watchView(s.watches[w.ID]).Watch, nil
}

func (s *Store) RenewWatch(id string, expiresAt time.Time) (Watch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.renewWatchLocked(id, expiresAt)
}

func (s *Store) renewWatchLocked(id string, expiresAt time.Time) (Watch, error) {
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
	return s.cancelWatchLocked(id)
}

func (s *Store) cancelWatchLocked(id string) error {
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
func (s *Store) ResolveWatchItem(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item := s.watchItems[id]
	if item == nil {
		return ErrNotFound
	}
	if item.State != "open" {
		return ErrConflict
	}
	return s.commit("", "effect", "watch_item_resolved", change{Watch: &watchChange{Resolve: &watchResolve{ItemID: id}}})
}

// EvaluateWatches ends expired leases, reports silence and delivers due groups
// at now without a decision provider: a triaged watch delivers at its default
// with provider_unconfigured. The engine evaluates with its providers.
func (s *Store) EvaluateWatches(now time.Time) error {
	_, err := s.evaluateWatches(now, nil)
	return err
}

// evaluateWatches commits every decision as a receipt; replay never
// re-evaluates. It returns the triage requests it persisted, for the caller
// to dispatch outside the store lock.
func (s *Store) evaluateWatches(now time.Time, bind triageBinder) ([]triageDispatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.settleStaleTriage(now); err != nil {
		return nil, err
	}
	// The kernel maintains the supervision record first (#126): lease
	// renewal for live members, and watches whose last member settled.
	if err := s.reconcileSupervision(now); err != nil {
		return nil, err
	}
	dispatches := []triageDispatch{}
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
		if !s.ownerGrantHolds(w) {
			d := s.summaryDelivery(w, "revoked", watch.Notify)
			if err := s.commitWatchDelivery("watch_revoked", w, &d, &watchEnd{ID: id, State: "revoked"}, now); err != nil {
				return nil, err
			}
			continue
		}
		if !now.Before(w.Spec.Lease.ExpiresAt) {
			d := s.summaryDelivery(w, "expired", watch.Notify)
			if err := s.commitWatchDelivery("watch_expired", w, &d, &watchEnd{ID: id, State: "expired"}, now); err != nil {
				return nil, err
			}
			continue
		}
		if w.Spec.MaxSilenceMS > 0 && !w.Silent && !now.Before(w.LastActivity.Add(time.Duration(w.Spec.MaxSilenceMS)*time.Millisecond)) {
			d := s.summaryDelivery(w, "silent", watch.Notify)
			if err := s.commitWatchDelivery("watch_silent", w, &d, nil, now); err != nil {
				return nil, err
			}
		}
		keys := make([]string, 0, len(w.Groups))
		for key := range w.Groups {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			g := w.Groups[key]
			if g == nil {
				continue
			}
			// Events a pending triage request holds wait for its outcome; the
			// rest of the group is evaluated on its own.
			pending := s.triageRequests[s.triagePending[w.ID+"\x00"+key]]
			due := watch.Group{Key: key, Entries: unreserved(g.Entries, pending)}
			if !due.Due(now, w.LastDelivered[key], w.Spec.Grouping) {
				continue
			}
			if w.Spec.Triage == nil || hasRule(due.Entries) {
				// Must-deliver events are never triaged.
				d := s.groupDelivery(w, &due, nil)
				if err := s.commitWatchDelivery("watch_delivered", w, &d, nil, now); err != nil {
					return nil, err
				}
				continue
			}
			if pending != nil {
				continue // one request per group at a time
			}
			dispatch, err := s.beginTriage(w, &due, now, bind)
			if err != nil {
				return nil, err
			}
			if dispatch != nil {
				dispatches = append(dispatches, *dispatch)
			}
		}
	}
	return dispatches, s.flushDigests(now)
}

// summaryDelivery reports a lease or silence notice with the count of events
// still waiting, at the rule level or the watch default if higher.
func (s *Store) summaryDelivery(w *Watch, kind string, rule watch.Level) WatchDelivery {
	d := WatchDelivery{ID: newID("delivery"), WatchID: w.ID, Owner: w.Spec.Owner, Kind: kind, Level: watch.Max(rule, w.Spec.DefaultLevel), Source: watch.SourceRule, Counts: map[string]int{}, Watches: []string{w.ID}}
	for _, g := range w.Groups {
		for _, e := range g.Entries {
			d.Count++
			d.Counts[e.Kind]++
		}
	}
	return d
}

// groupDelivery builds the receipt for a due group. Entries that the owner's
// other watches also hold join this delivery at the highest level any of them
// assigned, so the owner receives each event once; sibling groups of this
// watch that hold the same sequences join the same way, so a single journal
// event projected into several run groups (a session ending several requests)
// arrives as one delivery carrying every affected run instead of being
// silently consumed by sequence deduplication. A triage grade replaces this
// watch's default for the group; it never lowers another watch's level.
func (s *Store) groupDelivery(w *Watch, g *watch.Group, grade *watch.Grade) WatchDelivery {
	d := WatchDelivery{ID: newID("delivery"), WatchID: w.ID, Owner: w.Spec.Owner, Kind: "group", GroupKey: g.Key, Source: watch.SourceDefault, Counts: map[string]int{}}
	levels := map[int64]watch.Level{}
	watches := map[string]bool{w.ID: true}
	for _, e := range g.Entries {
		levels[e.Seq] = e.Level
		if grade != nil && e.Reason == "" {
			levels[e.Seq] = grade.Level
		}
	}
	if grade != nil {
		d.Source, d.Fallback = grade.Source, grade.Reason
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
	entries := slices.Clone(g.Entries)
	for key, sibling := range w.Groups {
		if key == g.Key {
			continue
		}
		for _, e := range sibling.Entries {
			if _, shared := levels[e.Seq]; shared {
				entries = append(entries, e)
			}
		}
	}
	for _, e := range entries {
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
	if len(d.Reasons) > 0 {
		d.Source, d.Fallback = watch.SourceRule, ""
	}
	return d
}

// attachWatchItem decides whether a delivery opens a Notifications item or
// updates the open one: wake and escalate for the operator, and escalate for
// any other owner (escalate is wake plus an operator item). It returns the
// item to open, or nil when the delivery updates an open item or has none.
func (s *Store) attachWatchItem(d *WatchDelivery, w *Watch, now time.Time) *WatchItem {
	if d.Owner.Kind == watch.OwnerOperator && d.Level.Rank() < watch.Wake.Rank() || d.Owner.Kind != watch.OwnerOperator && d.Level != watch.Escalate {
		return nil
	}
	item := &WatchItem{ID: newID("witem"), WatchID: w.ID, Owner: d.Owner, GroupKey: d.GroupKey, Kind: d.Kind, Level: d.Level, Count: d.Count, Counts: graphClone(d.Counts), Reasons: slices.Clone(d.Reasons), FirstSeq: d.FirstSeq, LastSeq: d.LastSeq}
	if open := s.watchItems[s.watchOpenItems[w.ID+"\x00"+d.GroupKey]]; open != nil && open.State == "open" {
		repeat := time.Duration(w.Spec.Grouping.RepeatIntervalMS) * time.Millisecond
		if now.Before(open.OpenedAt.Add(repeat)) {
			d.ItemID = open.ID
			return nil
		}
		item.Supersedes = open.ID
	}
	d.ItemID = item.ID
	return item
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

// cloneDelivery copies a receipt under the store lock, detaching the Counts
// map a later coalescing delivery mutates in place, so serialization outside
// the lock never shares state with the watch loop.
func cloneDelivery(d *WatchDelivery) WatchDelivery {
	out := *d
	out.Counts = maps.Clone(d.Counts)
	return out
}

// WatchDeliveries returns a watch's receipts, newest first, with the
// outcome and mark each collected.
func (s *Store) WatchDeliveries(id string, limit int) ([]WatchDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watches[id] == nil {
		return nil, ErrNotFound
	}
	ids := s.watchOrder[id]
	out := []WatchDelivery{}
	for i := len(ids) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, cloneDeliveryWithMarks(s, s.watchDeliveries[ids[i]]))
	}
	return out, nil
}

// WatchItemView is a Notifications item with its watch's description.
type WatchItemView struct {
	WatchItem
	Description string `json:"description"`
	ProjectID   string `json:"project_id,omitempty"`
}

// OperatorWatchItems lists the open Notifications items, newest update first.
func (s *Store) OperatorWatchItems() []WatchItemView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []WatchItemView{}
	for _, id := range s.watchOpenItems {
		item := s.watchItems[id]
		if item == nil || item.State != "open" {
			continue
		}
		w := s.watches[item.WatchID]
		out = append(out, WatchItemView{WatchItem: graphClone(*item), Description: w.Spec.Description, ProjectID: w.Spec.Condition.Scope.Project})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// WatchItems returns a watch's Notifications items, newest first.
func (s *Store) WatchItems(watchID string) ([]WatchItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watches[watchID] == nil {
		return nil, ErrNotFound
	}
	out := []WatchItem{}
	for _, item := range s.watchItems {
		if item.WatchID == watchID {
			out = append(out, graphClone(*item))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenedAt.After(out[j].OpenedAt) })
	return out, nil
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
			// Budget evaluation shares the watch clock (#128): conditions,
			// judgments and wrap-up grace advance with deliveries.
			if err := e.evaluateAttemptBudgets(now.UTC()); err != nil && errors.Is(err, errJournalClosed) {
				return
			}
			if err := e.evaluateWatches(e.ctx, now.UTC(), false); err != nil && errors.Is(err, errJournalClosed) {
				return
			}
		}
	}
}
