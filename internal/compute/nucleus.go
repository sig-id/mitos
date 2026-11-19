package compute

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Nucleus confinement (ADR 0008). Every launch runs in a rootless Nucleus
// strict-agent container on the native runtime. Mitos admits the posture;
// Nucleus enforces it and attests what it enforced in its control event
// stream. Nucleus holds the workload on a start gate until that attestation
// matches the admitted posture; a mismatch closes the gate, so the workload
// never executes, and the posture is never downgraded after admission.
//
// The workload sees only the workspace copy (/workspace, executable), the
// per-attempt home and temporary directories, read-only dependency inputs and
// context files, and either the host system directories Nucleus binds (/bin, /usr, /lib,
// /lib64, /nix/store) or a pinned agent toolchain rootfs. The attempt base
// itself is never mounted: it holds the workspace history and baseline that
// harvest trusts.
//
// A brokered launch (NetBroker) keeps network mode none. Nucleus listens on
// loopback inside the container and relays each connection to the launch's
// broker socket, which is not mounted; the workload is the broker's client
// and reaches nothing else.

const (
	ConfinementNucleus = "nucleus"

	nucleusWorkspace    = "/workspace"
	nucleusStateRoot    = "/mitos"
	nucleusPidsMax      = 4096
	nucleusCPUPeriod    = 100000
	nucleusStartTimeout = 30 * time.Second
	// nucleusExitGrace is how long a supervisor whose container was revoked
	// may take to report the summary and exit before it is killed too.
	nucleusExitGrace  = 5 * time.Second
	nucleusLeaf       = "mitos-server"
	nucleusEventsFile = "nucleus-events.jsonl"
	nucleusConfigFD   = 3
	nucleusEventsFD   = 4
	nucleusEnvFD      = 5
	nucleusStartFD    = 6
)

// HarnessSyscalls are the opt-in system calls an agent and the developer
// toolchains it runs need beyond the strict-agent seccomp profile, which
// kills a process on any call it does not allow. Codex watches its
// configuration with inotify; touch, tar and git set timestamps; Go and tokio
// spawn processes through pidfds; V8 allocates protection keys; runtimes
// query priorities, limits and system information. Every entry is one
// Nucleus permits in strict-agent mode; io_uring, namespaces, memory policy,
// identity changes and extended attributes stay denied. They are the only
// calls a posture may add.
var HarnessSyscalls = []string{
	"inotify_init", "inotify_init1", "inotify_add_watch", "inotify_rm_watch",
	"utimensat", "utimes", "utime", "futimesat", "fchmodat2", "creat", "openat2",
	"pidfd_open", "pidfd_send_signal", "tkill", "rt_tgsigqueueinfo", "pause", "alarm",
	"epoll_pwait2", "futex_waitv", "futex_wake", "futex_wait", "futex_requeue", "membarrier",
	"timer_create", "timer_settime", "timer_gettime", "timer_getoverrun", "timer_delete",
	"sysinfo", "time", "getpriority", "setpriority", "setrlimit", "getresuid", "getresgid", "getsid", "capget",
	"sched_getattr", "sched_get_priority_max", "sched_get_priority_min", "sched_rr_get_interval",
	"pkey_alloc", "pkey_free", "pkey_mprotect", "mincore", "readahead",
	"sync", "syncfs", "splice", "tee", "sendfile", "recvmmsg", "sendmmsg",
}

// ErrMemoryExhausted reports a workload that the kernel killed at its admitted
// memory limit, as distinct from any other failure.
var ErrMemoryExhausted = errors.New("memory_exhausted: the workload was killed at its memory limit")

// nucleusHostPaths are the host directories Nucleus binds read-only into a
// native agent container that has no toolchain rootfs.
var nucleusHostPaths = []string{"/bin", "/usr", "/lib", "/lib64", "/nix/store"}

// NucleusConfig selects the Nucleus executable and the cgroup subtree that
// Mitos owns. The subtree must be delegated to the operator (systemd
// Delegate=yes); Mitos never infers delegation from cgroup ownership.
type NucleusConfig struct {
	Binary     string
	CgroupRoot string
	// Rootfs is an optional pinned agent toolchain rootfs. Without it the
	// workload uses the host system directories Nucleus binds.
	Rootfs string
	// ProbeDir holds the startup capability probe's scratch attempt.
	ProbeDir string
}

type nucleusRuntime struct {
	cfg  NucleusConfig
	caps Capabilities
}

// NewNucleus validates the configuration, reclaims containers left in the
// owned cgroup subtree by a previous server, and probes what this host
// actually enforces. It fails closed: an unusable Nucleus never degrades to
// unconfined execution.
func NewNucleus(ctx context.Context, cfg NucleusConfig) (*Local, error) {
	if !filepath.IsAbs(cfg.Binary) {
		return nil, errors.New("nucleus executable must be an absolute path")
	}
	if info, err := os.Stat(cfg.Binary); err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("nucleus executable is not usable: %s", cfg.Binary)
	}
	if cfg.Rootfs != "" && !filepath.IsAbs(cfg.Rootfs) {
		return nil, errors.New("nucleus toolchain rootfs must be an absolute path")
	}
	if !filepath.IsAbs(cfg.ProbeDir) {
		return nil, errors.New("nucleus probe directory must be an absolute path")
	}
	root, err := validateCgroupRoot(cfg.CgroupRoot)
	if err != nil {
		return nil, err
	}
	cfg.CgroupRoot = root
	if err = leaveCgroupRoot(root); err != nil {
		return nil, err
	}
	n := &nucleusRuntime{cfg: cfg}
	if _, err = n.reclaim(); err != nil {
		return nil, err
	}
	if n.caps, err = n.probe(ctx); err != nil {
		return nil, fmt.Errorf("nucleus capability probe failed: %w", err)
	}
	return &Local{sandbox: n, name: ConfinementNucleus}, nil
}

func (n *nucleusRuntime) capabilities() Capabilities { return n.caps }

// nucleusCapabilities is what a passing probe proves: enforced CPU and memory
// caps, a confined workspace, and egress that is either off or brokered.
var nucleusCapabilities = Capabilities{CPUQuota: true, MemoryLimit: true, Isolation: "container", Egress: "broker", DataMount: "bind-ro", NetModes: []string{NetOff, NetBroker}, WorkspaceConfined: true, ReadOnlyWorkspace: true}

const cgroupMount = "/sys/fs/cgroup"

func validateCgroupRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("nucleus confinement requires NUCLEUS_CGROUP_ROOT: run the server in a delegated systemd unit (Delegate=yes) and name its cgroup")
	}
	clean := filepath.Clean(root)
	if !filepath.IsAbs(clean) || clean == cgroupMount || !strings.HasPrefix(clean, cgroupMount+"/") {
		return "", fmt.Errorf("NUCLEUS_CGROUP_ROOT must be a cgroup below %s", cgroupMount)
	}
	for _, name := range []string{"cgroup.procs", "cgroup.subtree_control", "cgroup.controllers"} {
		if _, err := os.Stat(filepath.Join(clean, name)); err != nil {
			return "", fmt.Errorf("NUCLEUS_CGROUP_ROOT is not a cgroup v2 directory: %w", err)
		}
	}
	return clean, nil
}

// ownCgroup reports this process's cgroup v2 path.
func ownCgroup() (string, error) {
	raw, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(cgroupMount, rest), nil
		}
	}
	return "", errors.New("no cgroup v2 membership")
}

// leaveCgroupRoot moves the server out of the delegated root when it runs
// there. A cgroup with member processes cannot enable controllers for its
// children, so Nucleus could not enforce limits on container cgroups.
func leaveCgroupRoot(root string) error {
	own, err := ownCgroup()
	if err != nil {
		return err
	}
	if own != root {
		return nil
	}
	leaf := filepath.Join(root, nucleusLeaf)
	if err = os.Mkdir(leaf, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create server cgroup: %w", err)
	}
	if err = os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0); err != nil {
		return fmt.Errorf("move server out of the delegated cgroup root: %w", err)
	}
	return nil
}

// reclaim kills and removes container cgroups left in the owned subtree by a
// previous server. The subtree is exclusively this server's, so every
// container found there belongs to an attempt whose outcome is now unknown.
func (n *nucleusRuntime) reclaim() (int, error) {
	entries, err := os.ReadDir(n.cfg.CgroupRoot)
	if err != nil {
		return 0, err
	}
	reclaimed := 0
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, "nucleus-") || name == "nucleus-supervisor" {
			continue
		}
		if err = killCgroup(filepath.Join(n.cfg.CgroupRoot, name), 5*time.Second); err != nil {
			return reclaimed, fmt.Errorf("reclaim %s: %w", name, err)
		}
		reclaimed++
	}
	return reclaimed, nil
}

// killCgroup kills every process in a cgroup and removes it once empty.
func killCgroup(path string, wait time.Duration) error {
	if err := os.WriteFile(filepath.Join(path, "cgroup.kill"), []byte("1"), 0); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	deadline := time.Now().Add(wait)
	for {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// nucleusDocument is the complete launch request passed on --config-fd.
// Field names follow Nucleus's config schema (long option names in
// snake_case); the document owns the launch, so no option is passed as argv.
type nucleusDocument struct {
	Name                 string   `json:"name"`
	Workspace            string   `json:"workspace"`
	WorkspaceMode        string   `json:"workspace_mode"`
	WorkspaceExec        bool     `json:"workspace_exec"`
	Workdir              string   `json:"workdir"`
	Rootless             bool     `json:"rootless"`
	Runtime              string   `json:"runtime"`
	ServiceMode          string   `json:"service_mode"`
	TrustLevel           string   `json:"trust_level"`
	Memory               string   `json:"memory,omitempty"`
	CPUs                 float64  `json:"cpus,omitempty"`
	Pids                 int      `json:"pids"`
	Network              string   `json:"network"`
	BrokerSocket         string   `json:"broker_socket,omitempty"`
	BrokerProxyListen    string   `json:"broker_proxy_listen,omitempty"`
	BrokerNoProxyEnv     bool     `json:"credential_broker_no_proxy_env,omitempty"`
	SeccompAllow         []string `json:"seccomp_allow,omitempty"`
	AgentToolchainRootfs string   `json:"agent_toolchain_rootfs,omitempty"`
	Volumes              []string `json:"volumes"`
	EnvFD                int      `json:"env_fd"`
	StartFD              int      `json:"start_fd"`
	Command              []string `json:"command"`
}

// nucleusPosture is what the started event must attest.
type nucleusPosture struct {
	CgroupRoot    string
	Workspace     string
	WorkspaceMode string
	// NetworkMode is the attested network mode: "none", or "broker-socket"
	// when loopback is relayed to a broker.
	NetworkMode string
	// BrokerListen is the in-container broker address, empty without one.
	BrokerListen string
	MemoryBytes  *int64
	CPUQuota     *int64
	PidsMax      int64
}

// pathMap translates host paths of an attempt into container paths.
type pathMap []struct{ host, guest string }

func (m pathMap) guest(path string) (string, bool) {
	best := -1
	for i, entry := range m {
		if path == entry.host || strings.HasPrefix(path, entry.host+"/") {
			if best < 0 || len(entry.host) > len(m[best].host) {
				best = i
			}
		}
	}
	if best < 0 {
		return "", false
	}
	return m[best].guest + strings.TrimPrefix(path, m[best].host), true
}

const (
	nucleusInputs  = nucleusStateRoot + "/inputs"
	nucleusContext = nucleusStateRoot + "/context"
)

// nucleusMounts is the attempt's path namespace: the workspace copy first,
// then the per-attempt home and temporary directories, and the read-only
// dependency inputs and context files that spill out of an agent prompt.
func nucleusMounts(spec LaunchSpec) (base, workspace string, mounts pathMap, err error) {
	base = filepath.Clean(spec.Base)
	workspace = filepath.Clean(spec.Workspace.Directory)
	if !filepath.IsAbs(base) || !filepath.IsAbs(workspace) {
		return "", "", nil, errors.New("nucleus launch needs absolute attempt paths")
	}
	mounts = pathMap{{workspace, nucleusWorkspace}}
	for _, name := range []string{"home", "tmp", "inputs", "context"} {
		mounts = append(mounts, struct{ host, guest string }{filepath.Join(base, name), filepath.Join(nucleusStateRoot, name)})
	}
	return base, workspace, mounts, nil
}

// GuestPath reports where an attempt path appears to the launched workload,
// so callers can describe attempt paths in the workload's own namespace. A
// path the workload cannot see is an error.
func (spec LaunchSpec) GuestPath(host string) (string, error) {
	if spec.Provider != ConfinementNucleus {
		return host, nil
	}
	_, _, mounts, err := nucleusMounts(spec)
	if err != nil {
		return "", err
	}
	if guest, ok := mounts.guest(filepath.Clean(host)); ok {
		return guest, nil
	}
	return "", fmt.Errorf("path %s is not visible in the sandbox", host)
}

func within(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// renderNucleus compiles an admitted launch and a native command into the
// Nucleus document, the workload environment and the posture the started
// event must attest. It is pure apart from resolving executable symlinks.
func renderNucleus(cfg NucleusConfig, spec LaunchSpec, argv []string, dir string, env []string) (nucleusDocument, []string, nucleusPosture, error) {
	var doc nucleusDocument
	broker := ""
	switch spec.Posture.NetMode {
	case NetOff:
	case NetBroker:
		path, ok := strings.CutPrefix(spec.Posture.BrokerEndpoint, "unix://")
		if !ok || !filepath.IsAbs(path) {
			return doc, nil, nucleusPosture{}, errors.New("nucleus brokered egress needs a local unix broker endpoint")
		}
		broker = filepath.Clean(path)
	default:
		return doc, nil, nucleusPosture{}, fmt.Errorf("nucleus confinement supports network modes %q and %q only", NetOff, NetBroker)
	}
	base, workspace, mounts, err := nucleusMounts(spec)
	if err != nil {
		return doc, nil, nucleusPosture{}, err
	}
	var volumes []string
	for _, mount := range mounts[1:] {
		access := "rw"
		if mount.guest == nucleusInputs || mount.guest == nucleusContext {
			if !dirExists(mount.host) {
				continue
			}
			access = "ro"
		}
		volumes = append(volumes, mount.host+":"+mount.guest+":"+access+",caller-owned")
	}
	translate := func(value string) (string, error) {
		candidate := value
		prefix := ""
		if !filepath.IsAbs(candidate) {
			if i := strings.IndexByte(value, '='); i > 0 && filepath.IsAbs(value[i+1:]) {
				prefix, candidate = value[:i+1], value[i+1:]
			} else {
				return value, nil
			}
		}
		candidate = filepath.Clean(candidate)
		if guest, ok := mounts.guest(candidate); ok {
			return prefix + guest, nil
		}
		if within(base, candidate) {
			return "", fmt.Errorf("path %s is not visible in the sandbox", candidate)
		}
		return value, nil
	}
	executable := argv[0]
	if cfg.Rootfs == "" {
		resolved, err := filepath.EvalSymlinks(executable)
		if err != nil {
			return doc, nil, nucleusPosture{}, fmt.Errorf("resolve executable: %w", err)
		}
		if guest, ok := mounts.guest(resolved); ok && strings.HasPrefix(guest, nucleusWorkspace) {
			executable = guest
		} else if visibleHostPath(resolved) {
			executable = resolved
		} else {
			return doc, nil, nucleusPosture{}, fmt.Errorf("executable %s is not visible in the sandbox; install it in the toolchain rootfs", resolved)
		}
	}
	command := []string{executable}
	for _, arg := range argv[1:] {
		translated, err := translate(arg)
		if err != nil {
			return doc, nil, nucleusPosture{}, err
		}
		command = append(command, translated)
	}
	workdir, ok := mounts.guest(filepath.Clean(dir))
	if !ok {
		return doc, nil, nucleusPosture{}, fmt.Errorf("working directory %s is not visible in the sandbox", dir)
	}
	guestEnv := make([]string, 0, len(env))
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if key == "PATH" {
			value = sandboxPath(value, mounts, cfg.Rootfs != "")
		} else {
			var err error
			if value, err = translate(value); err != nil {
				return doc, nil, nucleusPosture{}, fmt.Errorf("environment %s: %w", key, err)
			}
		}
		guestEnv = append(guestEnv, key+"="+value)
	}
	mode := "bind-rw"
	if spec.Posture.ReadOnlyWorkspace {
		mode = "bind-ro"
	}
	posture := nucleusPosture{CgroupRoot: cfg.CgroupRoot, Workspace: workspace, WorkspaceMode: mode, NetworkMode: "none", PidsMax: nucleusPidsMax}
	doc = nucleusDocument{Name: nucleusName(base), Workspace: workspace, WorkspaceMode: mode, WorkspaceExec: true, Workdir: workdir, Rootless: true, Runtime: "native", ServiceMode: "strict-agent", TrustLevel: "trusted", Pids: nucleusPidsMax, Network: "none", AgentToolchainRootfs: cfg.Rootfs, Volumes: volumes, EnvFD: nucleusEnvFD, StartFD: nucleusStartFD, Command: command}
	for _, name := range spec.Posture.SeccompAllow {
		if !slices.Contains(HarnessSyscalls, name) {
			return doc, nil, nucleusPosture{}, fmt.Errorf("posture may not allow system call %q", name)
		}
	}
	doc.SeccompAllow = slices.Clone(spec.Posture.SeccompAllow)
	if broker != "" {
		// The harness is configured with explicit base URLs; proxy variables
		// would only steer other tools into tunnels the broker refuses.
		doc.BrokerSocket, doc.BrokerProxyListen, doc.BrokerNoProxyEnv = broker, EgressLoopAddr, true
		posture.NetworkMode, posture.BrokerListen = "broker-socket", EgressLoopAddr
	}
	if limit := spec.Posture.Resources.MemoryMiB; limit.Mode == "limit" {
		doc.Memory = strconv.FormatInt(limit.Amount, 10) + "M"
		bytes := limit.Amount << 20
		posture.MemoryBytes = &bytes
	}
	if limit := spec.Posture.Resources.CPUMillicores; limit.Mode == "limit" {
		doc.CPUs = float64(limit.Amount) / 1000
		quota := limit.Amount * nucleusCPUPeriod / 1000
		posture.CPUQuota = &quota
	}
	return doc, guestEnv, posture, nil
}

func visibleHostPath(path string) bool {
	for _, root := range nucleusHostPaths {
		if within(root, path) {
			return true
		}
	}
	return false
}

// sandboxPath keeps the PATH entries the workload can actually use. Host
// profile directories (for example NixOS /run/current-system/sw/bin) resolve
// to store paths that Nucleus binds; anything else is dropped rather than
// leaving the workload to find binaries that are not there.
func sandboxPath(value string, mounts pathMap, rootfs bool) string {
	var kept []string
	for _, entry := range filepath.SplitList(value) {
		if entry == "" || !filepath.IsAbs(entry) {
			continue
		}
		if guest, ok := mounts.guest(filepath.Clean(entry)); ok {
			kept = append(kept, guest)
			continue
		}
		if rootfs {
			kept = append(kept, entry)
			continue
		}
		resolved, err := filepath.EvalSymlinks(entry)
		if err == nil && visibleHostPath(resolved) {
			kept = append(kept, resolved)
		}
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

// nucleusName derives a stable container name from the attempt base.
func nucleusName(base string) string {
	sum := sha256.Sum256([]byte(base))
	return "mitos-" + hex.EncodeToString(sum[:8])
}

// nucleusEvent is the subset of a Nucleus control event Mitos verifies.
type nucleusEvent struct {
	Type      string `json:"type"`
	Container struct {
		ID                string `json:"id"`
		Name              string `json:"name"`
		Runtime           string `json:"runtime"`
		CgroupPath        string `json:"cgroup_path"`
		NetworkMode       string `json:"network_mode"`
		BrokerProxyListen string `json:"broker_proxy_listen"`
		WorkspaceMount    *struct {
			Source       string `json:"source"`
			Destination  string `json:"destination"`
			Mode         string `json:"mode"`
			AllowExecute bool   `json:"allow_execute"`
		} `json:"workspace_mount"`
	} `json:"container"`
	Security struct {
		SeccompMode           string `json:"seccomp_mode"`
		SeccompStatus         string `json:"seccomp_status"`
		LandlockStatus        string `json:"landlock_status"`
		CapabilitiesStatus    string `json:"capabilities_status"`
		Rootless              bool   `json:"rootless"`
		AllowDegradedSecurity bool   `json:"allow_degraded_security"`
	} `json:"security"`
	ResourceLimits struct {
		MemoryBytes *int64 `json:"memory_bytes"`
		CPUQuotaUS  *int64 `json:"cpu_quota_us"`
		CPUPeriodUS *int64 `json:"cpu_period_us"`
		PidsMax     *int64 `json:"pids_max"`
	} `json:"resource_limits"`
	ExitStatus *struct {
		Code  *int   `json:"code"`
		Error string `json:"error"`
	} `json:"exit_status"`
	ResourceStats *struct {
		MemoryEvents *struct {
			OOMKill uint64 `json:"oom_kill"`
		} `json:"memory_events"`
	} `json:"resource_stats"`
}

// verify compares the attested posture with the admitted one. Every field is
// checked; any difference refuses the session.
func (p nucleusPosture) verify(event nucleusEvent, name string) error {
	var problems []string
	check := func(ok bool, format string, args ...any) {
		if !ok {
			problems = append(problems, fmt.Sprintf(format, args...))
		}
	}
	c, s, r := event.Container, event.Security, event.ResourceLimits
	check(event.Type == "container_started", "event %q", event.Type)
	check(c.Name == name, "container name %q", c.Name)
	check(c.Runtime == "native", "runtime %q", c.Runtime)
	check(c.NetworkMode == p.NetworkMode, "network mode %q", c.NetworkMode)
	check(c.BrokerProxyListen == p.BrokerListen, "broker listener %q", c.BrokerProxyListen)
	check(c.CgroupPath != "" && strings.HasPrefix(filepath.Clean(c.CgroupPath), p.CgroupRoot+"/"), "cgroup %q outside %s", c.CgroupPath, p.CgroupRoot)
	ws := c.WorkspaceMount
	check(ws != nil && ws.Source == p.Workspace && ws.Destination == nucleusWorkspace && ws.Mode == p.WorkspaceMode, "workspace mount %+v", ws)
	check(s.SeccompMode == "enforce" && s.SeccompStatus == "installed", "seccomp %s/%s", s.SeccompMode, s.SeccompStatus)
	check(s.LandlockStatus == "fully_enforced", "landlock %s", s.LandlockStatus)
	check(s.CapabilitiesStatus == "drop_all", "capabilities %s", s.CapabilitiesStatus)
	check(s.Rootless && !s.AllowDegradedSecurity, "rootless=%t degraded=%t", s.Rootless, s.AllowDegradedSecurity)
	check(equalLimit(r.MemoryBytes, p.MemoryBytes), "memory limit %s", formatLimit(r.MemoryBytes))
	if p.CPUQuota == nil {
		check(r.CPUQuotaUS == nil, "unrequested cpu quota %s", formatLimit(r.CPUQuotaUS))
	} else {
		check(r.CPUQuotaUS != nil && r.CPUPeriodUS != nil && *r.CPUQuotaUS**r.CPUPeriodUS != 0 && *r.CPUQuotaUS*nucleusCPUPeriod == *p.CPUQuota**r.CPUPeriodUS, "cpu quota %s/%s", formatLimit(r.CPUQuotaUS), formatLimit(r.CPUPeriodUS))
	}
	check(r.PidsMax != nil && *r.PidsMax == p.PidsMax, "pids limit %s", formatLimit(r.PidsMax))
	if len(problems) > 0 {
		return fmt.Errorf("nucleus did not attest the admitted posture: %s", strings.Join(problems, "; "))
	}
	return nil
}

func equalLimit(got, want *int64) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	return *got == *want
}

func formatLimit(v *int64) string {
	if v == nil {
		return "none"
	}
	return strconv.FormatInt(*v, 10)
}

// sandboxRun is one Nucleus invocation: the supervisor command, its control
// descriptors and the attested container identity.
type sandboxRun struct {
	argv     []string
	dir      string
	env      []string
	files    []*os.File // child ends, inherited as fd 3, 4, 5, 6
	parent   []*os.File // our ends: config writer, events reader, env writer, start gate writer
	config   []byte
	guestEnv []byte
	posture  nucleusPosture
	name     string
	record   string

	started chan nucleusEvent
	drained chan struct{}
	mu      sync.Mutex
	cgroup  string
	summary *nucleusEvent
	readErr error
}

func (n *nucleusRuntime) launch(spec LaunchSpec, argv []string, dir string, env []string) (*sandboxRun, error) {
	doc, guestEnv, posture, err := renderNucleus(n.cfg, spec, argv, dir, env)
	if err != nil {
		return nil, err
	}
	config, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	envJSON, err := json.Marshal(guestEnv)
	if err != nil {
		return nil, err
	}
	run := &sandboxRun{config: config, guestEnv: envJSON, posture: posture, name: doc.Name, record: filepath.Join(spec.Base, nucleusEventsFile), started: make(chan nucleusEvent, 1), drained: make(chan struct{})}
	// The supervisor gets only what Nucleus needs; the workload environment
	// travels on its own descriptor and never appears in argv or here.
	run.argv = []string{n.cfg.Binary, "run", "--config-fd", strconv.Itoa(nucleusConfigFD), "--events-fd", strconv.Itoa(nucleusEventsFD)}
	run.dir = spec.Base
	run.env = []string{"NUCLEUS_CGROUP_ROOT=" + n.cfg.CgroupRoot, "PATH=/usr/bin:/bin"}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		run.env = append(run.env, "XDG_RUNTIME_DIR="+runtimeDir)
	}
	configRead, configWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	eventsRead, eventsWrite, err := os.Pipe()
	if err != nil {
		closeFiles(configRead, configWrite)
		return nil, err
	}
	envRead, envWrite, err := os.Pipe()
	if err != nil {
		closeFiles(configRead, configWrite, eventsRead, eventsWrite)
		return nil, err
	}
	// The gate's read end is the only one Nucleus inherits, so closing our
	// write end without a byte is an EOF that refuses the start.
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		closeFiles(configRead, configWrite, eventsRead, eventsWrite, envRead, envWrite)
		return nil, err
	}
	run.files = []*os.File{configRead, eventsWrite, envRead, gateRead}
	run.parent = []*os.File{configWrite, eventsRead, envWrite, gateWrite}
	return run, nil
}

func closeFiles(files ...*os.File) {
	for _, f := range files {
		if f != nil {
			_ = f.Close()
		}
	}
}

// begin runs after the supervisor started: it releases the child descriptor
// ends, feeds the launch document and environment, and reads control events.
func (r *sandboxRun) begin() {
	closeFiles(r.files...)
	write := func(f *os.File, data []byte) {
		_, _ = f.Write(data)
		_ = f.Close()
	}
	go write(r.parent[0], r.config)
	go write(r.parent[2], r.guestEnv)
	go r.read(r.parent[1])
}

func (r *sandboxRun) read(events *os.File) {
	defer close(r.drained)
	defer events.Close()
	record, recordErr := os.OpenFile(r.record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if record != nil {
		defer record.Close()
	}
	scanner := bufio.NewScanner(events)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if record != nil {
			_, _ = record.Write(append(append([]byte(nil), line...), '\n'))
		}
		var event nucleusEvent
		if err := json.Unmarshal(line, &event); err != nil {
			r.fail(fmt.Errorf("malformed nucleus event: %w", err))
			continue
		}
		switch event.Type {
		case "container_started":
			r.mu.Lock()
			r.cgroup = event.Container.CgroupPath
			r.mu.Unlock()
			select {
			case r.started <- event:
			default:
				r.fail(errors.New("duplicate nucleus started event"))
			}
		case "container_summary":
			r.mu.Lock()
			r.summary = &event
			r.mu.Unlock()
		}
	}
	if err := scanner.Err(); err != nil {
		r.fail(err)
	}
	if recordErr != nil {
		r.fail(fmt.Errorf("record nucleus events: %w", recordErr))
	}
}

func (r *sandboxRun) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.readErr = errors.Join(r.readErr, err)
}

// await blocks until the started event arrives, then releases the held
// workload only if the attested posture matches the admitted one. Every other
// outcome closes the gate, so the workload never runs.
func (r *sandboxRun) await(ctx context.Context, exited <-chan struct{}) error {
	gate := r.parent[3]
	err := r.attest(ctx, exited)
	if err == nil {
		if _, err = gate.Write([]byte{1}); err != nil {
			err = fmt.Errorf("release nucleus start gate: %w", err)
		}
	}
	_ = gate.Close()
	return err
}

func (r *sandboxRun) attest(ctx context.Context, exited <-chan struct{}) error {
	timer := time.NewTimer(nucleusStartTimeout)
	defer timer.Stop()
	select {
	case event := <-r.started:
		return r.posture.verify(event, r.name)
	case <-r.drained:
		// A short stream can close right after its started event; select
		// does not prefer either ready case, so look for it first.
		select {
		case event := <-r.started:
			return r.posture.verify(event, r.name)
		default:
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return errors.Join(errors.New("nucleus exited before the container started"), r.readErr)
	case <-exited:
		return errors.New("nucleus exited before the container started")
	case <-timer.C:
		return errors.New("nucleus did not report a started container")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// kill revokes the container through its cgroup, which reaches every
// descendant regardless of process group or session changes. It reports
// whether a started container was revoked, so the caller can leave the
// supervisor alive to report the outcome.
func (r *sandboxRun) kill() (bool, error) {
	r.mu.Lock()
	cgroup := r.cgroup
	r.mu.Unlock()
	if cgroup == "" || !strings.HasPrefix(filepath.Clean(cgroup), r.posture.CgroupRoot+"/") {
		return false, nil
	}
	err := os.WriteFile(filepath.Join(cgroup, "cgroup.kill"), []byte("1"), 0)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return err == nil, err
}

// finish waits for the event stream to close after the supervisor exited and
// reports a stream or recording failure, which makes the outcome untrusted.
func (r *sandboxRun) finish() error {
	select {
	case <-r.drained:
	case <-time.After(5 * time.Second):
		return errors.New("nucleus event stream did not close")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.summary == nil {
		return errors.Join(errors.New("nucleus reported no container summary"), r.readErr)
	}
	return r.readErr
}

// memoryExhausted reports whether the kernel OOM-killed a process of the
// container at its memory limit.
func (r *sandboxRun) memoryExhausted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.summary
	return stats != nil && stats.ResourceStats != nil && stats.ResourceStats.MemoryEvents != nil && stats.ResourceStats.MemoryEvents.OOMKill > 0
}

func (r *sandboxRun) close() { closeFiles(append(r.files, r.parent...)...) }

// probe launches one container with every enforced limit and derives the
// capabilities from its attested posture rather than from configuration.
func (n *nucleusRuntime) probe(ctx context.Context) (Capabilities, error) {
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		return Capabilities{}, err
	}
	if err = os.RemoveAll(n.cfg.ProbeDir); err != nil {
		return Capabilities{}, err
	}
	workspace := filepath.Join(n.cfg.ProbeDir, "workspace")
	for _, dir := range []string{workspace, filepath.Join(n.cfg.ProbeDir, "home"), filepath.Join(n.cfg.ProbeDir, "tmp")} {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			return Capabilities{}, err
		}
	}
	// The canary is brokered, so brokered egress is advertised only when this
	// Nucleus accepts and attests a broker. Nothing connects to the socket.
	socket := filepath.Join(n.cfg.ProbeDir, "broker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return Capabilities{}, fmt.Errorf("probe broker socket: %w", err)
	}
	defer listener.Close()
	spec := LaunchSpec{Base: n.cfg.ProbeDir, Workspace: Workspace{Directory: workspace}, Posture: Posture{NetMode: NetBroker, BrokerEndpoint: "unix://" + socket}}
	spec.Posture.Resources.MemoryMiB.Mode, spec.Posture.Resources.MemoryMiB.Amount = "limit", 64
	spec.Posture.Resources.CPUMillicores.Mode, spec.Posture.Resources.CPUMillicores.Amount = "limit", 500
	if n.cfg.Rootfs != "" {
		shell = "/bin/sh"
	}
	probe := &Local{sandbox: &nucleusRuntime{cfg: n.cfg, caps: Capabilities{Isolation: "container"}}}
	h := &localHandle{owner: probe, spec: spec}
	session, err := probe.startCommand(ctx, h, Command{Argv: []string{shell, "-c", "test -w /workspace && test -w \"$HOME\""}, NativeEnv: []string{"HOME=" + filepath.Join(n.cfg.ProbeDir, "home"), "PATH=/usr/bin:/bin"}})
	if err != nil {
		return Capabilities{}, err
	}
	output, err := session.Result()
	if err != nil {
		return Capabilities{}, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	_ = os.RemoveAll(n.cfg.ProbeDir)
	return nucleusCapabilities, nil
}
