package compute

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Confinement describes an OS-enforced local sandbox for the local provider.
// The Linux-first implementation wraps every launched process in bubblewrap
// with only the workspace copy, dependency inputs, the per-attempt runtime
// home and required toolchains mounted. Nothing outside those mounts is
// visible, so reads are confined rather than advisory.
type Confinement struct {
	Tool    string `json:"tool"`
	NetMode string `json:"net_mode"`
}

const (
	ConfinementBwrap = "bwrap"
	NetOff           = "off"
	NetUnrestricted  = "unrestricted"
)

// NewConfinement validates the requested sandbox. It fails closed when the
// tool is missing instead of silently degrading to advisory execution.
func NewConfinement(tool, netMode string) (Confinement, error) {
	if tool == "" {
		return Confinement{}, nil
	}
	if tool != ConfinementBwrap {
		return Confinement{}, fmt.Errorf("unsupported confinement tool %q", tool)
	}
	if netMode == "" {
		netMode = NetOff
	}
	if netMode != NetOff && netMode != NetUnrestricted {
		return Confinement{}, fmt.Errorf("unsupported confinement network mode %q", netMode)
	}
	if _, err := exec.LookPath(ConfinementBwrap); err != nil {
		return Confinement{}, errors.New("confinement requested but bwrap is not installed")
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
	return Capabilities{Isolation: "container", Egress: egress, DataMount: "bind-ro", NetModes: modes, WorkspaceConfined: true}
}

// confineArgv wraps argv in the sandbox. Mounts are minimal and explicit: the
// toolchains and system configuration read-only, a fresh /tmp, the per-attempt
// runtime home, read-only dependency inputs and the writable workspace copy.
// Host control sockets under /run are deliberately not mounted: a read-only
// mount still lets a confined process issue requests to docker or the user's
// systemd manager over those sockets, which would defeat the confinement.
func confineArgv(c Confinement, argv []string, base, workspace string) ([]string, error) {
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
	if c.NetMode == NetOff {
		args = append(args, "--unshare-net")
	} else {
		args = append(args, "--share-net")
	}
	return append(args, append([]string{"--"}, argv...)...), nil
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
