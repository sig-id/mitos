package compute

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"mitos/internal/resourcepolicy"
)

// apple/container confinement (ADR 0011): every confined launch boots its own
// lightweight Linux VM from an OCI image. The bwrap mount contract maps onto
// virtiofs volumes at identical paths, so the workspace copy, per-attempt home,
// tmp and read-only dependency inputs keep their meaning, and host observation
// of workspace writes keeps working through the shared filesystem.
//
// Network postures:
//   - off: "--network none", the reserved name that attaches no interface.
//   - unrestricted, unmediated: the default vmnet NAT network. The guest can
//     reach the external network but never the host's loopback listeners.
//   - unrestricted, mediated: "--network none" plus the host egress relay's
//     UNIX socket, bind-mounted at its own path. apple/container detects
//     socket sources and transparently replaces the volume with a vsock-backed
//     forwarder (RuntimeService.configureContainer → UnixSocketConfiguration
//     direction .into), so the in-guest "mitos egress-loop" forwarder connects
//     to the exact same allowlisted relay as on Linux and the workload's
//     HTTPS_PROXY wiring is unchanged.

const (
	defaultGuestBinDir  = "/usr/local/bin"
	defaultGuestHelper  = "/usr/local/bin/mitos"
	appleContainerCidFn = "container.id"
)

// confineArgvApple wraps argv in a per-launch apple/container VM. Failing
// closed mirrors the bwrap path: a missing image, helper or mediated-egress
// prerequisite refuses the launch instead of degrading the posture.
func confineArgvApple(c Confinement, argv []string, base, workspace, workdir string, resources resourcepolicy.Agent) ([]string, error) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, errors.New("command requires an absolute executable")
	}
	if c.Image == "" {
		return nil, errors.New("apple-container confinement requires an OCI image; set MITOS_CONFINEMENT_IMAGE to an image shipping the workload toolchain and the guest mitos helper (ADR 0011)")
	}
	if c.NetMode != NetOff && c.NetMode != NetUnrestricted {
		return nil, fmt.Errorf("unsupported confinement network mode %q", c.NetMode)
	}
	helper := c.GuestHelper
	if helper == "" {
		helper = defaultGuestHelper
	}
	if !filepath.IsAbs(helper) {
		return nil, errors.New("apple-container confinement requires an absolute guest helper path")
	}
	tool, err := exec.LookPath(ConfinementContainerCLI)
	if err != nil {
		return nil, fmt.Errorf("confinement tool unavailable: %w", err)
	}
	home := filepath.Join(base, "home")
	tmp := filepath.Join(base, "tmp")
	for _, dir := range []string{home, tmp, workspace} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	}
	args := []string{
		tool, "run",
		"--rm",
		// No die-with-parent exists: the CLI records the container ID so
		// session teardown can kill a deadline-orphans VM.
		"--cidfile", filepath.Join(base, appleContainerCidFn),
		"-w", workspace,
	}
	if workdir != "" {
		if !filepath.IsAbs(workdir) {
			return nil, errors.New("working directory must be absolute")
		}
		args[len(args)-1] = workdir
	}
	// The guest boots from the image's rootfs; only the per-attempt state is
	// shared with the host, at identical paths.
	binds := [][3]string{
		{home, home, ""},
		{tmp, tmp, ""},
		{workspace, workspace, ""},
	}
	if inputs := filepath.Join(base, "inputs"); dirExists(inputs) {
		binds = append(binds, [3]string{inputs, inputs, "ro"})
	}
	mediated := c.EgressSocket != ""
	if mediated {
		if c.NetMode != NetUnrestricted {
			return nil, errors.New("mediated egress requires the unrestricted network mode")
		}
		// The socket volume is replaced by a vsock forwarder; the guest sees a
		// working UNIX socket at the same path the relay serves on the host.
		binds = append(binds, [3]string{c.EgressSocket, c.EgressSocket, "ro"})
	}
	for _, bind := range binds {
		spec := bind[0] + ":" + bind[1]
		if bind[2] != "" {
			spec += ":" + bind[2]
		}
		args = append(args, "--volume", spec)
	}
	switch {
	case mediated || c.NetMode == NetOff:
		// Mediated egress wants no interface at all: the relay socket is the
		// only route out, matching the Linux isolated-namespace posture.
		args = append(args, "--network", "none")
	default:
		// Unrestricted, unmediated: the default NAT network reaches the
		// external world but never the host's loopback listeners.
	}
	if flag, value, ok := appleCPULimit(resources); ok {
		args = append(args, flag, value)
	}
	if flag, value, ok := appleMemoryLimit(resources); ok {
		args = append(args, flag, value)
	}
	guest := appleGuestArgv(c, argv)
	args = append(args, c.Image)
	if mediated {
		// Same in-sandbox loop as Linux: the guest helper forwards the
		// workload's proxy traffic to the relay socket, signals readiness
		// through a file in the mounted tmp, and only then execs the workload
		// in place so process identity and stdio stay with it.
		ready := filepath.Join(tmp, "egress.ready")
		script := fmt.Sprintf("%s egress-loop --socket %q --listen %q --ready %q >/dev/null 2>&1 & i=0; while [ ! -e %q ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done; exec \"$@\"", helper, c.EgressSocket, EgressLoopAddr, ready, ready)
		guest = append([]string{"/bin/sh", "-c", script, "sh"}, guest...)
	}
	return append(args, guest...), nil
}

// appleGuestArgv rewrites the workload into the image's toolchain directory:
// a Linux guest cannot run the host binary at argv[0], so the image contract
// (ADR 0011) ships every workload binary under GuestBinDir by name.
func appleGuestArgv(c Confinement, argv []string) []string {
	dir := c.GuestBinDir
	if dir == "" {
		dir = defaultGuestBinDir
	}
	guest := append([]string(nil), argv...)
	guest[0] = filepath.Join(dir, filepath.Base(argv[0]))
	return guest
}

// appleCPULimit maps an admitted millicore ceiling onto the VM's CPU count.
func appleCPULimit(resources resourcepolicy.Agent) (string, string, bool) {
	if resources.CPUMillicores.Mode != "limit" || resources.CPUMillicores.Amount <= 0 {
		return "", "", false
	}
	cpus := float64(resources.CPUMillicores.Amount) / 1000
	if cpus < 1 {
		cpus = 1
	}
	value := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", cpus), "0"), ".")
	return "--cpus", value, true
}

// appleMemoryLimit maps an admitted MiB ceiling onto the VM's memory size.
func appleMemoryLimit(resources resourcepolicy.Agent) (string, string, bool) {
	if resources.MemoryMiB.Mode != "limit" || resources.MemoryMiB.Amount <= 0 {
		return "", "", false
	}
	return "--memory", fmt.Sprintf("%dM", resources.MemoryMiB.Amount), true
}

// reapAppleContainer best-effort kills and removes a confinement VM recorded
// in its cidfile. The foreground CLI has no die-with-parent: without this, a
// deadline-killed CLI could leave its VM running.
func reapAppleContainer(c Confinement, base string) {
	if c.Tool != ConfinementAppleContainer {
		return
	}
	raw, err := os.ReadFile(filepath.Join(base, appleContainerCidFn))
	if err != nil || len(raw) == 0 {
		return
	}
	id := string(bytes.TrimSpace(raw))
	if id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, argv := range [][]string{
		{ConfinementContainerCLI, "kill", "--signal", "KILL", id},
		{ConfinementContainerCLI, "delete", "--force", id},
	} {
		_ = exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
	}
	_ = os.Remove(filepath.Join(base, appleContainerCidFn))
}
