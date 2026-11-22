package studio

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mitos/internal/desk"
	"mitos/internal/watch"
)

func (e *Engine) MessageResponsibility(project, id, body string) (WatchDelivery, error) {
	d, err := e.Store.messageResponsibility(project, id, body)
	if err == nil && d.Route == routeInbox && d.Trigger == 0 {
		e.Desk.WakeResponsibility(id)
	}
	return d, err
}

// A direct message has no subscription or lease. Its source event, wake and
// receipt are one journal record, including when the pending inbox overflows.
func (s *Store) messageResponsibility(project, id, body string) (WatchDelivery, error) {
	if strings.TrimSpace(body) == "" || len(body) > 8000 || strings.ContainsRune(body, 0) {
		return WatchDelivery{}, errors.New("provide a message of at most 8000 bytes")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.responsibilityInProject(project, id) {
		return WatchDelivery{}, ErrNotFound
	}
	owner := watch.Owner{Kind: watch.OwnerResponsibility, ID: id}
	if g := s.graphs[id]; g != nil {
		if g.Status != "active" || g.Policy.Mode == "defined" {
			return WatchDelivery{}, fmt.Errorf("%w: graph has no active planning responsibility", ErrConflict)
		}
	} else if t := s.tasks[s.deskResponsibilities[id].TaskID]; t == nil || t.Status == "done" || t.Status == "cancelled" {
		return WatchDelivery{}, fmt.Errorf("%w: responsibility is closed", ErrConflict)
	}
	seq := s.seq + 1
	d := WatchDelivery{ID: newID("delivery"), Owner: owner, Kind: "operator_message", Message: body, Level: watch.Wake, Source: watch.SourceRule,
		FirstSeq: seq, LastSeq: seq, Count: 1, Counts: map[string]int{"operator_message": 1}}
	if err := s.commitWatchDelivery("responsibility_message", nil, &d, nil, time.Now().UTC()); err != nil {
		return WatchDelivery{}, err
	}
	return cloneDelivery(s.watchDeliveries[d.ID]), nil
}

func (s *Store) responsibilityInProject(project, id string) bool {
	if g := s.graphs[id]; g != nil {
		return g.ProjectID == project
	}
	r, ok := s.deskResponsibilities[id]
	return ok && r.ProjectID == project
}

func (s *Store) deliveryInterval(watchID string) int64 {
	if w := s.watches[watchID]; w != nil {
		return w.Spec.Grouping.IntervalMS
	}
	return 0 // direct messages can flush as soon as space is available
}

// ResponsibilityMessages pages the immutable receipts, including messages
// folded under inbox pressure and messages the current or a prior owner read.
func (s *Store) ResponsibilityMessages(project, id string, before int64) ([]WatchDelivery, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.responsibilityInProject(project, id) {
		return nil, ErrNotFound
	}
	ids := s.operatorMessages[id]
	end := len(ids)
	if before > 0 {
		end = sort.Search(end, func(i int) bool { return s.watchDeliveries[ids[i]].FirstSeq >= before })
	}
	out := []WatchDelivery{}
	for i := end - 1; i >= 0 && len(out) < 50; i-- {
		out = append(out, cloneDeliveryWithMarks(s, s.watchDeliveries[ids[i]]))
	}
	return out, nil
}

func (s *Store) ResponsibilityNotices(session desk.Session) []desk.Notice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.deskResponsibilities[session.ResponsibilityID]
	if r.OrchestratorSessionID != session.ID || session.Status != "running" {
		return nil
	}
	owner := watch.Owner{Kind: watch.OwnerResponsibility, ID: r.ID}
	in := s.watchInboxes[owner.Key()]
	if in == nil {
		return nil
	}
	var out []desk.Notice
	for _, id := range in.Pending {
		d := s.watchDeliveries[id]
		body := d.Message
		if fold := s.inboxEntries[id].Absorbed; fold != nil {
			body = strings.TrimSpace(fmt.Sprintf("%s\n%d additional events were folded (journal sequences %d–%d).", body, fold.Count, fold.FirstSeq, fold.LastSeq))
		}
		out = append(out, desk.Notice{ID: id, ProjectID: r.ProjectID, Author: "operator", Body: body, CreatedAt: d.At, Targets: []desk.NoticeTarget{{SessionID: session.ID, State: "pending"}}})
		// Leave room in native hooks' bounded response. Acknowledgements
		// advance the window, including across orchestrator replacement.
		if len(out) == 12 {
			break
		}
	}
	return out
}

// A native adapter fetching context leaves the receipt pending. Only an
// explicit acknowledgement by the current session settles the delivery.
func (s *Store) ReceiveResponsibilityNotice(session desk.Session, id string, acknowledge bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.deskResponsibilities[session.ResponsibilityID]
	entry := s.inboxEntries[id]
	if entry == nil || entry.Owner.Kind != watch.OwnerResponsibility || entry.Owner.ID != r.ID {
		return desk.ErrNotFound
	}
	if r.OrchestratorSessionID != session.ID || session.Status != "running" {
		return desk.ErrConflict
	}
	if acknowledge {
		_, err := s.recordDeliveryOutcomeLocked(entry.Owner, int64(r.Generation), id, "acted")
		return err
	}
	return nil
}
