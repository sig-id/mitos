package compute

// Manual smoke test for ADR 0011's observation downgrade: proves the macOS
// scan-based status monitoring still sees a confined apple/container worker.
//
//  1. Workspace writes made INSIDE the VM surface to the host scanner
//     (virtiofs mtime/size stamps), so WorkspaceWrites keeps counting.
//  2. The process-tree sample (kern.procargs2) resolves the host-side
//     container CLI root while the VM runs, and settles to an empty tree
//     after the workload exits — the liveness semantics ADR 0011 records.
//  3. The container CLI's own state monitoring (ls/stats) observes the VM.
//
//	go test ./internal/compute -run TestManualAppleObservationSmoke -v -count=1 -timeout 15m
//
// Requires: the container CLI installed and started, /tmp/ms-tools/mitos
// (linux/arm64), and the alpine image pulled.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/observation"
	"mitos/internal/resourcepolicy"
)

func TestManualAppleObservationSmoke(t *testing.T) {
	if _, err := exec.LookPath(ConfinementContainerCLI); err != nil {
		t.Skip("container CLI required")
	}
	if _, err := os.Stat("/tmp/ms-tools/mitos"); err != nil {
		t.Skip("linux/arm64 mitos build missing")
	}
	dir, err := os.MkdirTemp("", "ms-obs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	base := filepath.Join(dir, "launch")
	workspace := filepath.Join(base, "workspace")
	if err = os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	// A marker seeded before the scan baseline; the workload rewrites it.
	if err = os.WriteFile(filepath.Join(workspace, "counter.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "i=0; while [ $i -lt 12 ]; do echo tick $i > \"" + workspace + "/tick-$i.txt\"; echo update $i > \"" + workspace + "/counter.txt\"; i=$((i+1)); sleep 0.3; done; echo observation-done"
	l := &Local{Confinement: Confinement{
		Tool: ConfinementAppleContainer, NetMode: NetOff,
		Image:    "docker.io/library/alpine:latest",
		ToolsDir: "/tmp/ms-tools",
		// Keep the workload's own /bin/sh path intact under the rewrite.
		GuestBinDir: "/bin",
	}}
	caps := l.Capabilities()
	spec := LaunchSpec{RunID: "run_apple_observe", CapabilityRef: "app", TrustTier: "T1", Provider: "local", Workspace: Workspace{Mode: "copy", Directory: workspace}, Base: base, Harness: "installed-command", Posture: Posture{Resources: resourcepolicy.Agent{}, NetMode: NetOff, DataMount: caps.DataMount, Deadline: time.Now().Add(5 * time.Minute)}, Capabilities: caps}
	handle, err := l.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{"/bin/sh", "-c", script}, Directory: workspace, NativeEnv: []string{"PATH=/bin:/usr/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	activity, err := observation.NewLocalActivity(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer activity.Close()
	activity.SetPID(session.PID())

	var maxWrites, maxTree, maxExecs uint64
	var containerRunning, statsSaw bool
	cid := ""
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-session.Done():
		default:
		}
		sample, sampleErr := activity.Sample(0)
		if sampleErr != nil {
			t.Fatalf("status sample failed: %v", sampleErr)
		}
		if sample.WorkspaceWrites > maxWrites {
			maxWrites = sample.WorkspaceWrites
		}
		if uint64(sample.TreeSize) > maxTree {
			maxTree = uint64(sample.TreeSize)
		}
		if uint64(len(sample.Execs)) > maxExecs {
			maxExecs = uint64(len(sample.Execs))
		}
		if !containerRunning {
			if raw, readErr := os.ReadFile(filepath.Join(base, appleContainerCidFn)); readErr == nil && cid == "" {
				cid = strings.TrimSpace(string(raw))
			}
			if cid != "" {
				if raw, lsErr := exec.Command(ConfinementContainerCLI, "ls", "--format", "json").CombinedOutput(); lsErr == nil {
					var rows []struct {
						ID     string `json:"id"`
						Status struct {
							State string `json:"state"`
						} `json:"status"`
					}
					if json.Unmarshal(raw, &rows) == nil {
						for _, row := range rows {
							if row.ID == cid && row.Status.State == "running" {
								containerRunning = true
							}
						}
					}
				}
				if out, statsErr := exec.Command(ConfinementContainerCLI, "stats", "--no-stream", "--format", "json", cid).CombinedOutput(); statsErr == nil && len(out) > 2 {
					statsSaw = true
				}
			}
		}
		select {
		case <-session.Done():
		case <-time.After(200 * time.Millisecond):
		}
		if sessionDone(session) {
			break
		}
	}
	out, runErr := session.Result()
	_ = l.Stop(context.Background(), session)
	t.Logf("output: %s (err %v)", strings.TrimSpace(string(out)), runErr)
	t.Logf("monitoring: writes=%d tree=%d execs=%d containerRunning=%v stats=%v", maxWrites, maxTree, maxExecs, containerRunning, statsSaw)
	if maxWrites < 8 {
		t.Fatalf("workspace writes made inside the VM were not observed by the host scanner: %d", maxWrites)
	}
	if maxTree < 1 || maxExecs < 1 {
		t.Fatalf("process-tree sample did not resolve the confined worker: tree=%d execs=%d", maxTree, maxExecs)
	}
	if !containerRunning {
		t.Fatal("container ls never observed the VM running")
	}
	if !statsSaw {
		t.Fatal("container stats returned nothing for the running VM")
	}
	// After the workload exits the observed tree settles to empty: the
	// liveness signal operators rely on for quiescence and kill decisions.
	settleDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(settleDeadline) {
		sample, sampleErr := activity.Sample(0)
		if sampleErr != nil {
			t.Fatalf("post-exit sample failed: %v", sampleErr)
		}
		if sample.TreeSize == 0 && len(sample.Execs) == 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("observed process tree never settled after the workload exited")
}

func sessionDone(session Session) bool {
	select {
	case <-session.Done():
		return true
	default:
		return false
	}
}
