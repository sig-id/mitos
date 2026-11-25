package compute

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"mitos/internal/resourcepolicy"
)

// Confinement describes an OS-enforced local sandbox for the local provider.
// On Linux every launched process is wrapped in bubblewrap with only the
// workspace copy, dependency inputs, the per-attempt runtime home and required
// toolchains mounted. On macOS (ADR 0011) every confined launch runs in its
// own lightweight Linux VM through apple/container, with the same minimal
// explicit mounts expressed as virtiofs volumes. Nothing outside those mounts
// is visible, so reads are confined rather than advisory.
type Confinement struct {
	Tool    string `json:"tool"`
	NetMode string `json:"net_mode"`
	// EgressSocket names a host UNIX socket carrying the mediated model
	// transport. When set, even an "unrestricted" posture runs without a
	// route to any host service – in a private network namespace on Linux, or
	// behind a vsock-forwarded socket with no interface at all on macOS – so
	// host services, including loopback listeners, stay unreachable.
	EgressSocket string `json:"egress_socket,omitempty"`
	// HelperExecutable is the host binary that provides the in-sandbox
	// egress loop. It is mounted read-only next to the workload. Linux only:
	// the apple-container guest runs GuestHelper from the image instead.
	HelperExecutable string `json:"-"`
	// Image is the OCI image reference whose filesystem the apple-container
	// workload boots from. It must ship the workload toolchain under
	// GuestBinDir, a Linux mitos build at GuestHelper, and /bin/sh – unless
	// ToolsDir provides the toolchain from the host instead.
	Image string `json:"image,omitempty"`
	// ToolsDir is a host directory of Linux guest binaries mounted read-only
	// into every apple-container VM at the same path, mirroring the bwrap
	// model of one shared host toolchain. When set, GuestBinDir defaults to
	// it, so workloads resolve there without baking tools into the image.
	ToolsDir string `json:"tools_dir,omitempty"`
	// GuestBinDir is the in-image directory confinement rewrites the
	// workload's argv[0] into (default /usr/local/bin, or ToolsDir when
	// set). apple-container only.
	GuestBinDir string `json:"guest_bin_dir,omitempty"`
	// GuestHelper is the in-image mitos binary that runs the in-guest egress
	// loop for mediated launches (default /usr/local/bin/mitos).
	GuestHelper string `json:"guest_helper,omitempty"`
}

const (
	ConfinementBwrap          = "bwrap"
	ConfinementAppleContainer = "apple-container"
	// ConfinementContainerCLI is the executable apple/container installs.
	ConfinementContainerCLI = "container"
	NetOff                  = "off"
	NetUnrestricted         = "unrestricted"
)

// DefaultConfinementTool names the platform's supported confinement backend:
// bubblewrap on Linux, apple/container on macOS (ADR 0011). Hosts without the
// tool fail closed inside NewConfinement, never by silently dropping the
// confinement.
func DefaultConfinementTool() string {
	if runtime.GOOS == "darwin" {
		return ConfinementAppleContainer
	}
	return ConfinementBwrap
}

// NewConfinement validates the requested sandbox. It fails closed when the
// tool is missing instead of silently degrading to advisory execution.
func NewConfinement(tool, netMode string) (Confinement, error) {
	if tool == "" {
		return Confinement{}, nil
	}
	if netMode == "" {
		netMode = NetOff
	}
	if netMode != NetOff && netMode != NetUnrestricted {
		return Confinement{}, fmt.Errorf("unsupported confinement network mode %q", netMode)
	}
	switch tool {
	case ConfinementBwrap:
		if _, err := exec.LookPath(ConfinementBwrap); err != nil {
			return Confinement{}, errors.New("confinement requested but bwrap is not installed")
		}
	case ConfinementAppleContainer:
		if _, err := exec.LookPath(ConfinementContainerCLI); err != nil {
			return Confinement{}, errors.New("confinement requested but the apple/container CLI (\"container\") is not installed; see ADR 0011")
		}
	default:
		return Confinement{}, fmt.Errorf("unsupported confinement tool %q", tool)
	}
	return Confinement{Tool: tool, NetMode: netMode}, nil
}

func (c Confinement) enabled() bool { return c.Tool != "" }

// Enabled reports whether OS confinement is configured, so callers outside
// the compute package can adapt admission and environment staging.
func (c Confinement) Enabled() bool { return c.enabled() }

func (c Confinement) capabilities() Capabilities {
	if !c.enabled() {
		return Capabilities{}
	}
	modes := []string{NetOff}
	egress := NetOff
	if c.NetMode == NetUnrestricted {
		modes = []string{NetOff, NetUnrestricted}
		egress = NetUnrestricted
	}
	caps := Capabilities{Isolation: "container", Egress: egress, DataMount: "bind-ro", NetModes: modes, WorkspaceConfined: true}
	// A per-container VM enforces real CPU and memory ceilings (ADR 0011),
	// where a bubblewrap process sandbox cannot.
	if c.Tool == ConfinementAppleContainer {
		caps.CPUQuota = true
		caps.MemoryLimit = true
	}
	return caps
}

// confineArgv wraps argv in the sandbox selected by the confinement tool.
// Mounts stay minimal and explicit on every platform; workdir is the resolved
// guest working directory, resources the admitted posture ceilings, which
// VM-backed tools can enforce, and env the workload's environment, which a
// VM guest does not inherit from the host process and must be passed
// explicitly. Host control sockets under /run are deliberately not mounted
// on Linux: a read-only mount still lets a confined process issue requests
// to docker or the user's systemd manager over those sockets, which would
// defeat the confinement.
func confineArgv(c Confinement, argv []string, base, workspace, workdir string, resources resourcepolicy.Agent, env []string) ([]string, error) {
	if c.Tool == ConfinementAppleContainer {
		return confineArgvApple(c, argv, base, workspace, workdir, resources, env)
	}
	return confineArgvBwrap(c, argv, base, workspace)
}

func confineArgvBwrap(c Confinement, argv []string, base, workspace string) ([]string, error) {
	if len(argv) == 0 || !filepath.IsAbs(argv[0]) {
		return nil, errors.New("command requires an absolute executable")
	}
	tool, err := exec.LookPath(c.Tool)
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
		tool,
		"--die-with-parent",
		"--unshare-ipc", "--unshare-pid", "--unshare-uts", "--unshare-cgroup",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
		"--tmpfs", tmp,
		"--bind", home, home,
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/bin", "/bin",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind-try", "/etc", "/etc",
		"--ro-bind-try", "/opt", "/opt",
		"--ro-bind-try", "/nix", "/nix",
		"--ro-bind-try", filepath.Dir(argv[0]), filepath.Dir(argv[0]),
		"--bind", workspace, workspace,
	}
	if inputs := filepath.Join(base, "inputs"); dirExists(inputs) {
		args = append(args, "--ro-bind", inputs, inputs)
	}
	// A mediated egress socket means the workload never shares the host
	// network namespace: model transport flows through the allowlisted relay
	// instead of direct egress.
	if c.EgressSocket != "" {
		if c.NetMode != NetUnrestricted {
			return nil, errors.New("mediated egress requires the unrestricted network mode")
		}
		if c.HelperExecutable == "" {
			return nil, errors.New("mediated egress requires the helper executable")
		}
		args = append(args, "--unshare-net", "--ro-bind", c.EgressSocket, c.EgressSocket, "--ro-bind-try", c.HelperExecutable, c.HelperExecutable)
		argv = egressLoopArgv(c, argv, base)
	} else if c.NetMode == NetOff {
		args = append(args, "--unshare-net")
	} else {
		args = append(args, "--share-net")
	}
	return append(args, append([]string{"--"}, argv...)...), nil
}

// egressLoopArgv starts the in-sandbox loop forwarder in the background, waits
// until its listener is ready, and execs the workload in place, so process
// identity, signals and the protocol streams stay with the workload. The
// forwarder's output is discarded: the workload owns stdio.
func egressLoopArgv(c Confinement, argv []string, base string) []string {
	ready := filepath.Join(base, "tmp", "egress.ready")
	script := fmt.Sprintf("%s egress-loop --socket %q --listen %q --ready %q >/dev/null 2>&1 & i=0; while [ ! -e %q ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done; exec \"$@\"", c.HelperExecutable, c.EgressSocket, EgressLoopAddr, ready, ready)
	return append([]string{"/bin/sh", "-c", script, "sh"}, argv...)
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
