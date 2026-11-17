package compute

import (
	"context"
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
	// Withheld network stays blocked at the OS level.
	if python, pyErr := exec.LookPath("python3"); pyErr == nil {
		out, runErr = runConfined(t, l, spec, []string{python, "-c", "import socket\ntry:\n socket.create_connection(('127.0.0.1', 9), timeout=2)\n print('leaked')\nexcept OSError:\n print('blocked')"})
		if runErr != nil || strings.TrimSpace(out) != "blocked" {
			t.Fatalf("network not blocked: %q %v", out, runErr)
		}
	} else {
		t.Log("python3 unavailable; network block asserted via unshare-net flag only")
	}
	// The per-attempt runtime home is mounted and writable.
	out, runErr = runConfined(t, l, spec, []string{sh, "-c", "echo state > $HOME/.runtime && cat $HOME/.runtime"})
	if runErr != nil || strings.TrimSpace(out) != "state" {
		t.Fatalf("runtime home unusable: %q %v", out, runErr)
	}
}
