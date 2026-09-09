package fleet

import "testing"

func TestOwnershipValidationAndOverlap(t *testing.T) {
	for _, p := range []string{"../outside", "/absolute", "src/../other", "src/*", "src/", ""} {
		if (Ownership{Paths: []string{p}}).Validate() == nil {
			t.Errorf("accepted %q", p)
		}
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
