package compute

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mitos/internal/resourcepolicy"
)

// fakeAppleCLI writes a `container` executable that consumes this process's
// flags, drops the image argument, and execs the guest command line directly.
// Real flag handling is asserted by the argv construction tests; the provider
// tests use it to prove launches flow through the CLI end to end.
func fakeAppleCLI(t *testing.T, binDir string) {
	t.Helper()
	script := `#!/bin/sh
[ "$1" = "run" ] || exit 64
shift
while [ $# -gt 0 ]; do
  case "$1" in
    --rm) shift ;;
    -*) shift 2 ;;
    *) break ;;
  esac
done
[ $# -gt 0 ] || exit 64
shift
exec "$@"
`
	path := filepath.Join(binDir, ConfinementContainerCLI)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func appleConfinement(netMode string) Confinement {
	return Confinement{Tool: ConfinementAppleContainer, NetMode: netMode, Image: "mitos-worker:test"}
}

func TestAppleConfinementFailsClosed(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if _, err := NewConfinement(ConfinementAppleContainer, ""); err == nil || !strings.Contains(err.Error(), "container") {
		t.Fatalf("missing CLI did not fail closed: %v", err)
	}
	fakeAppleCLI(t, bin)
	c, err := NewConfinement(ConfinementAppleContainer, NetOff)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tool != ConfinementAppleContainer || c.NetMode != NetOff {
		t.Fatalf("unexpected confinement: %+v", c)
	}
	if _, err = NewConfinement(ConfinementAppleContainer, "proxy"); err == nil {
		t.Fatal("unsupported network mode accepted")
	}
	// The image is the VM's rootfs: refusing to launch without it never
	// degrades to an unconfined or wrong-toolchain execution.
	argv, err := confineArgv(Confinement{Tool: ConfinementAppleContainer, NetMode: NetOff}, []string{"/bin/true"}, t.TempDir(), t.TempDir(), "", resourcepolicy.Agent{})
	if err == nil || !strings.Contains(err.Error(), "OCI image") {
		t.Fatalf("missing image did not fail closed: %v (%v)", err, argv)
	}
	// Mediation requires the unrestricted posture, exactly as under bwrap.
	withheld := appleConfinement(NetOff)
	withheld.EgressSocket = filepath.Join(t.TempDir(), "model.sock")
	if _, err = confineArgv(withheld, []string{"/bin/true"}, t.TempDir(), t.TempDir(), "", resourcepolicy.Agent{}); err == nil {
		t.Fatal("mediated socket without unrestricted mode accepted")
	}
}

func TestAppleConfinementCapabilitiesAreTruthful(t *testing.T) {
	off := appleConfinement(NetOff).capabilities()
	if !off.WorkspaceConfined || off.Isolation != "container" || off.Egress != "off" || len(off.NetModes) != 1 || off.NetModes[0] != "off" {
		t.Fatalf("net-off capabilities not truthful: %+v", off)
	}
	if !off.CPUQuota || !off.MemoryLimit {
		t.Fatalf("a per-container VM must report enforceable resource caps: %+v", off)
	}
	open := appleConfinement(NetUnrestricted).capabilities()
	if !open.WorkspaceConfined || open.Egress != "unrestricted" || len(open.NetModes) != 2 {
		t.Fatalf("unrestricted capabilities not truthful: %+v", open)
	}
	// The bubblewrap process sandbox cannot enforce ceilings and must not
	// grow them by association.
	if bwrap := (Confinement{Tool: ConfinementBwrap, NetMode: NetOff}).capabilities(); bwrap.CPUQuota || bwrap.MemoryLimit {
		t.Fatalf("bwrap reported enforceable resource caps: %+v", bwrap)
	}
}

func TestAppleConfineArgvMatchesTheBwrapContract(t *testing.T) {
	bin := t.TempDir()
	fakeAppleCLI(t, bin)
	t.Setenv("PATH", bin)
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	inputs := filepath.Join(base, "inputs")
	for _, dir := range []string{workspace, inputs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(base, "model.sock")
	if listener, err := net.Listen("unix", socket); err == nil {
		listener.Close()
	}
	capped := resourcepolicy.Agent{CPUMillicores: resourcepolicy.Capped(1500), MemoryMiB: resourcepolicy.Capped(2048)}

	t.Run("network off", func(t *testing.T) {
		argv, err := confineArgv(appleConfinement(NetOff), []string{"/opt/homebrew/bin/codex", "exec"}, base, workspace, workspace, capped)
		if err != nil {
			t.Fatal(err)
		}
		assertArgv(t, argv[1:], []string{"run", "--rm", "--cidfile", filepath.Join(base, appleContainerCidFn), "-w", workspace}, "prefix")
		if !containsPair(argv, "--volume", workspace+":"+workspace) || !containsPair(argv, "--volume", inputs+":"+inputs+":ro") {
			t.Fatalf("minimal mounts missing: %v", argv)
		}
		if !containsPair(argv, "--network", "none") {
			t.Fatalf("net-off must attach no interface: %v", argv)
		}
		if !containsPair(argv, "--cpus", "1.5") || !containsPair(argv, "--memory", "2048M") {
			t.Fatalf("admitted ceilings not mapped onto the VM: %v", argv)
		}
		if argv[len(argv)-3] != "mitos-worker:test" || argv[len(argv)-2] != "/usr/local/bin/codex" || argv[len(argv)-1] != "exec" {
			t.Fatalf("image or guest toolchain path wrong: %v", argv)
		}
	})

	t.Run("unrestricted without mediation keeps the default NAT network", func(t *testing.T) {
		argv, err := confineArgv(appleConfinement(NetUnrestricted), []string{"/opt/homebrew/bin/codex"}, base, workspace, workspace, resourcepolicy.Agent{})
		if err != nil {
			t.Fatal(err)
		}
		if contains(argv, "--network") {
			t.Fatalf("unmediated unrestricted must use the default network: %v", argv)
		}
		if contains(argv, socket) {
			t.Fatalf("no mediation without an egress socket: %v", argv)
		}
	})

	t.Run("mediated egress mirrors the Linux loop", func(t *testing.T) {
		c := appleConfinement(NetUnrestricted)
		c.EgressSocket = socket
		argv, err := confineArgv(c, []string{"/opt/homebrew/bin/codex", "--headless"}, base, workspace, workspace, resourcepolicy.Agent{})
		if err != nil {
			t.Fatal(err)
		}
		if !containsPair(argv, "--network", "none") {
			t.Fatalf("mediated egress must attach no interface: %v", argv)
		}
		if !containsPair(argv, "--volume", socket+":"+socket+":ro") {
			t.Fatalf("relay socket not mounted for vsock forwarding: %v", argv)
		}
		image := len(argv) - 7
		if argv[image] != "mitos-worker:test" || argv[image+1] != "/bin/sh" || argv[image+2] != "-c" {
			t.Fatalf("mediated workload must run under the egress-loop wrapper: %v", argv)
		}
		script := argv[image+3]
		for _, want := range []string{defaultGuestHelper + " egress-loop", "--socket " + strconv.Quote(socket), "--listen " + strconv.Quote(EgressLoopAddr), `exec "$@"`} {
			if !strings.Contains(script, want) {
				t.Fatalf("egress loop script missing %q: %q", want, script)
			}
		}
		if argv[image+4] != "sh" || argv[image+5] != "/usr/local/bin/codex" || argv[image+6] != "--headless" {
			t.Fatalf("workload not passed through the wrapper: %v", argv)
		}
	})

	t.Run("guest bin dir is configurable and the workdir honored", func(t *testing.T) {
		c := appleConfinement(NetOff)
		c.GuestBinDir = "/opt/tools/bin"
		argv, err := confineArgv(c, []string{"/usr/bin/env"}, base, workspace, filepath.Join(workspace, "sub"), resourcepolicy.Agent{})
		if err != nil {
			t.Fatal(err)
		}
		if !containsPair(argv, "-w", filepath.Join(workspace, "sub")) {
			t.Fatalf("workdir not passed: %v", argv)
		}
		if argv[len(argv)-1] != "/opt/tools/bin/env" {
			t.Fatalf("guest toolchain rewrite missing: %v", argv)
		}
	})
}

func TestAppleConfiningProviderLaunchesThroughTheCLI(t *testing.T) {
	bin := t.TempDir()
	fakeAppleCLI(t, bin)
	t.Setenv("PATH", bin)
	guestBin := t.TempDir()
	probe := filepath.Join(guestBin, "probe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\necho apple-confined-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	l := &Local{Confinement: appleConfinement(NetOff)}
	l.Confinement.GuestBinDir = guestBin
	spec := confiningLaunch(t, l, base, filepath.Join(base, "workspace"))
	spec.Base = base
	spec.Workspace.Directory = filepath.Join(base, "workspace")
	if err := os.MkdirAll(spec.Workspace.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runConfined(t, l, spec, []string{probe})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "apple-confined-ok" {
		t.Fatalf("unexpected workload output: %q", out)
	}
}

func TestAppleMediatedProviderLaunchesWithRelay(t *testing.T) {
	bin := t.TempDir()
	fakeAppleCLI(t, bin)
	// The wrapper's readiness gate and the stub's shell need the system
	// tools next to the fake CLI.
	t.Setenv("PATH", bin+":/bin:/usr/bin")
	guestBin := t.TempDir()
	probe := filepath.Join(guestBin, "probe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\necho mediated-ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(guestBin, "mitos")
	// The stub helper creates the readiness file the wrapper waits for,
	// mirroring the real egress loop's --ready contract.
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nready=\"\"\nwhile [ $# -gt 0 ]; do\n  [ \"$1\" = --ready ] && ready=$2\n  shift\ndone\n[ -n \"$ready\" ] && : > \"$ready\"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	// Keep the relay socket under every platform's sun_path limit.
	egressDir, err := os.MkdirTemp("", "mitos-apple-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(egressDir)
	egress := filepath.Join(egressDir, "e.sock")
	relay, err := StartEgressRelay(egress, DefaultModelHosts)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	l := &Local{Confinement: appleConfinement(NetUnrestricted)}
	l.Confinement.GuestBinDir = guestBin
	l.Confinement.GuestHelper = helper
	l.Confinement.EgressSocket = egress
	caps := l.Capabilities()
	spec := LaunchSpec{RunID: "run_apple_mediated", CapabilityRef: "app", TrustTier: "T1", Provider: "local", Workspace: Workspace{Mode: "copy", Directory: filepath.Join(base, "workspace")}, Base: base, Harness: "installed-command", Posture: Posture{Resources: resourcepolicy.Agent{}, NetMode: NetUnrestricted, BrokerEndpoint: "unix://" + egress, DataMount: caps.DataMount, Deadline: time.Now().Add(time.Minute)}, Capabilities: caps}
	if err = os.MkdirAll(spec.Workspace.Directory, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := l.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{probe}, Directory: spec.Workspace.Directory})
	if err != nil {
		t.Fatal(err)
	}
	<-session.Done()
	out, runErr := session.Result()
	_ = l.Stop(context.Background(), session)
	if runErr != nil {
		t.Fatal(runErr)
	}
	if strings.TrimSpace(string(out)) != "mediated-ok" {
		t.Fatalf("unexpected workload output: %q", out)
	}
}

func TestReapAppleContainerKillsAndRemoves(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, ConfinementContainerCLI), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, appleContainerCidFn), []byte("deadbeef\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reapAppleContainer(appleConfinement(NetOff), base)
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(raw)
	if !strings.Contains(calls, "kill --signal KILL deadbeef") || !strings.Contains(calls, "delete --force deadbeef") {
		t.Fatalf("container not reaped: %q", calls)
	}
	if _, err = os.Stat(filepath.Join(base, appleContainerCidFn)); !os.IsNotExist(err) {
		t.Fatalf("cidfile not cleaned: %v", err)
	}
	// Other tools and missing cidfiles stay no-ops.
	reapAppleContainer(Confinement{Tool: ConfinementBwrap}, base)
	reapAppleContainer(appleConfinement(NetOff), t.TempDir())
	if raw, err = os.ReadFile(log); err != nil || strings.Count(string(raw), "\n") != 2 {
		t.Fatalf("reap was not exactly once per cidfile: %v (%v)", string(raw), err)
	}
}

func contains(argv []string, flag string) bool {
	for _, arg := range argv {
		if arg == flag {
			return true
		}
	}
	return false
}

func containsPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func assertArgv(t *testing.T, argv, want []string, what string) {
	t.Helper()
	if len(argv) < len(want) {
		t.Fatalf("%s: argv shorter than expected: %v", what, argv)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("%s mismatch at %d: got %q want %q (full: %v)", what, i, argv[i], want[i], argv)
		}
	}
}
