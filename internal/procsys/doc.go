// Package procsys supplies child process attributes whose portability differs
// by platform. Linux ties every launched worker to the server's lifetime with
// PDEATHSIG so no orphan survives a crash; platforms without that field launch
// without it and rely on the explicit cancellation and WaitDelay paths.
package procsys
