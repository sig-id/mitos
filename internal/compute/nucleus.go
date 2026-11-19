package compute

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Nucleus confinement (ADR 0008). Every launch runs in a rootless Nucleus
// strict-agent container on the native runtime. Mitos admits the posture;
// Nucleus enforces it and attests what it enforced in its control event
// stream, which is compared with the admitted posture before the session is
// handed out. A mismatch kills the container: the posture is never downgraded
// after admission.
//
// The workload sees only the workspace copy (/workspace, executable), the
// per-attempt home and temporary directories, read-only dependency inputs,
// and either the host system directories Nucleus binds (/bin, /usr, /lib,
// /lib64, /nix/store) or a pinned agent toolchain rootfs. The attempt base
// itself is never mounted: it holds the workspace history and baseline that
// harvest trusts.

const (
	ConfinementNucleus = "nucleus"

	nucleusWorkspace    = "/workspace"
	nucleusStateRoot    = "/mitos"
	nucleusPidsMax      = 4096
	nucleusCPUPeriod    = 100000
	nucleusStartTimeout = 30 * time.Second
	nucleusLeaf         = "mitos-server"
	nucleusEventsFile   = "nucleus-events.jsonl"
	nucleusConfigFD     = 3
	nucleusEventsFD     = 4
	nucleusEnvFD        = 5
)

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
	AgentToolchainRootfs string   `json:"agent_toolchain_rootfs,omitempty"`
	Volumes              []string `json:"volumes"`
	EnvFD                int      `json:"env_fd"`
	Command              []string `json:"command"`
}

// nucleusPosture is what the started event must attest.
type nucleusPosture struct {
	CgroupRoot  string
	Workspace   string
	MemoryBytes *int64
	CPUQuota    *int64
	PidsMax     int64
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

func within(root, path string) bool {
	return path == root || strings.HasPrefix(path, root+"/")
}

// renderNucleus compiles an admitted launch and a native command into the
// Nucleus document, the workload environment and the posture the started
// event must attest. It is pure apart from resolving executable symlinks.
func renderNucleus(cfg NucleusConfig, spec LaunchSpec, argv []string, dir string, env []string) (nucleusDocument, []string, nucleusPosture, error) {
	var doc nucleusDocument
	if spec.Posture.NetMode != NetOff {
		return doc, nil, nucleusPosture{}, fmt.Errorf("nucleus confinement supports network mode %q only; brokered egress awaits a Nucleus-side forwarder", NetOff)
	}
	base := filepath.Clean(spec.Base)
	workspace := filepath.Clean(spec.Workspace.Directory)
	if !filepath.IsAbs(base) || !filepath.IsAbs(workspace) {
		return doc, nil, nucleusPosture{}, errors.New("nucleus launch needs absolute attempt paths")
	}
	mounts := pathMap{{workspace, nucleusWorkspace}}
	var volumes []string
	for _, name := range []string{"home", "tmp", "inputs"} {
		host := filepath.Join(base, name)
		if name == "inputs" && !dirExists(host) {
			continue
		}
		guest := filepath.Join(nucleusStateRoot, name)
		access := "rw"
		if name == "inputs" {
			access = "ro"
		}
		mounts = append(mounts, struct{ host, guest string }{host, guest})
		volumes = append(volumes, host+":"+guest+":"+access+",caller-owned")
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
	posture := nucleusPosture{CgroupRoot: cfg.CgroupRoot, Workspace: workspace, PidsMax: nucleusPidsMax}
	doc = nucleusDocument{Name: nucleusName(base), Workspace: workspace, WorkspaceMode: "bind-rw", WorkspaceExec: true, Workdir: workdir, Rootless: true, Runtime: "native", ServiceMode: "strict-agent", TrustLevel: "trusted", Pids: nucleusPidsMax, Network: "none", AgentToolchainRootfs: cfg.Rootfs, Volumes: volumes, EnvFD: nucleusEnvFD, Command: command}
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
		ID             string `json:"id"`
		Name           string `json:"name"`
		Runtime        string `json:"runtime"`
		CgroupPath     string `json:"cgroup_path"`
		NetworkMode    string `json:"network_mode"`
		WorkspaceMount *struct {
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
	check(c.NetworkMode == "none", "network mode %q", c.NetworkMode)
	check(c.CgroupPath != "" && strings.HasPrefix(filepath.Clean(c.CgroupPath), p.CgroupRoot+"/"), "cgroup %q outside %s", c.CgroupPath, p.CgroupRoot)
	ws := c.WorkspaceMount
	check(ws != nil && ws.Source == p.Workspace && ws.Destination == nucleusWorkspace && ws.Mode == "bind-rw", "workspace mount %+v", ws)
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
	files    []*os.File // child ends, inherited as fd 3, 4, 5
	parent   []*os.File // our ends: config writer, events reader, env writer
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
	run.files = []*os.File{configRead, eventsWrite, envRead}
	run.parent = []*os.File{configWrite, eventsRead, envWrite}
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

// await blocks until the started event arrives and the attested posture
// matches the admitted one.
func (r *sandboxRun) await(ctx context.Context, exited <-chan struct{}) error {
	timer := time.NewTimer(nucleusStartTimeout)
	defer timer.Stop()
	select {
	case event := <-r.started:
		return r.posture.verify(event, r.name)
	case <-r.drained:
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
// descendant regardless of process group or session changes.
func (r *sandboxRun) kill() error {
	r.mu.Lock()
	cgroup := r.cgroup
	r.mu.Unlock()
	if cgroup == "" || !strings.HasPrefix(filepath.Clean(cgroup), r.posture.CgroupRoot+"/") {
		return nil
	}
	err := os.WriteFile(filepath.Join(cgroup, "cgroup.kill"), []byte("1"), 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
	spec := LaunchSpec{Base: n.cfg.ProbeDir, Workspace: Workspace{Directory: workspace}, Posture: Posture{NetMode: NetOff}}
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
	return Capabilities{CPUQuota: true, MemoryLimit: true, Isolation: "container", Egress: NetOff, DataMount: "bind-ro", NetModes: []string{NetOff}, WorkspaceConfined: true}, nil
}
