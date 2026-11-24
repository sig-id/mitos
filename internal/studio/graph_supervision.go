package studio

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"mitos/internal/watch"
)

// The supervision record (spec 09, issue #126): every non-orchestrator agent
// task in an adaptive graph is assigned to exactly one supervising watch from
// admission until it settles. The kernel maintains the assignments and the
// watches' member lists; the orchestrator reads the record and changes it
// through its tools and plan operations.

// supervisionTemplateNames lists the kernel's watch templates; the
// watch_create tool description is derived from it.
var supervisionTemplateNames = []string{"child-terminal-states"}

// watchTemplate builds a watch spec from a named template for a graph. Every
// template is owned by the graph's responsibility so supervision survives
// orchestrator replacement.
func watchTemplate(name, graphID string) (watch.Spec, error) {
	switch name {
	case "child-terminal-states":
		// Terminal states land in the owner's inbox at notify — the kernel
		// already replans on them — while stalls and budget overruns wake
		// their owner by rule whatever this watch's filters say.
		return watch.Spec{
			Version:      watch.Version,
			Owner:        watch.Owner{Kind: watch.OwnerResponsibility, ID: graphID},
			Description:  "Supervise member tasks: terminal states, stalls and budget overruns",
			Condition:    watch.Condition{Scope: watch.Scope{Graph: graphID}, Kinds: []string{"run_finished", "run_failed", "run_cancelled", "run_parked", "attempt_overrun", "attempt_stalled"}},
			Grouping:     watch.Grouping{WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
			DefaultLevel: watch.Notify,
		}, nil
	}
	return watch.Spec{}, fmt.Errorf("unknown watch template %q", name)
}

// GraphWatchSelection is the watch field of an add, supersede or retry
// operation: assign an existing watch, or create one from a template or spec.
type GraphWatchSelection struct {
	Assign string       `json:"assign,omitempty"`
	Create *WatchCreate `json:"create,omitempty"`
}

// supervisedMember reports whether a member is an agent task the kernel must
// keep under a supervising watch.
func supervisedMember(g *WorkGraph, m *GraphMember) bool {
	return g.Policy.Mode == "adaptive" && m != nil && m.OrchestratorFor == "" &&
		m.Spec.Kind == "application" && m.Executor != nil && m.Executor.Application.Agent != nil
}

// supervisionSettled reports whether a member no longer needs supervision:
// accepted, being closed, or replaced.
func supervisionSettled(m *GraphMember) bool {
	return m == nil || m.Accepted != nil || m.CancelRequested || m.Replacement != ""
}

// watchMembersLive reports whether any of a watch's member tasks still needs
// supervision. Caller holds the store lock.
func (s *Store) watchMembersLive(w *Watch) bool {
	if !w.Spec.Condition.IsSupervision() {
		return false
	}
	g := s.graphs[w.Grant.Graph]
	if g == nil || g.Status != "active" {
		return false
	}
	for _, tid := range w.Membership() {
		if m := g.Members[tid]; m != nil && !supervisionSettled(m) {
			return true
		}
	}
	return false
}

// Membership returns the watch's supervised member tasks.
func (w *Watch) Membership() []string { return w.Spec.Condition.Members }

// supervisionWatchChange records one kernel mutation of a watch's member
// list; mutations are committed in the same journal record as the graph
// change that admits, moves or settles the member.
type supervisionWatchChange struct {
	Watch  string   `json:"watch_id"`
	Add    []string `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
	Kernel bool     `json:"kernel,omitempty"`
}

// planSupervision assigns the members an amendment touches to their
// supervising watches, pre-creating kernel watch records so the assignment,
// the member and its watch commit in one journal record (#126). It mutates
// the cloned graph under construction and returns the watch records to
// commit with it; nothing is committed here, so a refused plan leaves no
// watch behind. Caller holds the store lock.
func (s *Store) planSupervision(g *WorkGraph, assigns, settles []string, request map[string]*GraphWatchSelection) (*watchChange, error) {
	var memberships []supervisionWatchChange
	pending := map[string]*Watch{}
	record := func(watchID, add, remove string) {
		for i := range memberships {
			if memberships[i].Watch != watchID {
				continue
			}
			if add != "" && !slices.Contains(memberships[i].Add, add) {
				memberships[i].Add = append(memberships[i].Add, add)
			}
			if remove != "" && !slices.Contains(memberships[i].Remove, remove) {
				memberships[i].Remove = append(memberships[i].Remove, remove)
			}
			return
		}
		m := supervisionWatchChange{Watch: watchID, Kernel: true}
		if add != "" {
			m.Add = []string{add}
		}
		if remove != "" {
			m.Remove = []string{remove}
		}
		memberships = append(memberships, m)
	}
	newKernelWatch := func(spec watch.Spec, template string) *Watch {
		if spec.Condition.Scope.Project == "" {
			spec.Condition.Scope.Project = g.ProjectID
		}
		spec.Lease.ExpiresAt = time.Now().UTC().Add(watch.MaxLease)
		created := &Watch{ID: newID("watch"), Spec: spec, State: "active", Grant: watch.Scope{Project: g.ProjectID, Graph: g.ID}, Template: template, CreatedAt: g.UpdatedAt, CreatedSeq: s.seq + 1}
		created.Groups, created.LastDelivered = map[string]*watch.Group{}, map[string]time.Time{}
		pending[created.ID] = created
		return created
	}
	resolve := func(tid string) (string, error) {
		var id, template string
		var create *WatchCreate
		if sel := request[tid]; sel != nil {
			id, create = sel.Assign, sel.Create
			if create != nil {
				template = create.Template
			}
		}
		if id != "" {
			w := s.watches[id]
			if w == nil {
				w = pending[id]
			}
			if w == nil || w.State != "active" || w.Spec.Owner.Kind != watch.OwnerResponsibility || w.Spec.Owner.ID != g.ID {
				return "", graphError("watch assignment must name an active watch of this graph's responsibility")
			}
			return id, nil
		}
		if create != nil {
			spec, err := s.watchCreateSpec(*create, g, nil)
			if err != nil {
				return "", err
			}
			spec.Condition.Members = []string{tid}
			if equivalent := s.equivalentSupervisionWatchLocked(spec, template); equivalent != "" {
				return equivalent, nil
			}
			for _, w := range pending {
				if template != "" && w.Template == template || watchConditionsEqual(w.Spec.Condition, spec.Condition) {
					return w.ID, nil
				}
			}
			return newKernelWatch(spec, template).ID, nil
		}
		if g.SupervisionWatch != "" {
			if w := s.watches[g.SupervisionWatch]; w != nil && w.State == "active" {
				return g.SupervisionWatch, nil
			}
			if w := pending[g.SupervisionWatch]; w != nil {
				return g.SupervisionWatch, nil
			}
		}
		spec, err := watchTemplate("child-terminal-states", g.ID)
		if err != nil {
			return "", err
		}
		spec.Condition.Members = []string{tid}
		if equivalent := s.equivalentSupervisionWatchLocked(spec, "child-terminal-states"); equivalent != "" {
			g.SupervisionWatch = equivalent
			return equivalent, nil
		}
		created := newKernelWatch(spec, "child-terminal-states")
		g.SupervisionWatch = created.ID
		return created.ID, nil
	}
	for _, tid := range assigns {
		m := g.Members[tid]
		if !supervisedMember(g, m) {
			continue
		}
		target, err := resolve(tid)
		if err != nil {
			return nil, err
		}
		if old := m.SupervisionWatch; old != "" && old != target {
			record(old, "", tid)
			if w := pending[old]; w != nil && !slices.Contains(w.Spec.Condition.Members, tid) {
				w.Spec.Condition.Members = append(w.Spec.Condition.Members, tid)
			}
		}
		m.SupervisionWatch = target
		if w := pending[target]; w != nil && !slices.Contains(w.Spec.Condition.Members, tid) {
			w.Spec.Condition.Members = append(w.Spec.Condition.Members, tid)
		}
		record(target, tid, "")
	}
	for _, tid := range settles {
		m := g.Members[tid]
		if m == nil || m.SupervisionWatch == "" {
			continue
		}
		record(m.SupervisionWatch, "", tid)
	}
	if len(pending) == 0 && len(memberships) == 0 {
		return nil, nil
	}
	change := &watchChange{Memberships: memberships}
	ids := make([]string, 0, len(pending))
	for id := range pending {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		change.Creates = append(change.Creates, pending[id])
	}
	return change, nil
}

// watchCreateSpec derives a validated watch spec from a tool or plan request,
// clamped to the graph. Empty fields inherit the template's values. A tool
// call supplies its caller; a plan-created watch belongs to the graph's
// responsibility without a caller.
func (s *Store) watchCreateSpec(in WatchCreate, g *WorkGraph, c *watchCaller) (watch.Spec, error) {
	attempt := ""
	if c != nil {
		if owner, err := c.owner(in.Owner); err != nil {
			return watch.Spec{}, err
		} else if owner.Kind == watch.OwnerAttempt {
			attempt = owner.ID
		}
	} else if in.Owner == watch.OwnerAttempt {
		return watch.Spec{}, &HandoffFailure{Code: "invalid_owner", Detail: "an attempt watch needs a running attempt"}
	}
	var base watch.Spec
	if in.Template != "" {
		t, err := watchTemplate(in.Template, g.ID)
		if err != nil {
			return watch.Spec{}, err
		}
		base = t
	}
	owner := watch.Owner{Kind: watch.OwnerResponsibility, ID: g.ID}
	if attempt != "" {
		owner = watch.Owner{Kind: watch.OwnerAttempt, ID: attempt}
	}
	condition := base.Condition
	if in.Condition.Scope != (watch.Scope{}) || len(in.Condition.Types) > 0 || len(in.Condition.Kinds) > 0 || in.Condition.Members != nil {
		condition = in.Condition
	}
	scope := &condition.Scope
	for _, f := range []struct {
		field *string
		value string
	}{{&scope.Project, g.ProjectID}, {&scope.Graph, g.ID}} {
		if *f.field != "" && *f.field != f.value {
			return watch.Spec{}, &HandoffFailure{Code: "scope_outside_grant", Detail: "watch scope must stay within your graph"}
		}
		*f.field = f.value
	}
	if condition.Members != nil {
		return watch.Spec{}, &HandoffFailure{Code: "members_kernel_only", Detail: "condition.members is maintained by the kernel; assign tasks through plan operations or watch_assign"}
	}
	description := in.Description
	if description == "" {
		description = base.Description
	}
	grouping := in.Grouping
	if grouping.By == nil && grouping.WaitMS == 0 && grouping.IntervalMS == 0 && grouping.MaxEvents == 0 && grouping.RepeatIntervalMS == 0 {
		grouping = base.Grouping
	}
	level := in.DefaultLevel
	if level == "" {
		level = base.DefaultLevel
	}
	expires := time.Now().UTC().Add(watch.MaxLease)
	if in.LeaseMS != 0 {
		if in.LeaseMS < 0 || in.LeaseMS > watch.MaxLease.Milliseconds() {
			return watch.Spec{}, errors.New("lease_ms must be 1–86400000")
		}
		expires = time.Now().UTC().Add(time.Duration(in.LeaseMS) * time.Millisecond)
	}
	spec := watch.Spec{Version: watch.Version, Owner: owner, Description: description, Condition: condition, Lease: watch.Lease{ExpiresAt: expires, MaxRenewals: max(in.MaxRenewals, 1)}, Grouping: grouping, MaxSilenceMS: in.MaxSilenceMS, DefaultLevel: level, Triage: in.Triage}
	if err := spec.Validate(time.Now().UTC()); err != nil {
		return watch.Spec{}, err
	}
	return spec, nil
}

// equivalentSupervisionWatchLocked returns an active watch equivalent to the
// requested spec — the graph's watch built from the same template, or one
// with an identical condition and level — so repeated creation returns one
// watch instead of duplicates (#126). Caller holds the store lock.
func (s *Store) equivalentSupervisionWatchLocked(spec watch.Spec, template string) string {
	for _, w := range s.watches {
		if w.State != "active" || w.Spec.Owner.Key() != spec.Owner.Key() {
			continue
		}
		if template != "" && w.Template == template && w.Grant.Graph == spec.Condition.Scope.Graph {
			return w.ID
		}
		if w.Template == "" && w.Spec.DefaultLevel == spec.DefaultLevel && watchConditionsEqual(w.Spec.Condition, spec.Condition) {
			return w.ID
		}
	}
	return ""
}

func watchConditionsEqual(a, b watch.Condition) bool {
	return a.Scope == b.Scope && slices.Equal(a.Types, b.Types) && slices.Equal(a.Kinds, b.Kinds) && slices.Equal(a.Members, b.Members)
}

// SupervisionRecord is the orchestrator-facing view of the supervision
// record: which watches supervise which tasks, their leases and pending
// delivery counts.
type SupervisionRecord struct {
	Graph      string                 `json:"graph"`
	Default    string                 `json:"default_watch,omitempty"`
	Unassigned []string               `json:"unassigned,omitempty"`
	Watches    []SupervisionWatchView `json:"watches"`
}

// SupervisionWatchView is one supervising watch in the record.
type SupervisionWatchView struct {
	ID           string                  `json:"id"`
	Template     string                  `json:"template,omitempty"`
	Description  string                  `json:"description,omitempty"`
	DefaultLevel watch.Level             `json:"default_level"`
	LeaseExpiry  time.Time               `json:"lease_expires_at"`
	Members      []SupervisionMemberView `json:"members"`
	Pending      int                     `json:"pending"`
}

// SupervisionMemberView is one supervised task in a watch.
type SupervisionMemberView struct {
	Task    string `json:"task"`
	Title   string `json:"title,omitempty"`
	State   string `json:"state"`
	Settled bool   `json:"settled"`
}

// supervisionRecordLocked projects the graph's supervision record. Caller
// holds the store lock.
func (s *Store) supervisionRecordLocked(g *WorkGraph) SupervisionRecord {
	record := SupervisionRecord{Graph: g.ID, Default: g.SupervisionWatch, Watches: []SupervisionWatchView{}}
	seen := map[string]bool{}
	view := func(w *Watch) *SupervisionWatchView {
		for i := range record.Watches {
			if record.Watches[i].ID == w.ID {
				return &record.Watches[i]
			}
		}
		v := SupervisionWatchView{ID: w.ID, Template: w.Template, Description: w.Spec.Description, DefaultLevel: w.Spec.DefaultLevel, LeaseExpiry: w.Spec.Lease.ExpiresAt, Members: []SupervisionMemberView{}}
		if inbox := s.watchInboxes[w.Spec.Owner.Key()]; inbox != nil {
			v.Pending = len(inbox.Pending)
		}
		record.Watches = append(record.Watches, v)
		return &record.Watches[len(record.Watches)-1]
	}
	for _, tid := range graphIDs(g) {
		m := g.Members[tid]
		if !supervisedMember(g, m) {
			continue
		}
		if m.SupervisionWatch == "" || s.watches[m.SupervisionWatch] == nil {
			record.Unassigned = append(record.Unassigned, tid)
			continue
		}
		w := s.watches[m.SupervisionWatch]
		if w == nil {
			continue
		}
		seen[w.ID] = true
		v := view(w)
		v.Members = append(v.Members, SupervisionMemberView{Task: tid, Title: m.Spec.Brief.Title, State: m.State, Settled: supervisionSettled(m)})
	}
	for _, w := range s.watches {
		if w.State != "active" || !seen[w.ID] || w.Grant.Graph != g.ID {
			continue
		}
		view(w)
	}
	return record
}

// supervisionProjection renders the compact supervision record for the
// planning context, ahead of the inbox so the shared prefix stays stable.
// Caller holds the store lock.
func (s *Store) supervisionProjection(g *WorkGraph) any {
	supervised := g.SupervisionWatch != ""
	for _, tid := range graphIDs(g) {
		if m := g.Members[tid]; supervisedMember(g, m) || m != nil && m.SupervisionWatch != "" {
			supervised = true
			break
		}
	}
	if !supervised {
		return nil
	}
	return s.supervisionRecordLocked(g)
}

// assignSupervisionWatch moves a task to another supervising watch through
// the orchestrator's watch_assign tool. The caller commits the returned
// change with the graph change that records the move. Caller holds the store
// lock.
func (s *Store) assignSupervisionWatch(g *WorkGraph, tid, watchID string) (*watchChange, error) {
	m := g.Members[tid]
	if m == nil {
		return nil, ErrNotFound
	}
	if !supervisedMember(g, m) {
		return nil, graphError("only the graph's agent tasks are supervised")
	}
	target := s.watches[watchID]
	if target == nil || target.State != "active" || target.Grant.Graph != g.ID || target.Spec.Owner.Kind != watch.OwnerResponsibility || target.Spec.Owner.ID != g.ID {
		return nil, graphError("watch_assign must name an active watch of this graph's responsibility")
	}
	if m.SupervisionWatch == watchID {
		return nil, nil
	}
	change := &watchChange{}
	if m.SupervisionWatch != "" {
		change.Memberships = append(change.Memberships, supervisionWatchChange{Watch: m.SupervisionWatch, Remove: []string{tid}, Kernel: true})
	}
	change.Memberships = append(change.Memberships, supervisionWatchChange{Watch: watchID, Add: []string{tid}, Kernel: true})
	m.SupervisionWatch = watchID
	return change, nil
}

// reconcileSupervision maintains the invariant on the evaluation loop: the
// kernel renews leases for watches with live members, and ends a watch when
// its last member settled. Caller holds the store lock.
func (s *Store) reconcileSupervision(now time.Time) error {
	ids := make([]string, 0, len(s.watches))
	for id, w := range s.watches {
		if w.State == "active" && w.Spec.Condition.IsSupervision() {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		w := s.watches[id]
		if !s.watchMembersLive(w) {
			// Every member settled: supervision is complete. Pending
			// deliveries stay readable through their receipts and inbox.
			d := s.summaryDelivery(w, "settled", watch.Notify)
			if err := s.commitWatchDelivery("watch_settled", w, &d, &watchEnd{ID: w.ID, State: "settled"}, now); err != nil {
				return err
			}
			continue
		}
		if w.Spec.Lease.ExpiresAt.Sub(now) < time.Hour {
			// Kernel renewal does not consume the owner's allowance.
			expires := now.Add(watch.MaxLease)
			if err := s.commit("", "effect", "watch_renewed", change{Watch: &watchChange{Renew: &watchRenewal{ID: w.ID, ExpiresAt: expires, Kernel: true}}}); err != nil {
				return err
			}
		}
	}
	return nil
}
