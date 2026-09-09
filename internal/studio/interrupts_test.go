package studio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInterruptReportCountsHumanResolutionsAndExcludesSamples(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	d := recoveryDecision()
	if err := s.Park(r.ID, d); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(r.ID, d.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := s.Resolve(r.ID, d.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	unresolved := createRun(t, s)
	if err := s.Park(unresolved.ID, observationDecision(errBlockedOnHuman)); err != nil {
		t.Fatal(err)
	}
	sample, err := s.Create(testBrief("."), "Example", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Park(sample.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	report := ReportInterrupts(s.List())
	if report.Requested != 2 || report.HumanTouches != 1 || report.Unresolved != 1 || report.SampleRunsExcluded != 1 || report.SufficientForCalibration {
		t.Fatalf("incorrect report: %+v", report)
	}
	rescue := report.Distribution[3]
	if rescue.Class != "rescue" || rescue.Requests != 1 || rescue.Touches != 1 || rescue.Fraction == nil || *rescue.Fraction != 1 {
		t.Fatalf("wrong distribution: %+v", rescue)
	}
	// A reader must work while the writer is open, without taking its lock or
	// changing the canonical bytes; the live API and offline CLI share the fold.
	before, err := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditInterruptLogs(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report, offline) {
		t.Fatalf("offline/live reports differ: %+v %+v", report, offline)
	}
	after, _ := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
	if string(before) != string(after) {
		t.Fatal("audit changed canonical log")
	}
}

func TestInterruptAuditRejectsCorruptionAndNeverRepairsPartialTail(t *testing.T) {
	s := testStore(t)
	createRun(t, s)
	path := filepath.Join(s.Dir(), "events.jsonl")
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	audit := filepath.Join(t.TempDir(), "events.jsonl")
	for _, bad := range [][]byte{append(append([]byte{}, good...), []byte(`{"seq":2`)...), []byte(strings.Replace(string(good), "Inspect the workspace", "Altered the workspace", 1))} {
		if err = os.WriteFile(audit, bad, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = AuditInterruptLogs(audit); err == nil {
			t.Fatal("corrupt log accepted")
		}
		after, _ := os.ReadFile(audit)
		if string(after) != string(bad) {
			t.Fatal("audit repaired the log")
		}
	}
}

func TestHistoricalEvidenceDoesNotInventCalendarCoverageOrClassifications(t *testing.T) {
	dir := t.TempDir()
	row := map[string]any{"schema_version": 1, "sequence": 0, "timestamp_ms": 1, "run_id": "old", "type": "run_created"}
	b, _ := json.Marshal(row)
	b = append(b, '\n')
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), b, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := AuditInterruptLogs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.LegacyEvents != 1 || report.Runs != 1 || report.FirstAt != nil || report.HumanTouches != 0 || report.SufficientForCalibration {
		t.Fatalf("legacy evidence overstated: %+v", report)
	}
	for _, bucket := range report.Distribution {
		if bucket.Fraction != nil {
			t.Fatal("invented distribution with no touches")
		}
	}
}
