package studio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/autonomy"
	"mitos/internal/fleet"
)

func TestDispatchClaimsSerializeUntilHarvestAndAllowDisjointPaths(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `sleep 1; printf result > result.txt`)
	e, s := testEngine(t, root, []Application{app})
	first := launchApp(t, e, root, app)
	first = waitRun(t, s, first.ID, func(r Run) bool { return r.Claim != nil })
	second := launchApp(t, e, root, app)
	second = waitRun(t, s, second.ID, func(r Run) bool { return len(r.ClaimConflicts) > 0 })
	if second.Status != "queued" || len(second.Segments) != 0 || second.ClaimConflicts[0].RunID != first.ID {
		t.Fatal("conflicting run dispatched")
	}
	if e.ReleaseInspectedClaims(first.ID) == nil {
		t.Fatal("released active execution")
	}
	first = waitRecovery(t, s, first.ID)
	second = waitRecovery(t, s, second.ID)
	if first.Status != "done" || second.Status != "done" || first.Claim.ReleasedAt == nil || second.Claim.ReleasedAt == nil {
		t.Fatal("claims did not release with completed work")
	}
	if second.Claim.AcquiredAt.Before(*first.Claim.ReleasedAt) {
		t.Fatal("claims overlapped")
	}
	count := 0
	for _, event := range second.Events {
		if event.Kind == "dispatch_conflict" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("conflict repeated each tick: %d", count)
	}
	if _, err := os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("source changed")
	}

	// Path-disjoint siblings start together even when they share a source root.
	appA := executionApp(t, `sleep 1; printf a > a`)
	appA.ID = "path-a"
	appB := executionApp(t, `sleep 1; printf b > b`)
	appB.ID = "path-b"
	appA.Acceptance = acceptance.Spec{Cmd: []string{"/bin/sh", "-c", `test -s "$MITOS_WORKSPACE/a"`}}
	appB.Acceptance = acceptance.Spec{Cmd: []string{"/bin/sh", "-c", `test -s "$MITOS_WORKSPACE/b"`}}
	e2, s2 := testEngine(t, root, []Application{appA, appB})
	launch := func(app Application, path string) Run {
		b := testBrief(root)
		b.AppID = app.ID
		b.Ownership.Paths = []string{path}
		r, err := e2.Launch(b, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, b := launch(appA, "a"), launch(appB, "b")
	a = waitRecovery(t, s2, a.ID)
	b = waitRecovery(t, s2, b.ID)
	if a.Status != "done" || b.Status != "done" || a.Claim.AcquiredAt.After(*b.Claim.ReleasedAt) || b.Claim.AcquiredAt.After(*a.Claim.ReleasedAt) {
		t.Fatal("disjoint claims did not overlap")
	}
}

func TestOwnershipViolationPreservesPatchAndWithholdsAcceptance(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf result > result.txt; printf outside > other.txt`)
	e, s := testEngine(t, root, []Application{app})
	b := testBrief(root)
	b.AppID = app.ID
	b.Ownership.Paths = []string{"result.txt"}
	r, err := e.Launch(b, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRecovery(t, s, r.ID)
	if r.Status != "parked" || r.Verified || r.Acceptance != nil || r.Decision.ReasonSignal != "ownership_violation" || r.Claim.ReleasedAt == nil {
		t.Fatalf("scope violation accepted: %s %s", r.Status, r.Summary)
	}
	if _, err = s.Artifact(r.ID, "changes.patch"); err != nil {
		t.Fatal(err)
	}
}

func TestCrashClaimsRetainedUntilInspectionAndReplayed(t *testing.T) {
	root := t.TempDir()
	s := testStore(t)
	apps, _ := LoadApps("")
	b := testBrief(root)
	b.Ownership = fleet.Ownership{Paths: []string{"."}, Leases: []string{"shared-db"}}
	r, err := s.Create(b, apps[0].Name, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AcquireClaims(r.ID); err != nil || !ok {
		t.Fatal(err)
	}
	// Reopen the durable prefix where dispatch acquired ownership but no exit was observed.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	e, err := NewEngine(s, apps, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	r, _ = s.Get(r.ID)
	if r.Status != "parked" || r.Claim.ReleasedAt != nil {
		t.Fatal("restart released uncertain execution")
	}
	child, err := e.Launch(b, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitRun(t, s, child.ID, func(r Run) bool { return len(r.ClaimConflicts) > 0 })
	if err = e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(r.ID)
	if r.Claim.ReleasedAt != nil {
		t.Fatal("cancelling orphan released claims")
	}
	if err = e.ReleaseInspectedClaims(r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Get(r.ID)
	if r.Claim.ReleaseReason != "operator_confirmed_stopped" {
		t.Fatal("missing inspection provenance")
	}
	seq := len(r.Events)
	if err = e.ReleaseInspectedClaims(r.ID); err != nil {
		t.Fatal(err)
	}
	again, _ := s.Get(r.ID)
	if len(again.Events) != seq {
		t.Fatal("duplicate inspection mutated record")
	}
	child = waitRecovery(t, s, child.ID)
	if child.Status != "done" {
		t.Fatal("released work did not dispatch")
	}
	e.Close()
	live, _ := s.Get(r.ID)
	want, _ := json.Marshal(live.Claim)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, _ := reopened.Get(r.ID)
	got, _ := json.Marshal(replayed.Claim)
	if string(got) != string(want) {
		t.Fatal("claim release did not replay")
	}
}

func TestCancellationReleasesManagedClaims(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `printf result > result.txt; sleep 30`)
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	waitRun(t, s, r.ID, func(r Run) bool { return len(r.Segments) > 0 })
	if err := e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	r = waitRecovery(t, s, r.ID)
	if r.Status != "cancelled" || r.Claim == nil || r.Claim.ReleasedAt == nil || r.Claim.ReleasedAt.After(time.Now()) {
		t.Fatal("managed cancellation left claims held")
	}
}

func TestSingletonLeaseCoordinatesDifferentSourceRoots(t *testing.T) {
	s := testStore(t)
	create := func(root string) Run {
		b := testBrief(root)
		b.Ownership = fleet.Ownership{Paths: []string{"."}, Leases: []string{"database:shared"}}
		r, err := s.Create(b, "Inventory", "", false)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, b := create(t.TempDir()), create(t.TempDir())
	if ok, err := s.AcquireClaims(a.ID); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := s.AcquireClaims(b.ID); err != nil || ok {
		t.Fatal("shared lease did not block")
	}
	b, _ = s.Get(b.ID)
	if len(b.ClaimConflicts) != 1 || b.ClaimConflicts[0].Kind != "lease" {
		t.Fatal("incorrect conflict")
	}
	if err := s.ReleaseClaims(a.ID, "execution_stopped_and_harvest_finished"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AcquireClaims(b.ID); err != nil || !ok {
		t.Fatal("released lease did not acquire")
	}
	b, _ = s.Get(b.ID)
	if len(b.ClaimConflicts) != 0 {
		t.Fatal("stale conflict remained")
	}
}

func TestCrashDuringQueuedRestoreRequiresInspectionAndResumesSavedDecision(t *testing.T) {
	root := t.TempDir()
	app := supervisedApp(t, root)
	app.Supervision.Strategy = "escalate"
	app.Supervision.MaxRestarts = limit(1)
	e, s := testEngine(t, root, []Application{app})
	r := launchApp(t, e, root, app)
	r = waitRecovery(t, s, r.ID)
	e.Close()
	request := autonomy.Rewind{CheckpointID: r.Checkpoints[0].ID, FromSegment: 1, Scope: "step", Memo: "Resume the exact saved restoration.", ReasonSignal: "operator_recovery"}
	if _, err := s.BeginRewind(r.ID, request); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AcquireClaims(r.ID); err != nil || !ok {
		t.Fatal(err)
	}
	// Automatic recovery can publish queued while its execution still holds claims.
	if err := s.Transition(r.ID, "restore_queued", "queued", "Restoring", "Recovery pending"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e2, err := NewEngine(reopened, []Application{app}, root)
	if err != nil {
		t.Fatal(err)
	}
	defer e2.Close()
	parked, _ := reopened.Get(r.ID)
	if parked.Status != "parked" || parked.Claim.ReleasedAt != nil || !parked.Decision.HasOption("rewind") {
		t.Fatal("pending crash cannot be inspected and resumed")
	}
	if _, err = e2.Rewind(r.ID, request); err == nil {
		t.Fatal("recovery bypassed held claim inspection")
	}
	if err = e2.ReleaseInspectedClaims(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = e2.Resolve(r.ID, parked.Decision.ID, "rewind"); err != nil {
		t.Fatal(err)
	}
	final := waitRecovery(t, reopened, r.ID)
	if final.Status != "done" || len(final.Rewinds) != 1 || final.Rewinds[0].Memo != request.Memo || final.Execution.Deadline != r.Execution.Deadline || final.Decision.Choice != "rewind" {
		t.Fatalf("saved recovery lost: %s %s", final.Status, final.Summary)
	}
}
