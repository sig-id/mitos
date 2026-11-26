package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

// The oracle tampering check re-captures the installed acceptance tree after
// execution and compares fingerprints with the intake capture (CaptureSource).
// The re-capture must keep CaptureSource's semantics — every real file — not
// CaptureResult's derivable exclusions, or an installed __pycache__ would
// look like tampering (found by the strict audit of issue #137's fix).
func TestCaptureInstalledMatchesCaptureSourceSemantics(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "app")
	writeTree(t, source, map[string]string{
		"check.sh":              "#!/bin/sh\nexit 0",
		"helper/lib.py":         "def run(): pass",
		"__pycache__/lib.pyc":   "\x00bytecode",
		"node_modules/dep/i.js": "installed dependency",
		"target/release/check":  "binary-ish",
	})
	intake, err := CaptureSource(context.Background(), source, filepath.Join(base, "snap"), DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	recaptured, err := CaptureInstalled(context.Background(), source, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if recaptured.Fingerprint != intake.Fingerprint {
		t.Fatalf("installed re-capture diverged from intake: %s vs %s", recaptured.Fingerprint, intake.Fingerprint)
	}
	if recaptured.OmittedDerivable != nil {
		t.Fatalf("installed capture must not exclude derivable directories: %v", recaptured.OmittedDerivable)
	}
	// Result captures use the excluding semantics; that difference is exactly
	// why the oracle path must not use them.
	result, err := CaptureResult(context.Background(), source, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fingerprint == intake.Fingerprint || result.OmittedDerivable == nil {
		t.Fatalf("result capture did not exclude derivable content: %+v", result.OmittedDerivable)
	}
	// Data captures keep their documented every-file semantics.
	data, err := CaptureData(context.Background(), source, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if data.Fingerprint != intake.Fingerprint || data.OmittedDerivable != nil {
		t.Fatalf("data capture lost its every-file semantics: %v", data.OmittedDerivable)
	}
}
