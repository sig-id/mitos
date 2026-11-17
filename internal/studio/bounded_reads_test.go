package studio

import (
	"testing"
)

func TestBoundedReadsKeepSeqCursorsAndTenantFeedAcrossReplay(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	s := e.Store
	alpha, token := serviceTestPrincipal(t, s, "alpha", "a", true)
	beta, _ := serviceTestPrincipal(t, s, "beta", "b", true)
	view := e.Application(alpha)
	var ids []string
	for i, key := range []string{"one", "two", "three"} {
		r, err := view.Admit(structuredBrief(key, "agent:mitos.generalist"))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
		for range i + 1 {
			if err = s.RecordDecision(r.ID, "observe", "tenant A"); err != nil {
				t.Fatal(err)
			}
		}
	}
	foreign, err := e.Application(beta).Admit(structuredBrief("foreign", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDecision(foreign.ID, "observe", "secret B"); err != nil {
		t.Fatal(err)
	}
	cursor := int64(0)
	afterID := ""
	seen := map[string]bool{}
	for {
		page, err := view.RunPage(cursor, afterID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Runs) > 1 {
			t.Fatal("unbounded runs")
		}
		for _, r := range page.Runs {
			if seen[r.ID] || r.ID == foreign.ID || len(r.Events) != 0 {
				t.Fatal("duplicate/foreign/unbounded run")
			}
			seen[r.ID] = true
		}
		if !page.HasMore {
			break
		}
		if page.Next < cursor {
			t.Fatal("cursor regressed")
		}
		cursor, afterID = page.Next, page.NextID
	}
	if len(seen) != 3 {
		t.Fatal("run page skipped data", seen)
	}
	cursor = 0
	events := []Event{}
	for {
		page, err := view.Feed(cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) > 2 {
			t.Fatal("unbounded feed")
		}
		for _, event := range page.Events {
			if event.Seq <= cursor || event.RunID == foreign.ID {
				t.Fatal("foreign or repeated event")
			}
			events = append(events, event)
		}
		cursor = page.Next
		if !page.HasMore {
			break
		}
	}
	if len(events) != 9 {
		t.Fatal("missing creation/progress events", len(events))
	}
	page, err := s.EventPage(ids[2], 0, 1)
	if err != nil || len(page.Events) != 1 || !page.HasMore {
		t.Fatal("event bound", page, err)
	}
	for _, limit := range []int{0, ReadMaxLimit + 1} {
		if _, err = view.Feed(0, limit); err == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	if _, err = view.RunPage(-1, "", 1); err == nil {
		t.Fatal("negative cursor accepted")
	}
	before := cursor
	e.Close()
	s.Close()
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	p, err := reopened.AuthenticateServicePrincipal(token)
	if err != nil {
		t.Fatal(err)
	}
	if page, err := reopened.As(p).Feed(before, 2); err != nil || len(page.Events) != 0 {
		t.Fatal("feed cursor lost on replay", err)
	}
	if page, err := reopened.As(p).Feed(0, ReadMaxLimit); err != nil || len(page.Events) != 9 {
		t.Fatal("feed index not replayed", err)
	}
}
