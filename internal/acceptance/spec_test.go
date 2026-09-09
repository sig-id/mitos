package acceptance

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/workspace"
)

func TestManifestCompatibilityAndCanonicalValidation(t *testing.T) {
	for _, data := range []string{`["/bin/true"]`, `{"cmd":["/bin/true"],"oracle_paths":["tests/","verify.sh"],"timeout_ms":2000,"measure":["/bin/echo","1"]}`} {
		var spec Spec
		if err := json.Unmarshal([]byte(data), &spec); err != nil {
			t.Fatal(err)
		}
		if err := spec.Validate(); err != nil {
			t.Fatal(err)
		}
		canonical, err := json.Marshal(spec)
		if err != nil || !strings.HasPrefix(string(canonical), `{"cmd":`) {
			t.Fatalf("noncanonical manifest: %s %v", canonical, err)
		}
	}
	for _, data := range []string{`{"cmd":["relative"]}`, `{"cmd":["/bin/true"],"oracle_paths":["../tests"]}`, `{"cmd":["/bin/true"],"oracle_paths":["/tests"]}`, `{"cmd":["/bin/true"],"timeout_ms":-1}`, `{"cmd":["/bin/true"],"unknown":true}`} {
		var spec Spec
		err := json.Unmarshal([]byte(data), &spec)
		if err == nil {
			err = spec.Validate()
		}
		if err == nil {
			t.Fatalf("invalid predicate accepted: %s", data)
		}
	}
	if (Spec{}).Timeout() != time.Minute {
		t.Fatal("incorrect legacy timeout")
	}
}

func TestOracleIntersectionsIncludeDeletesModeChangesAndPathBoundaries(t *testing.T) {
	spec := Spec{OraclePaths: []string{"tests/", "verify.sh"}}
	changes := []workspace.Change{{Path: "tests/a.go", Kind: "deleted"}, {Path: "teststuff/a.go", Kind: "modified"}, {Path: "verify.sh", Kind: "modified", BeforeMode: "100644", AfterMode: "100755"}, {Path: "src/a.go", Kind: "added"}}
	touched := spec.Touched(changes)
	if len(touched) != 2 || touched[0] != "tests/a.go" || touched[1] != "verify.sh" {
		t.Fatalf("wrong oracle intersection: %v", touched)
	}
}

func TestMeasuredProgressIsNumericAndDimensionChecked(t *testing.T) {
	before, err := Measurement([]byte(`[1,2]`))
	if err != nil {
		t.Fatal(err)
	}
	after, err := Measurement([]byte(`[3,1]`))
	if err != nil {
		t.Fatal(err)
	}
	delta, err := Delta(before, after)
	if err != nil || len(delta) != 2 || delta[0] != 2 || delta[1] != -1 {
		t.Fatalf("wrong delta: %v %v", delta, err)
	}
	for _, data := range []string{`"passes"`, `{"score":1}`, `[]`, `[1,"x"]`, `1 2`, `1e9999`} {
		if _, err := Measurement([]byte(data)); err == nil {
			t.Fatalf("invalid measurement accepted: %s", data)
		}
	}
	if _, err := Delta([]float64{1}, []float64{1, 2}); err == nil {
		t.Fatal("changed measure dimensions accepted")
	}
}
