//go:build !linux && !darwin

package desk

// processSnapshot has no portable enumerator on this platform; session
// cleanup falls back to the process-group kill alone.
func processSnapshot() []Process { return []Process{} }
