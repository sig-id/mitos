package watch

import (
	"strings"
	"testing"
	"time"
)

func validSpec(now time.Time) Spec {
	return Spec{
		Version: Version, Owner: Owner{Kind: "operator"}, Description: "payments failures",
		Condition:    Condition{Scope: Scope{Project: "p1"}, Types: []string{"effect"}, Kinds: []string{"verification_failed"}},
		Lease:        Lease{ExpiresAt: now.Add(time.Hour), MaxRenewals: 4},
		Grouping:     Grouping{By: []string{"run_id"}, WaitMS: 2000, IntervalMS: 60000, MaxEvents: 8, RepeatIntervalMS: 3600000},
		DefaultLevel: Notify,
	}
}

func TestValidateRefusesOutOfContractWatches(t *testing.T) {
	now := time.Now()
	if err := validSpec(now).Validate(now); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Spec){
		"version":       func(s *Spec) { s.Version = "" },
		"owner kind":    func(s *Spec) { s.Owner = Owner{Kind: "session", ID: "s1"} },
		"owner id":      func(s *Spec) { s.Owner = Owner{Kind: "attempt"} },
		"operator id":   func(s *Spec) { s.Owner = Owner{Kind: "operator", ID: "x"} },
		"triage":        func(s *Spec) { s.Triage = map[string]any{"policy_ref": "x"} },
		"empty scope":   func(s *Spec) { s.Condition.Scope = Scope{} },
		"type":          func(s *Spec) { s.Condition.Types = []string{"message"} },
		"duplicate":     func(s *Spec) { s.Condition.Kinds = []string{"a", "a"} },
		"lease past":    func(s *Spec) { s.Lease.ExpiresAt = now.Add(-time.Second) },
		"lease too far": func(s *Spec) { s.Lease.ExpiresAt = now.Add(25 * time.Hour) },
		"renewals":      func(s *Spec) { s.Lease.MaxRenewals = 0 },
		"group field":   func(s *Spec) { s.Grouping.By = []string{"project"} },
		"interval":      func(s *Spec) { s.Grouping.IntervalMS = 10 },
		"max events":    func(s *Spec) { s.Grouping.MaxEvents = 1000 },
		"repeat":        func(s *Spec) { s.Grouping.RepeatIntervalMS = 0 },
		"silence":       func(s *Spec) { s.MaxSilenceMS = 1000 },
		"level":         func(s *Spec) { s.DefaultLevel = "drop" },
	}
	for name, mutate := range cases {
		s := validSpec(now)
		mutate(&s)
		if err := s.Validate(now); err == nil {
			t.Errorf("%s: invalid watch accepted", name)
		}
	}
}

func TestMustDeliverFactsBypassTheAuthorsCondition(t *testing.T) {
	s := validSpec(time.Now())
	ordinary := Fact{Project: "p1", Type: "effect", Kind: "run_failed"}
	if _, ok := s.Admit(ordinary); ok {
		t.Fatal("unmatched ordinary fact admitted")
	}
	ruled := ordinary
	ruled.Rule, ruled.Reason = Notify, "run_failed"
	if level, ok := s.Admit(ruled); !ok || level != Notify {
		t.Fatalf("terminal state not delivered: %v %v", level, ok)
	}
	outside := ruled
	outside.Project = "p2"
	if _, ok := s.Admit(outside); ok {
		t.Fatal("must-deliver fact admitted outside the watch scope")
	}
	s.DefaultLevel = Wake
	if level, _ := s.Admit(ruled); level != Wake {
		t.Fatalf("rule level did not rise to the watch default: %v", level)
	}
}

func TestDueBoundsWaitIntervalAndStarvation(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	p := Grouping{WaitMS: 2000, IntervalMS: 10000, MaxEvents: 4, RepeatIntervalMS: 60000}
	g := Group{Entries: []Entry{{Seq: 1, At: t0, Kind: "x", Level: Notify}}}
	if g.Due(t0.Add(1999*time.Millisecond), time.Time{}, p) || !g.Due(t0.Add(2*time.Second), time.Time{}, p) {
		t.Fatal("first delivery does not honour wait_ms")
	}
	// Events keep arriving every second after a delivery: the group is still due
	// once interval_ms has passed since that delivery.
	delivered := t0.Add(2 * time.Second)
	g.Entries = nil
	for i := 1; i <= 3; i++ {
		g.Entries = append(g.Entries, Entry{Seq: int64(i + 1), At: delivered.Add(time.Duration(i) * time.Second), Kind: "x", Level: Notify})
	}
	if g.Due(delivered.Add(9*time.Second), delivered, p) || !g.Due(delivered.Add(10*time.Second), delivered, p) {
		t.Fatal("continuous updates postponed delivery past interval_ms")
	}
	// A wait_ms longer than interval_ms must not stretch a continuing group's
	// cadence to once per wait_ms: after the first delivery only interval_ms
	// applies (spec 09: continuous updates MUST NOT postpone past interval_ms).
	long := Grouping{WaitMS: 60000, IntervalMS: 1000, MaxEvents: 256, RepeatIntervalMS: 60000}
	stream := Group{Entries: []Entry{{Seq: 1, At: delivered.Add(time.Second), Kind: "x", Level: Notify}}}
	if stream.Due(delivered.Add(999*time.Millisecond), delivered, long) || !stream.Due(delivered.Add(time.Second), delivered, long) {
		t.Fatal("wait_ms postponed a continuing group past interval_ms")
	}
	g.Entries = append(g.Entries, Entry{Seq: 9, At: delivered.Add(4 * time.Second), Kind: "x", Level: Notify})
	if !g.Due(delivered.Add(4*time.Second), delivered, p) {
		t.Fatal("a full group is not due immediately")
	}
	urgent := Group{Entries: []Entry{{Seq: 1, At: t0, Kind: "quiescent", Level: Wake, Reason: "quiescence_block"}}}
	if !urgent.Due(t0, time.Time{}, p) {
		t.Fatal("a wake rule waited for wait_ms")
	}
	plain := Group{Entries: []Entry{{Seq: 1, At: t0, Kind: "note", Level: Escalate}}}
	if plain.Due(t0, time.Time{}, p) {
		t.Fatal("an escalate default without a rule skipped wait_ms")
	}
}

func TestGroupKeyAndLevels(t *testing.T) {
	f := Fact{Run: "r1", Task: "t1", Kind: "k"}
	if got := (Grouping{By: []string{"run_id", "kind"}}).GroupKey(f); !strings.Contains(got, "run=r1") || !strings.Contains(got, "kind=k") {
		t.Fatalf("group key %q", got)
	}
	if Max(Digest, Escalate) != Escalate || Max(Wake, Notify) != Wake || Max(Notify, "bogus") != Notify {
		t.Fatal("level order broken")
	}
}

func TestScopeWithinGrant(t *testing.T) {
	grant := Scope{Project: "p", Graph: "g"}
	if !(Scope{Project: "p", Graph: "g", Run: "r"}).Within(grant) {
		t.Fatal("a narrower scope was refused")
	}
	for _, s := range []Scope{{Project: "p"}, {Graph: "other"}, {Project: "q", Graph: "g"}} {
		if s.Within(grant) {
			t.Fatalf("%+v escaped its grant", s)
		}
	}
}
