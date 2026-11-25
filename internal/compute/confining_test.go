package compute

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/resourcepolicy"
)

func confiningCaps() Capabilities {
	return Confinement{Tool: ConfinementBwrap, NetMode: NetOff}.capabilities()
}

func TestConfinementCapabilitiesAreTruthful(t *testing.T) {
	off := Confinement{Tool: ConfinementBwrap, NetMode: NetOff}.capabilities()
	if !off.WorkspaceConfined || off.Isolation != "container" || off.Egress != "off" || len(off.NetModes) != 1 || off.NetModes[0] != "off" {
		t.Fatalf("net-off capabilities not truthful: %+v", off)
	}
	open := Confinement{Tool: ConfinementBwrap, NetMode: NetUnrestricted}.capabilities()
	if !open.WorkspaceConfined || open.Egress != "unrestricted" || len(open.NetModes) != 2 {
		t.Fatalf("unrestricted capabilities not truthful: %+v", open)
	}
	if _, err := NewConfinement("jail", ""); err == nil {
		t.Fatal("unknown tool accepted")
	}
	if _, err := NewConfinement(ConfinementBwrap, "proxy"); err == nil {
		t.Fatal("unsupported network mode accepted")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if _, err := NewConfinement(ConfinementBwrap, ""); err == nil || !strings.Contains(err.Error(), "bwrap is not installed") {
		t.Fatalf("missing tool did not fail closed: %v", err)
	}
}

func TestCompileAdmitsConfinedContainerAndRefusesDowngrades(t *testing.T) {
	spec, err := Compile(Request{Resources: resourcepolicy.Agent{}, TrustTier: "T1", NetMode: NetOff, WorkspaceMode: "copy"}, map[string]Capabilities{"local": confiningCaps()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Provider != "local" || !spec.Capabilities.WorkspaceConfined || spec.Posture.NetMode != "off" {
		t.Fatalf("confined launch not admitted: %+v", spec)
	}
	// An unconfined process provider may not claim the container posture.
	if _, err = Compile(Request{Resources: resourcepolicy.Agent{}, TrustTier: "T1", NetMode: NetOff, WorkspaceMode: "copy"}, map[string]Capabilities{"local": (&Local{}).Capabilities()}, false); err == nil {
		t.Fatal("unconfined provider admitted a confined posture")
	}
	// T0 still refuses containers, confined or not.
	if _, err = Compile(Request{Resources: resourcepolicy.Agent{}, TrustTier: "T0", NetMode: NetOff, WorkspaceMode: "copy"}, map[string]Capabilities{"local": confiningCaps()}, false); err == nil {
		t.Fatal("T0 admitted a container")
	}
}

func confiningLaunch(t *testing.T, l *Local, base, workspace string) LaunchSpec {
	t.Helper()
	caps := l.Capabilities()
	return LaunchSpec{RunID: "run_confinement", CapabilityRef: "app", TrustTier: "T1", Provider: "local", Workspace: Workspace{Mode: "copy", Directory: workspace}, Base: base, Harness: "installed-command", Posture: Posture{Resources: resourcepolicy.Agent{}, NetMode: NetOff, DataMount: caps.DataMount, Deadline: time.Now().Add(time.Minute)}, Capabilities: caps}
}

var confinedRuns int

func runConfined(t *testing.T, l *Local, spec LaunchSpec, argv []string) (string, error) {
	t.Helper()
	confinedRuns++
	suffix := string(rune('a' + confinedRuns))
	spec.RunID = spec.RunID + "-r" + suffix
	spec.Base = filepath.Join(filepath.Dir(spec.Base), "attempt-"+suffix)
	spec.Workspace.Directory = filepath.Join(spec.Base, "workspace")
	if err := os.MkdirAll(spec.Workspace.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	handle, err := l.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	session, err := l.Start(context.Background(), handle, Command{Argv: argv, Directory: spec.Workspace.Directory})
	if err != nil {
		t.Fatal(err)
	}
	<-session.Done()
	out, runErr := session.Result()
	_ = l.Stop(context.Background(), session)
	return string(out), runErr
}

func TestConfiningProviderBlocksOutsideMountsAndNetwork(t *testing.T) {
	if _, err := exec.LookPath(ConfinementBwrap); err != nil {
		t.Skip("bwrap required for confinement escape tests")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh required")
	}
	l := &Local{Confinement: Confinement{Tool: ConfinementBwrap, NetMode: NetOff}}
	root := t.TempDir()
	base := filepath.Join(root, "attempt")
	workspace := filepath.Join(base, "workspace")
	secret := filepath.Join(root, "outside-secret")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(secret, []byte("host secret"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := confiningLaunch(t, l, base, workspace)

	// Reads outside the mounted paths fail even for the same user.
	out, runErr := runConfined(t, l, spec, []string{sh, "-c", "if [ -f " + secret + " ]; then echo leaked; else echo confined; fi"})
	if runErr != nil || strings.TrimSpace(out) != "confined" {
		t.Fatalf("outside mount readable: %q %v", out, runErr)
	}
	// The workspace copy stays writable and readable.
	out, runErr = runConfined(t, l, spec, []string{sh, "-c", "echo work > note.txt && cat note.txt"})
	if runErr != nil || strings.TrimSpace(out) != "work" {
		t.Fatalf("workspace unusable: %q %v", out, runErr)
	}
	// Probe a live host socket, not a refused port, and use this self-contained
	// test executable so a user Python virtualenv cannot escape the mount set.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	out, runErr = runConfined(t, l, spec, []string{os.Args[0], "-test.run=^TestConfinementNetworkProbe$", "--", listener.Addr().String()})
	if runErr != nil || strings.TrimSpace(out) != "blocked" {
		t.Fatalf("network not blocked: %q %v", out, runErr)
	}
	open := &Local{Confinement: Confinement{Tool: ConfinementBwrap, NetMode: NetUnrestricted}}
	openSpec := spec
	openSpec.Capabilities = open.Capabilities()
	openSpec.Posture.NetMode = NetUnrestricted
	out, runErr = runConfined(t, open, openSpec, []string{os.Args[0], "-test.run=^TestConfinementNetworkProbe$", "--", listener.Addr().String()})
	if runErr != nil || strings.TrimSpace(out) != "connected" {
		t.Fatalf("live-socket positive control failed: %q %v", out, runErr)
	}
	// The same configured provider must enforce an admitted net-off request.
	openSpec.Posture.NetMode = NetOff
	out, runErr = runConfined(t, open, openSpec, []string{os.Args[0], "-test.run=^TestConfinementNetworkProbe$", "--", listener.Addr().String()})
	if runErr != nil || strings.TrimSpace(out) != "blocked" {
		t.Fatalf("provider ignored admitted network posture: %q %v", out, runErr)
	}
	// The per-attempt runtime home is mounted and writable.
	out, runErr = runConfined(t, l, spec, []string{sh, "-c", "echo state > $HOME/.runtime && cat $HOME/.runtime"})
	if runErr != nil || strings.TrimSpace(out) != "state" {
		t.Fatalf("runtime home unusable: %q %v", out, runErr)
	}
}

// Host control sockets under /run are never mounted: a read-only mount still
// accepts requests over docker or systemd bus sockets, which would defeat the
// confinement.
func TestConfinementOmitsHostControlSockets(t *testing.T) {
	if _, err := exec.LookPath(ConfinementBwrap); err != nil {
		t.Skip("bwrap required for confinement argv tests")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh required")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	c := Confinement{Tool: ConfinementBwrap, NetMode: NetOff}
	argv, err := confineArgv(c, []string{sh, "-c", "true"}, filepath.Join(root, "attempt"), workspace, workspace, resourcepolicy.Agent{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, "\x00")
	if strings.Contains(joined, "\x00/run\x00") || strings.Contains(joined, "/run\x00/run") {
		t.Fatalf("host /run mounted read-only: %v", argv)
	}
}

func TestConfinementNetworkProbe(t *testing.T) {
	if len(flag.Args()) != 1 {
		return
	}
	connection, err := net.DialTimeout("tcp", flag.Args()[0], time.Second)
	if err != nil {
		fmt.Println("blocked")
	} else {
		connection.Close()
		fmt.Println("connected")
	}
	os.Exit(0)
}
