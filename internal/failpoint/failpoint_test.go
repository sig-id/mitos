package failpoint

import (
	"os"
	"os/exec"
	"testing"
)

func TestHitRequiresExplicitOptInAndExactName(t *testing.T) {
	if os.Getenv("MITOS_FAILPOINT_TEST_CHILD") == "1" {
		Hit("test.boundary")
		return
	}
	for _, tc := range []struct {
		name, enabled, point string
		crash                bool
	}{
		{name: "disabled", point: "test.boundary"},
		{name: "not_explicit", enabled: "true", point: "test.boundary"},
		{name: "different_name", enabled: "1", point: "other.boundary"},
		{name: "no_name", enabled: "1"},
		{name: "enabled", enabled: "1", point: "test.boundary", crash: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MITOS_FAILPOINT_TEST_CHILD", "1")
			t.Setenv("MITOS_ENABLE_FAILPOINTS", tc.enabled)
			t.Setenv("MITOS_FAILPOINT", tc.point)
			cmd := exec.Command(os.Args[0], "-test.run=^TestHitRequiresExplicitOptInAndExactName$")
			err := cmd.Run()
			if tc.crash {
				if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != ExitCode {
					t.Fatalf("expected crash code %d, got %v", ExitCode, err)
				}
			} else if err != nil {
				t.Fatal("disabled failpoint crashed", err)
			}
		})
	}
}
