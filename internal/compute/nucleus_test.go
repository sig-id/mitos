package compute

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/resourcepolicy"
)

func nucleusSpec(t *testing.T, base string) LaunchSpec {
	t.Helper()
	workspace := filepath.Join(base, "workspace")
	for _, dir := range []string{workspace, filepath.Join(base, "home"), filepath.Join(base, "tmp"), filepath.Join(base, "inputs")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return LaunchSpec{Provider: ConfinementNucleus, Base: base, Workspace: Workspace{Mode: "copy", Directory: workspace}, Posture: Posture{NetMode: NetOff, Resources: resourcepolicy.Agent{MemoryMiB: resourcepolicy.Capped(512), CPUMillicores: resourcepolicy.Capped(1500)}}}
}

func hostShell(t *testing.T) string {
	t.Helper()
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skip("no /bin/sh")
	}
	if !visibleHostPath(shell) {
		t.Skipf("/bin/sh resolves outside the paths Nucleus binds: %s", shell)
	}
	return shell
}

func TestRenderNucleusTranslatesAttemptPathsAndFreezesLimits(t *testing.T) {
	base := t.TempDir()
	spec := nucleusSpec(t, base)
	shell := hostShell(t)
	cfg := NucleusConfig{Binary: "/usr/bin/nucleus", CgroupRoot: "/sys/fs/cgroup/mitos"}
	doc, env, posture, err := renderNucleus(cfg, spec, []string{"/bin/sh", "-c", "true", "--out=" + filepath.Join(base, "home", "result.txt"), filepath.Join(base, "workspace", "src")}, filepath.Join(base, "workspace", "src"), []string{"HOME=" + filepath.Join(base, "home"), "TMPDIR=" + filepath.Join(base, "tmp"), "MITOS_WORKSPACE=" + spec.Workspace.Directory, "PATH=/nonexistent/bin:" + filepath.Dir(shell), "LANG=C.UTF-8"})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Command[0] != shell || doc.Command[3] != "--out=/mitos/home/result.txt" || doc.Command[4] != "/workspace/src" || doc.Workdir != "/workspace/src" {
		t.Fatalf("command or workdir not translated: %q %q", doc.Command, doc.Workdir)
	}
	if doc.Workspace != spec.Workspace.Directory || !doc.WorkspaceExec || doc.WorkspaceMode != "bind-rw" || doc.Network != "none" || doc.ServiceMode != "strict-agent" || doc.Runtime != "native" || !doc.Rootless || doc.EnvFD != nucleusEnvFD || doc.StartFD != nucleusStartFD {
		t.Fatalf("posture fields not fixed: %+v", doc)
	}
	if doc.Memory != "512M" || doc.CPUs != 1.5 || doc.Pids != nucleusPidsMax {
		t.Fatalf("limits not rendered: %+v", doc)
	}
	if *posture.MemoryBytes != 512<<20 || *posture.CPUQuota != 150000 || posture.PidsMax != nucleusPidsMax {
		t.Fatalf("expected posture wrong: %+v", posture)
	}
	wantVolumes := []string{filepath.Join(base, "home") + ":/mitos/home:rw,caller-owned", filepath.Join(base, "tmp") + ":/mitos/tmp:rw,caller-owned", filepath.Join(base, "inputs") + ":/mitos/inputs:ro,caller-owned"}
	if strings.Join(doc.Volumes, "|") != strings.Join(wantVolumes, "|") {
		t.Fatalf("volumes %q", doc.Volumes)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"HOME=/mitos/home", "TMPDIR=/mitos/tmp", "MITOS_WORKSPACE=/workspace", "PATH=" + filepath.Dir(shell), "LANG=C.UTF-8"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("environment missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, base) || strings.Contains(strings.Join(doc.Command, " "), base) {
		t.Fatalf("host attempt path leaked into the sandbox:\n%s\n%q", joined, doc.Command)
	}
	// The document is deterministic: the same launch renders the same bytes.
	again, _, _, _ := renderNucleus(cfg, spec, []string{"/bin/sh", "-c", "true", "--out=" + filepath.Join(base, "home", "result.txt"), filepath.Join(base, "workspace", "src")}, filepath.Join(base, "workspace", "src"), nil)
	first, _ := json.Marshal(doc)
	second, _ := json.Marshal(again)
	if string(first) != string(second) {
		t.Fatalf("render not deterministic:\n%s\n%s", first, second)
	}
}

func TestRenderNucleusRefusesInvisiblePathsAndUnsupportedNetworks(t *testing.T) {
	base := t.TempDir()
	spec := nucleusSpec(t, base)
	hostShell(t)
	cfg := NucleusConfig{Binary: "/usr/bin/nucleus", CgroupRoot: "/sys/fs/cgroup/mitos"}
	cases := map[string]struct {
		argv []string
		dir  string
		env  []string
		spec LaunchSpec
	}{
		// The history and baseline harvest trusts are never exposed.
		"history argument": {argv: []string{"/bin/sh", filepath.Join(base, "history.git")}, dir: spec.Workspace.Directory, spec: spec},
		"history env":      {argv: []string{"/bin/sh"}, dir: spec.Workspace.Directory, env: []string{"GIT_DIR=" + filepath.Join(base, "history.git")}, spec: spec},
		"base workdir":     {argv: []string{"/bin/sh"}, dir: base, spec: spec},
		"host executable":  {argv: []string{os.Args[0]}, dir: spec.Workspace.Directory, spec: spec},
	}
	broker := spec
	broker.Posture.NetMode = "deny-all-except-broker"
	cases["brokered network"] = struct {
		argv []string
		dir  string
		env  []string
		spec LaunchSpec
	}{argv: []string{"/bin/sh"}, dir: spec.Workspace.Directory, spec: broker}
	for name, tc := range cases {
		if name == "host executable" && visibleHostPath(os.Args[0]) {
			continue
		}
		if _, _, _, err := renderNucleus(cfg, tc.spec, tc.argv, tc.dir, tc.env); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func startedEvent(name, root, workspace string) nucleusEvent {
	var e nucleusEvent
	e.Type = "container_started"
	e.Container.Name = name
	e.Container.Runtime = "native"
	e.Container.NetworkMode = "none"
	e.Container.CgroupPath = root + "/nucleus-abc"
	e.Container.WorkspaceMount = &struct {
		Source       string `json:"source"`
		Destination  string `json:"destination"`
		Mode         string `json:"mode"`
		AllowExecute bool   `json:"allow_execute"`
	}{workspace, "/workspace", "bind-rw", true}
	e.Security.SeccompMode, e.Security.SeccompStatus = "enforce", "installed"
	e.Security.LandlockStatus, e.Security.CapabilitiesStatus, e.Security.Rootless = "fully_enforced", "drop_all", true
	memory, quota, period, pids := int64(512<<20), int64(150000), int64(100000), int64(nucleusPidsMax)
	e.ResourceLimits.MemoryBytes, e.ResourceLimits.CPUQuotaUS, e.ResourceLimits.CPUPeriodUS, e.ResourceLimits.PidsMax = &memory, &quota, &period, &pids
	return e
}

func TestNucleusPostureVerificationRefusesEveryDeviation(t *testing.T) {
	memory, quota := int64(512<<20), int64(150000)
	posture := nucleusPosture{CgroupRoot: "/sys/fs/cgroup/mitos", Workspace: "/attempt/workspace", MemoryBytes: &memory, CPUQuota: &quota, PidsMax: nucleusPidsMax}
	good := startedEvent("mitos-x", posture.CgroupRoot, posture.Workspace)
	if err := posture.verify(good, "mitos-x"); err != nil {
		t.Fatal(err)
	}
	// A quota expressed against a different period is the same rate.
	scaled := startedEvent("mitos-x", posture.CgroupRoot, posture.Workspace)
	q, p := int64(75000), int64(50000)
	scaled.ResourceLimits.CPUQuotaUS, scaled.ResourceLimits.CPUPeriodUS = &q, &p
	if err := posture.verify(scaled, "mitos-x"); err != nil {
		t.Fatalf("equivalent quota refused: %v", err)
	}
	mutations := map[string]func(*nucleusEvent){
		"name":       func(e *nucleusEvent) { e.Container.Name = "other" },
		"runtime":    func(e *nucleusEvent) { e.Container.Runtime = "gvisor" },
		"network":    func(e *nucleusEvent) { e.Container.NetworkMode = "host" },
		"cgroup":     func(e *nucleusEvent) { e.Container.CgroupPath = "/sys/fs/cgroup/other/nucleus-abc" },
		"no cgroup":  func(e *nucleusEvent) { e.Container.CgroupPath = "" },
		"workspace":  func(e *nucleusEvent) { e.Container.WorkspaceMount.Source = "/elsewhere" },
		"read-only":  func(e *nucleusEvent) { e.Container.WorkspaceMount.Mode = "bind-ro" },
		"seccomp":    func(e *nucleusEvent) { e.Security.SeccompMode = "trace" },
		"landlock":   func(e *nucleusEvent) { e.Security.LandlockStatus = "partially_enforced" },
		"caps":       func(e *nucleusEvent) { e.Security.CapabilitiesStatus = "policy" },
		"degraded":   func(e *nucleusEvent) { e.Security.AllowDegradedSecurity = true },
		"memory":     func(e *nucleusEvent) { e.ResourceLimits.MemoryBytes = nil },
		"cpu":        func(e *nucleusEvent) { v := int64(200000); e.ResourceLimits.CPUQuotaUS = &v },
		"pids":       func(e *nucleusEvent) { e.ResourceLimits.PidsMax = nil },
		"event type": func(e *nucleusEvent) { e.Type = "container_summary" },
	}
	for name, mutate := range mutations {
		event := startedEvent("mitos-x", posture.CgroupRoot, posture.Workspace)
		mutate(&event)
		if err := posture.verify(event, "mitos-x"); err == nil {
			t.Fatalf("%s deviation accepted", name)
		}
	}
	// An unrequested limit is a deviation too: the posture is exact.
	unlimited := nucleusPosture{CgroupRoot: posture.CgroupRoot, Workspace: posture.Workspace, PidsMax: nucleusPidsMax}
	if err := unlimited.verify(good, "mitos-x"); err == nil {
		t.Fatal("unrequested limits accepted")
	}
}

// fakeNucleus writes a stand-in runtime that speaks the descriptor protocol:
// it saves the launch document and environment, emits the started event,
// holds on the start gate like Nucleus, and runs the given shell body only
// after the gate released it. The body may set summary and status.
func fakeNucleus(t *testing.T, dir string, started *nucleusEvent, body string) string {
	t.Helper()
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat")
	}
	startedLine := ""
	gate := ""
	if started != nil {
		refused := `{"type":"container_summary","exit_status":{"code":1,"error":"Start gate closed"}}`
		gate = "if [ \"$(head -c 1 <&6 | wc -c)\" -ne 1 ]; then printf '%s\\n' '" + refused + "' >&4; exit 1; fi\n"
		encoded, _ := json.Marshal(started)
		if err = os.WriteFile(filepath.Join(dir, "started.json"), append(encoded, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		startedLine = cat + " " + filepath.Join(dir, "started.json") + " >&4\n"
	}
	script := "#!/bin/sh\nPATH=" + os.Getenv("PATH") + "\nsummary='{\"type\":\"container_summary\",\"exit_status\":{\"code\":0}}'\nstatus=0\n" +
		cat + " <&3 > " + filepath.Join(dir, "config.json") + "\n" + cat + " <&5 > " + filepath.Join(dir, "env.json") + "\n" + startedLine +
		gate +
		strings.ReplaceAll(body, "CAT", cat) + "\nprintf '%s\\n' \"$summary\" >&4\nexit $status\n"
	path := filepath.Join(dir, "nucleus")
	if err = os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeProvider(t *testing.T, binary, root string) *Local {
	caps := Capabilities{CPUQuota: true, MemoryLimit: true, Isolation: "container", Egress: NetOff, DataMount: "bind-ro", NetModes: []string{NetOff}, WorkspaceConfined: true}
	return &Local{name: ConfinementNucleus, sandbox: &nucleusRuntime{cfg: NucleusConfig{Binary: binary, CgroupRoot: root}, caps: caps}}
}

func admitNucleus(t *testing.T, l *Local, base string) Handle {
	t.Helper()
	spec := nucleusSpec(t, base)
	admitted, err := Compile(Request{Resources: spec.Posture.Resources, TrustTier: "T1", NetMode: NetOff, WorkspaceMode: "copy"}, map[string]Capabilities{"local": (&Local{}).Capabilities(), ConfinementNucleus: l.Capabilities()}, false)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Provider != ConfinementNucleus {
		t.Fatalf("confined posture admitted by %q", admitted.Provider)
	}
	admitted.RunID, admitted.CapabilityRef, admitted.Base, admitted.Workspace = "run-1", "app", base, spec.Workspace
	handle, err := l.Prepare(context.Background(), admitted)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestNucleusSessionStartsOnlyAfterAttestedPosture(t *testing.T) {
	hostShell(t)
	dir, base, root := t.TempDir(), t.TempDir(), t.TempDir()
	event := startedEvent(nucleusName(base), root, filepath.Join(base, "workspace"))
	l := fakeProvider(t, fakeNucleus(t, dir, &event, "CAT"), root)
	handle := admitNucleus(t, l, base)
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{"/bin/sh", "-c", "true"}, Input: []byte("workload-output")})
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Result()
	if err != nil || string(output) != "workload-output" {
		t.Fatalf("result %q %v", output, err)
	}
	var doc nucleusDocument
	raw, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if err = json.Unmarshal(raw, &doc); err != nil || doc.Memory != "512M" || doc.Name != nucleusName(base) {
		t.Fatalf("launch document not delivered on fd 3: %s %v", raw, err)
	}
	var env []string
	raw, _ = os.ReadFile(filepath.Join(dir, "env.json"))
	if err = json.Unmarshal(raw, &env); err != nil || !strings.Contains(strings.Join(env, "\n"), "HOME=/mitos/home") {
		t.Fatalf("environment not delivered on fd 5: %s %v", raw, err)
	}
	record, err := os.ReadFile(filepath.Join(base, nucleusEventsFile))
	if err != nil || strings.Count(string(record), "\n") != 2 || !strings.Contains(string(record), "container_summary") {
		t.Fatalf("attested events not recorded with the attempt: %q %v", record, err)
	}
}

func TestNucleusRefusesDegradedOrSilentStarts(t *testing.T) {
	hostShell(t)
	cases := map[string]func(base, root string) (*nucleusEvent, string){
		"degraded": func(base, root string) (*nucleusEvent, string) {
			e := startedEvent(nucleusName(base), root, filepath.Join(base, "workspace"))
			e.Security.LandlockStatus = "not_enforced"
			// The held workload must never be released.
			return &e, "touch " + filepath.Join(base, "released") + "; exec sleep 60"
		},
		"silent": func(base, root string) (*nucleusEvent, string) { return nil, "exit 1" },
	}
	for name, setup := range cases {
		dir, base, root := t.TempDir(), t.TempDir(), t.TempDir()
		event, body := setup(base, root)
		l := fakeProvider(t, fakeNucleus(t, dir, event, body), root)
		handle := admitNucleus(t, l, base)
		started := time.Now()
		session, err := l.Start(context.Background(), handle, Command{Argv: []string{"/bin/sh", "-c", "true"}})
		if err == nil || session != nil {
			t.Fatalf("%s start accepted", name)
		}
		if time.Since(started) > 10*time.Second {
			t.Fatalf("%s refusal waited for the workload", name)
		}
		if name == "degraded" && !strings.Contains(err.Error(), "landlock") {
			t.Fatalf("degraded refusal does not name the deviation: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(base, "released")); statErr == nil {
			t.Fatalf("%s start released the workload", name)
		}
	}
}

func TestNucleusMissingSummaryMakesOutcomeUntrusted(t *testing.T) {
	hostShell(t)
	dir, base, root := t.TempDir(), t.TempDir(), t.TempDir()
	event := startedEvent(nucleusName(base), root, filepath.Join(base, "workspace"))
	// exec replaces the shell before the summary line, as a crashed runtime would.
	l := fakeProvider(t, fakeNucleus(t, dir, &event, "exec true"), root)
	handle := admitNucleus(t, l, base)
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{"/bin/sh", "-c", "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Result(); err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("missing summary accepted: %v", err)
	}
}

func TestGuestPathNamesAttemptPathsInTheWorkloadNamespace(t *testing.T) {
	spec := LaunchSpec{Provider: ConfinementNucleus, Base: "/srv/attempt", Workspace: Workspace{Directory: "/srv/attempt/workspace"}}
	for host, want := range map[string]string{"/srv/attempt/workspace": "/workspace", "/srv/attempt/workspace/src": "/workspace/src", "/srv/attempt/inputs/task-1": "/mitos/inputs/task-1"} {
		if got, err := spec.GuestPath(host); err != nil || got != want {
			t.Fatalf("%s -> %q %v", host, got, err)
		}
	}
	if _, err := spec.GuestPath("/srv/attempt/history.git"); err == nil {
		t.Fatal("invisible attempt path translated")
	}
	spec.Provider = "local"
	if got, err := spec.GuestPath("/srv/attempt/history.git"); err != nil || got != "/srv/attempt/history.git" {
		t.Fatalf("local launch path changed: %q %v", got, err)
	}
}

func TestNucleusReportsMemoryExhaustion(t *testing.T) {
	hostShell(t)
	dir, base, root := t.TempDir(), t.TempDir(), t.TempDir()
	event := startedEvent(nucleusName(base), root, filepath.Join(base, "workspace"))
	body := `summary='{"type":"container_summary","exit_status":{"code":137},"resource_stats":{"memory_events":{"max":3,"oom":1,"oom_kill":1}}}'; status=137`
	l := fakeProvider(t, fakeNucleus(t, dir, &event, body), root)
	session, err := l.Start(context.Background(), admitNucleus(t, l, base), Command{Argv: []string{"/bin/sh", "-c", "true"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = session.Result(); !errors.Is(err, ErrMemoryExhausted) {
		t.Fatalf("oom kill not reported as memory exhaustion: %v", err)
	}
}

func TestDevelopmentLaunchesNeverSelectNucleus(t *testing.T) {
	caps := fakeProvider(t, "/usr/bin/nucleus", "/sys/fs/cgroup/mitos").Capabilities()
	spec, err := Compile(Request{}, map[string]Capabilities{"local": (&Local{}).Capabilities(), ConfinementNucleus: caps}, true)
	if err != nil || spec.Provider != "local" {
		t.Fatalf("development launch admitted by %q: %v", spec.Provider, err)
	}
	// A posture Nucleus cannot enforce is refused, not handed to the process.
	if _, err = Compile(Request{TrustTier: "T1", NetMode: "deny-all-except-broker", BrokerEndpoint: "https://broker.example"}, map[string]Capabilities{"local": (&Local{}).Capabilities(), ConfinementNucleus: caps}, false); err == nil {
		t.Fatal("brokered posture admitted without a broker-capable provider")
	}
}
