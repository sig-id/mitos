package compute

// Manual smoke test for ADR 0011: runs the REAL pi coding agent inside a
// confined apple/container VM whose only egress is the mediated relay.
// Closes the "real workload, real model call" gap: the VM boots from the
// official node image, the pi package and the mitos helper come from one
// read-only host tools directory shared by every VM, and pi's model traffic
// (HTTPS_PROXY -> in-guest egress loop -> vsock-forwarded relay socket)
// must traverse the allowlisted relay to succeed.
//
//	go test ./internal/compute -run TestManualApplePiWorkloadSmoke -v -count=1 -timeout 20m
//
// Requires: container CLI started, /tmp/ms-tools/{mitos,pi} prepared
// (linux/arm64 mitos build + a copy of the host pi package), pi credentials
// under ~/.pi/agent, and the node:22-alpine image pulled.

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

func TestManualApplePiWorkloadSmoke(t *testing.T) {
	if _, err := exec.LookPath(ConfinementContainerCLI); err != nil {
		t.Skip("container CLI required")
	}
	tools := "/tmp/ms-tools"
	for _, bin := range []string{filepath.Join(tools, "mitos"), filepath.Join(tools, "pi", "dist", "bundle", "cli.js")} {
		if _, err := os.Stat(bin); err != nil {
			t.Skip("smoke toolchain missing under /tmp/ms-tools")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(home, ".pi", "agent", "auth.json")); err != nil {
		t.Skip("pi credentials missing")
	}
	dir, err := os.MkdirTemp("", "ms-pi-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "e.sock")
	relay, err := StartEgressRelay(socket, []string{"api.z.ai"})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	base := filepath.Join(dir, "launch")
	workspace := filepath.Join(base, "workspace")
	attemptHome := filepath.Join(base, "home")
	for _, d := range []string{workspace, filepath.Join(attemptHome, ".pi", "agent")} {
		if err = os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// The guest HOME is the per-attempt home mount; pi reads its credentials
	// and model catalog from there, exactly as it would on a workstation.
	// Settings are rebuilt minimal: host settings may declare extensions,
	// which pi would try to npm-install inside the network-less VM.
	for _, name := range []string{"auth.json", "models-store.json"} {
		raw, readErr := os.ReadFile(filepath.Join(home, ".pi", "agent", name))
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			t.Fatal(readErr)
		}
		if err = os.WriteFile(filepath.Join(attemptHome, ".pi", "agent", name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	minimal := []byte("{\"defaultProvider\": \"zai\", \"defaultModel\": \"glm-5.3\"}\n")
	if err = os.WriteFile(filepath.Join(attemptHome, ".pi", "agent", "settings.json"), minimal, 0o600); err != nil {
		t.Fatal(err)
	}

	l := &Local{Confinement: Confinement{
		Tool: ConfinementAppleContainer, NetMode: NetUnrestricted,
		Image:    "docker.io/library/node:22-alpine",
		ToolsDir: tools,
		// argv[0] is the image's own node; the rewrite keeps it unchanged.
		GuestBinDir:  "/usr/local/bin",
		EgressSocket: socket,
	}}
	caps := l.Capabilities()
	spec := LaunchSpec{RunID: "run_apple_pi", CapabilityRef: "app", TrustTier: "T1", Provider: "local", Workspace: Workspace{Mode: "copy", Directory: workspace}, Base: base, Harness: "installed-command", Posture: Posture{Resources: resourcepolicy.Agent{}, NetMode: NetUnrestricted, BrokerEndpoint: "unix://" + socket, DataMount: caps.DataMount, Deadline: time.Now().Add(15 * time.Minute)}, Capabilities: caps}
	handle, err := l.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"HOME=" + attemptHome,
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HTTPS_PROXY=http://" + EgressLoopAddr,
		"HTTP_PROXY=http://" + EgressLoopAddr,
		"NO_PROXY=localhost,127.0.0.1",
	}
	argv := []string{
		"/usr/local/bin/node", filepath.Join(tools, "pi", "dist", "bundle", "cli.js"),
		"--provider", "zai", "--model", "glm-5.3",
		"-p", "--no-extensions", "--no-mcp", "--no-skills", "--no-context-files", "--no-session", "--no-tools",
		"Reply with exactly this token and nothing else: pi-vm-egress-ok",
	}
	session, err := l.Start(context.Background(), handle, Command{Argv: argv, Directory: workspace, NativeEnv: env})
	if err != nil {
		t.Fatal(err)
	}
	<-session.Done()
	out, runErr := session.Result()
	_ = l.Stop(context.Background(), session)
	t.Logf("pi output:\n%s", out)
	if runErr != nil {
		t.Fatalf("confined pi run failed: %v", runErr)
	}
	if !strings.Contains(string(out), "pi-vm-egress-ok") {
		t.Fatalf("confined pi did not answer through the mediated relay:\n%s", out)
	}
}
