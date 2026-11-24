package compute

// Manual smoke test for ADR 0011 mediated egress on apple/container: drives
// the REAL container CLI with the argv built by confineArgvApple and proves
// the mediated transport end to end inside --network none (vsock-forwarded
// relay socket + in-guest egress loop). Skips unless the container CLI and
// the linux/arm64 helper binaries are present, so CI never depends on it.
//
//	go test ./internal/compute -run TestManualAppleEgressSmoke -v -count=1
//
// Prepare with:
//
//	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/mitos-smoke/mitos ./cmd/mitos
//	echo a CONNECT probe binary to /tmp/mitos-smoke/tunnelprobe

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mitos/internal/resourcepolicy"
)

func TestManualAppleEgressSmoke(t *testing.T) {
	if _, err := exec.LookPath(ConfinementContainerCLI); err != nil {
		t.Skip("container CLI required")
	}
	for _, bin := range []string{"/tmp/mitos-smoke/mitos", "/tmp/mitos-smoke/tunnelprobe"} {
		if _, err := os.Stat(bin); err != nil {
			t.Skip("linux/arm64 smoke binaries missing")
		}
	}
	dir := "/tmp/ms-relay"
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "e.sock")
	relay, err := StartEgressRelay(socket, DefaultModelHosts)
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()

	// Exercise the exact argv the product builds, not a hand-written one.
	base := "/tmp/ms-launch"
	_ = os.RemoveAll(base)
	workspace := filepath.Join(base, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	c := appleConfinement(NetUnrestricted)
	c.EgressSocket = socket
	// The image contract ships toolchains under GuestBinDir; for the smoke
	// the same paths are provided through the workspace virtiofs share.
	toolchain := filepath.Join(workspace, "bin")
	if err := os.MkdirAll(toolchain, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, bin := range []string{"mitos", "tunnelprobe"} {
		data, readErr := os.ReadFile(filepath.Join("/tmp/mitos-smoke", bin))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err = os.WriteFile(filepath.Join(toolchain, bin), data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	c.GuestBinDir = toolchain
	c.GuestHelper = filepath.Join(toolchain, "mitos")
	c.Image = "docker.io/library/alpine:latest"
	argv, err := confineArgvApple(c, []string{"/usr/bin/tunnelprobe", "127.0.0.1:3128", "api.openai.com"}, base, workspace, workspace, resourcepolicy.Agent{})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("product argv: %v", argv)
	cmd := exec.Command(argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	t.Logf("output:\n%s", out)
	if err != nil {
		t.Fatalf("container run failed: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, `response: "HTTP/1.1 2`) {
		t.Fatalf("allowlisted CONNECT was not mediated through the product argv:\n%s", text)
	}
}
