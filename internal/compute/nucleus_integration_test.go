package compute

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests run the real runtime. They need a Nucleus binary and a cgroup
// subtree delegated to the test process, which must be its only member:
//
//	just test-nucleus
func realNucleus(t *testing.T) *Local {
	t.Helper()
	binary := os.Getenv("MITOS_TEST_NUCLEUS")
	if binary == "" || os.Getenv("NUCLEUS_CGROUP_ROOT") == "" {
		t.Skip("set MITOS_TEST_NUCLEUS and a delegated NUCLEUS_CGROUP_ROOT (just test-nucleus)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	l, err := NewNucleus(ctx, NucleusConfig{Binary: binary, CgroupRoot: os.Getenv("NUCLEUS_CGROUP_ROOT"), ProbeDir: filepath.Join(t.TempDir(), "probe")})
	if err != nil {
		t.Fatal(err)
	}
	caps := l.Capabilities()
	if !caps.CPUQuota || !caps.MemoryLimit || !caps.WorkspaceConfined || caps.Isolation != "container" {
		t.Fatalf("probe did not establish enforced capabilities: %+v", caps)
	}
	return l
}

func preparedNucleus(t *testing.T, l *Local, source string) Handle {
	t.Helper()
	return preparedNucleusWith(t, l, source, nil)
}

func preparedNucleusWith(t *testing.T, l *Local, source string, adjust func(*LaunchSpec)) Handle {
	t.Helper()
	base := filepath.Join(t.TempDir(), "attempt")
	spec := nucleusSpec(t, base)
	if adjust != nil {
		adjust(&spec)
	}
	admitted, err := Compile(Request{Resources: spec.Posture.Resources, TrustTier: "T1", NetMode: NetOff, WorkspaceMode: "copy"}, map[string]Capabilities{"local": (&Local{}).Capabilities(), ConfinementNucleus: l.Capabilities()}, false)
	if err != nil {
		t.Fatal(err)
	}
	admitted.RunID, admitted.CapabilityRef, admitted.Base = "run-1", "app", base
	admitted.Workspace = Workspace{Mode: "copy", Source: source, Directory: filepath.Join(base, "workspace")}
	handle, err := l.Prepare(context.Background(), admitted)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestNucleusRunsConfinedAndHarvests(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handle := preparedNucleus(t, l, source)
	base := filepath.Dir(handle.Workspace())
	script := `set -e
read line
echo "stdin=$line"
echo after > /workspace/input.txt
echo note > "$HOME/note"
test ! -e ` + base + `
test ! -e /mitos/history.git
echo "home=$HOME tmp=$TMPDIR"`
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{shell, "-c", script}, Input: []byte("hello\n")})
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Result()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if !strings.Contains(string(output), "stdin=hello") || !strings.Contains(string(output), "home=/mitos/home tmp=/mitos/tmp") {
		t.Fatalf("output %q", output)
	}
	if note, _ := os.ReadFile(filepath.Join(base, "home", "note")); string(note) != "note\n" {
		t.Fatalf("per-attempt home not persisted: %q", note)
	}
	harvest, err := l.Harvest(context.Background(), session)
	if err != nil || harvest.Change == nil {
		t.Fatalf("harvest %v %+v", err, harvest)
	}
	var changed []string
	for _, change := range harvest.Change.Changes {
		changed = append(changed, change.Path)
	}
	if strings.Join(changed, ",") != "input.txt" {
		t.Fatalf("harvested changes %q", changed)
	}
	record, err := os.ReadFile(filepath.Join(base, nucleusEventsFile))
	if err != nil || !strings.Contains(string(record), `"landlock_status":"fully_enforced"`) {
		t.Fatalf("attestation not recorded: %v %s", err, record)
	}
}

func TestNucleusStopRevokesThroughTheCgroup(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	handle := preparedNucleus(t, l, "")
	// The workload escapes its process group; only the cgroup still reaches it.
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{shell, "-c", "setsid sleep 600 & exec sleep 600"}})
	if err != nil {
		t.Fatal(err)
	}
	cgroup := session.(*localSession).sandbox.cgroup
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = l.Stop(ctx, session); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(cgroup); errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			procs, _ := os.ReadFile(filepath.Join(cgroup, "cgroup.procs"))
			t.Fatalf("container cgroup survived stop: %s", procs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNucleusReclaimsContainersOfAPreviousServer(t *testing.T) {
	realNucleus(t)
	root := os.Getenv("NUCLEUS_CGROUP_ROOT")
	orphan := filepath.Join(root, "nucleus-orphan-test")
	if err := os.Mkdir(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	sleeper := exec.Command("sleep", "600")
	sleeper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	if err := os.WriteFile(filepath.Join(orphan, "cgroup.procs"), []byte(strconv.Itoa(sleeper.Process.Pid)), 0); err != nil {
		t.Fatal(err)
	}
	n := &nucleusRuntime{cfg: NucleusConfig{CgroupRoot: root}}
	count, err := n.reclaim()
	if err != nil || count != 1 {
		t.Fatalf("reclaimed %d: %v", count, err)
	}
	done := make(chan error, 1)
	go func() { done <- sleeper.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("orphaned workload survived reclaim")
	}
	if _, err = os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan cgroup not removed: %v", err)
	}
}

func TestNucleusNeverReleasesAnUnattestedWorkload(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	h := preparedNucleus(t, l, "").(*localHandle)
	workspace := h.spec.Workspace.Directory
	run, err := l.sandbox.launch(h.spec, []string{shell, "-c", "touch /workspace/released"}, workspace, []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	defer run.close()
	// The admitted posture differs from what Nucleus will enforce.
	run.posture.PidsMax++
	cmd := exec.Command(run.argv[0], run.argv[1:]...)
	cmd.Dir, cmd.Env, cmd.ExtraFiles = run.dir, run.env, run.files
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	run.begin()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = run.await(ctx, nil); err == nil || !strings.Contains(err.Error(), "pids limit") {
		t.Fatalf("mismatched posture released: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("refused start exited successfully")
		}
	case <-time.After(20 * time.Second):
		_ = run.kill()
		t.Fatal("closed start gate did not tear the container down")
	}
	if err = run.finish(); err != nil {
		t.Fatal(err)
	}
	if run.summary.ExitStatus == nil || !strings.Contains(run.summary.ExitStatus.Error, "Start gate") {
		t.Fatalf("summary does not report the refused start: %+v", run.summary.ExitStatus)
	}
	if _, err = os.Stat(filepath.Join(workspace, "released")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workload ran before attestation: %v", err)
	}
}

func TestNucleusKillsAWorkloadAtItsMemoryLimit(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	handle := preparedNucleusWith(t, l, "", func(spec *LaunchSpec) {
		spec.Posture.Resources.MemoryMiB.Amount = 64
	})
	// tail buffers a line with no newline, so it grows past the limit.
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{shell, "-c", "head -c 512m /dev/zero | tail -n 1 >/dev/null"}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := session.Result()
		result <- err
	}()
	select {
	case err = <-result:
		if !errors.Is(err, ErrMemoryExhausted) {
			t.Fatalf("over-limit workload not reported as memory exhaustion: %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = l.Stop(context.Background(), session)
		t.Fatal("over-limit workload was throttled instead of killed")
	}
}
