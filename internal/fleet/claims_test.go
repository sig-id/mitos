package fleet

import (
	"strings"
	"testing"
)

func TestOwnershipValidationAndOverlap(t *testing.T) {
	for _, p := range []string{"../outside", "/absolute", "src/../other", "src/*", "src/", ""} {
		err := (Ownership{Paths: []string{p}}).Validate()
		if err == nil {
			t.Errorf("accepted %q", p)
			continue
		}
		// The refusal names the offending entry so both the operator and an
		// in-session orchestrator can correct it in one step.
		if p != "" && !strings.Contains(err.Error(), p) {
			t.Errorf("refusal %q does not name %q", err, p)
		}
	}
	if err := (Ownership{Paths: []string{"src", "src"}}).Validate(); err == nil || !strings.Contains(err.Error(), "src") {
		t.Errorf("duplicate path: %v", err)
	}
	if err := (Ownership{Leases: []string{"db", "db"}}).Validate(); err == nil || !strings.Contains(err.Error(), "db") {
		t.Errorf("duplicate lease: %v", err)
	}
	held := []Claim{{RunID: "one", Workspace: "/repo", Ownership: Ownership{Paths: []string{"src"}, Leases: []string{"database:test"}}}}
	for _, test := range []struct {
		workspace, path, lease string
		want                   int
	}{
		{"/repo", "src/a", "", 1}, {"/repo", "src2", "", 0}, {"/repo/src", ".", "", 1}, {"/", "repo", "", 1},
		{"/other", ".", "database:test", 1}, {"/other", ".", "database:other", 0},
	} {
		o := Ownership{Paths: []string{test.path}}
		if test.lease != "" {
			o.Leases = []string{test.lease}
		}
		if got := len(Conflicts(test.workspace, o, held)); got != test.want {
			t.Errorf("%+v: %d conflicts", test, got)
		}
	}
	if !(Ownership{Paths: []string{"src"}}).Contains("src/a") || (Ownership{Paths: []string{"src"}}).Contains("src2/a") {
		t.Fatal("incorrect prefix boundary")
	}
}
