package autonomy

import (
	"testing"
	"time"
)

func TestHorizonRequiresEveryIndependentAdmissionFact(t *testing.T) {
	at := time.Unix(100, 0)
	eligible := Admission{true, true, true, true, true}
	grant := GrantHorizon(at, at.Add(time.Hour), at.Add(time.Minute), eligible)
	if grant.DurationMS != 60000 || !grant.UnattendedUntil.Equal(at.Add(time.Minute)) {
		t.Fatal("deadline did not bound the grant")
	}
	for _, remove := range []func(*Admission){func(a *Admission) { a.StructuredObservation = false }, func(a *Admission) { a.ObservationContract = false }, func(a *Admission) { a.MachinePredicate = false }, func(a *Admission) { a.OracleIsolated = false }, func(a *Admission) { a.WorkspaceConfined = false }} {
		a := eligible
		remove(&a)
		grant := GrantHorizon(at, at.Add(time.Hour), at.Add(time.Hour), a)
		if grant.DurationMS != 0 || !grant.UnattendedUntil.Equal(at) || len(grant.Reasons) == 0 {
			t.Fatal("unproven boundary earned a horizon")
		}
	}
	if grant := GrantHorizon(at, at.Add(time.Hour), at, eligible); grant.DurationMS != 0 {
		t.Fatal("expired deadline earned a horizon")
	}
}
