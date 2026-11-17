// Package failpoint provides opt-in process crashes for commit-boundary fixtures.
// Both environment variables must match explicitly; normal runs are unaffected.
package failpoint

import "os"

const ExitCode = 86

// Hit exits without running defers, modelling process loss rather than a clean
// shutdown. Fixture subprocesses must set MITOS_ENABLE_FAILPOINTS=1 and name one
// exact boundary in MITOS_FAILPOINT. Never enable this in a serving deployment.
func Hit(name string) {
	if name != "" && os.Getenv("MITOS_ENABLE_FAILPOINTS") == "1" && os.Getenv("MITOS_FAILPOINT") == name {
		os.Exit(ExitCode)
	}
}
