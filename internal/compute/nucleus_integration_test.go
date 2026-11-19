package compute

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// These tests run the real runtime. They need a Nucleus binary and a cgroup
// subtree delegated to the test process, which must be its only member:
//
//	just test-nucleus
func realNucleus(t *testing.T) *Local {
	t.Helper()
	binary := os.Getenv("MITOS_TEST_NUCLEUS")
	if binary == "" || os.Getenv("NUCLEUS_CGROUP_ROOT") == "" {
		t.Skip("set MITOS_TEST_NUCLEUS and a delegated NUCLEUS_CGROUP_ROOT (just test-nucleus)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	l, err := NewNucleus(ctx, NucleusConfig{Binary: binary, CgroupRoot: os.Getenv("NUCLEUS_CGROUP_ROOT"), ProbeDir: filepath.Join(t.TempDir(), "probe")})
	if err != nil {
		t.Fatal(err)
	}
	caps := l.Capabilities()
	if !caps.CPUQuota || !caps.MemoryLimit || !caps.WorkspaceConfined || caps.Isolation != "container" {
		t.Fatalf("probe did not establish enforced capabilities: %+v", caps)
	}
	return l
}

func preparedNucleus(t *testing.T, l *Local, source string) Handle {
	t.Helper()
	return preparedNucleusWith(t, l, source, nil)
}

func preparedNucleusWith(t *testing.T, l *Local, source string, adjust func(*LaunchSpec)) Handle {
	t.Helper()
	base := filepath.Join(t.TempDir(), "attempt")
	spec := nucleusSpec(t, base)
	if adjust != nil {
		adjust(&spec)
	}
	admitted, err := Compile(Request{Resources: spec.Posture.Resources, TrustTier: "T1", NetMode: NetOff, WorkspaceMode: "copy"}, map[string]Capabilities{"local": (&Local{}).Capabilities(), ConfinementNucleus: l.Capabilities()}, false)
	if err != nil {
		t.Fatal(err)
	}
	admitted.RunID, admitted.CapabilityRef, admitted.Base = "run-1", "app", base
	admitted.Workspace = Workspace{Mode: "copy", Source: source, Directory: filepath.Join(base, "workspace")}
	handle, err := l.Prepare(context.Background(), admitted)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestNucleusRunsConfinedAndHarvests(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handle := preparedNucleus(t, l, source)
	base := filepath.Dir(handle.Workspace())
	script := `set -e
read line
echo "stdin=$line"
echo after > /workspace/input.txt
echo note > "$HOME/note"
test ! -e ` + base + `
test ! -e /mitos/history.git
echo "home=$HOME tmp=$TMPDIR"`
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{shell, "-c", script}, Input: []byte("hello\n")})
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Result()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if !strings.Contains(string(output), "stdin=hello") || !strings.Contains(string(output), "home=/mitos/home tmp=/mitos/tmp") {
		t.Fatalf("output %q", output)
	}
	if note, _ := os.ReadFile(filepath.Join(base, "home", "note")); string(note) != "note\n" {
		t.Fatalf("per-attempt home not persisted: %q", note)
	}
	harvest, err := l.Harvest(context.Background(), session)
	if err != nil || harvest.Change == nil {
		t.Fatalf("harvest %v %+v", err, harvest)
	}
	var changed []string
	for _, change := range harvest.Change.Changes {
		changed = append(changed, change.Path)
	}
	if strings.Join(changed, ",") != "input.txt" {
		t.Fatalf("harvested changes %q", changed)
	}
	record, err := os.ReadFile(filepath.Join(base, nucleusEventsFile))
	if err != nil || !strings.Contains(string(record), `"landlock_status":"fully_enforced"`) {
		t.Fatalf("attestation not recorded: %v %s", err, record)
	}
}

func TestNucleusStopRevokesThroughTheCgroup(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	handle := preparedNucleus(t, l, "")
	// The workload escapes its process group; only the cgroup still reaches it.
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{shell, "-c", "setsid sleep 600 & exec sleep 600"}})
	if err != nil {
		t.Fatal(err)
	}
	cgroup := session.(*localSession).sandbox.cgroup
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = l.Stop(ctx, session); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, statErr := os.Stat(cgroup); errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			procs, _ := os.ReadFile(filepath.Join(cgroup, "cgroup.procs"))
			t.Fatalf("container cgroup survived stop: %s", procs)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestNucleusReclaimsContainersOfAPreviousServer(t *testing.T) {
	realNucleus(t)
	root := os.Getenv("NUCLEUS_CGROUP_ROOT")
	orphan := filepath.Join(root, "nucleus-orphan-test")
	if err := os.Mkdir(orphan, 0o755); err != nil {
		t.Fatal(err)
	}
	sleeper := exec.Command("sleep", "600")
	sleeper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	if err := os.WriteFile(filepath.Join(orphan, "cgroup.procs"), []byte(strconv.Itoa(sleeper.Process.Pid)), 0); err != nil {
		t.Fatal(err)
	}
	n := &nucleusRuntime{cfg: NucleusConfig{CgroupRoot: root}}
	count, err := n.reclaim()
	if err != nil || count != 1 {
		t.Fatalf("reclaimed %d: %v", count, err)
	}
	done := make(chan error, 1)
	go func() { done <- sleeper.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("orphaned workload survived reclaim")
	}
	if _, err = os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan cgroup not removed: %v", err)
	}
}

func TestNucleusNeverReleasesAnUnattestedWorkload(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	h := preparedNucleus(t, l, "").(*localHandle)
	workspace := h.spec.Workspace.Directory
	run, err := l.sandbox.launch(h.spec, []string{shell, "-c", "touch /workspace/released"}, workspace, []string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatal(err)
	}
	defer run.close()
	// The admitted posture differs from what Nucleus will enforce.
	run.posture.PidsMax++
	cmd := exec.Command(run.argv[0], run.argv[1:]...)
	cmd.Dir, cmd.Env, cmd.ExtraFiles = run.dir, run.env, run.files
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	run.begin()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = run.await(ctx, nil); err == nil || !strings.Contains(err.Error(), "pids limit") {
		t.Fatalf("mismatched posture released: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("refused start exited successfully")
		}
	case <-time.After(20 * time.Second):
		_, _ = run.kill()
		t.Fatal("closed start gate did not tear the container down")
	}
	if err = run.finish(); err != nil {
		t.Fatal(err)
	}
	if run.summary.ExitStatus == nil || !strings.Contains(run.summary.ExitStatus.Error, "Start gate") {
		t.Fatalf("summary does not report the refused start: %+v", run.summary.ExitStatus)
	}
	if _, err = os.Stat(filepath.Join(workspace, "released")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workload ran before attestation: %v", err)
	}
}

func TestNucleusKillsAWorkloadAtItsMemoryLimit(t *testing.T) {
	l := realNucleus(t)
	shell := hostShell(t)
	handle := preparedNucleusWith(t, l, "", func(spec *LaunchSpec) {
		spec.Posture.Resources.MemoryMiB.Amount = 64
	})
	// tail buffers a line with no newline, so it grows past the limit.
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{shell, "-c", "head -c 512m /dev/zero | tail -n 1 >/dev/null"}})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := session.Result()
		result <- err
	}()
	select {
	case err = <-result:
		if !errors.Is(err, ErrMemoryExhausted) {
			t.Fatalf("over-limit workload not reported as memory exhaustion: %v", err)
		}
	case <-time.After(30 * time.Second):
		_ = l.Stop(context.Background(), session)
		t.Fatal("over-limit workload was throttled instead of killed")
	}
}

// A brokered agent posture: the workload reaches its model API only through
// the per-attempt broker, holds no credential, cannot tunnel or route
// anywhere else, and sees the workspace read-only when its role cannot write.
func TestNucleusBrokersModelAccessWithoutCredentialsInside(t *testing.T) {
	l := realNucleus(t)
	if !slices.Contains(l.Capabilities().NetModes, NetBroker) {
		t.Fatalf("probe did not establish brokered egress: %+v", l.Capabilities())
	}
	shell := hostShell(t)
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("no curl")
	}
	if curl, err = filepath.EvalSymlinks(curl); err != nil || !visibleHostPath(curl) {
		t.Skip("curl is not visible in the sandbox")
	}
	upstream, seen := recordingUpstream(t)
	socket := filepath.Join(t.TempDir(), "b.sock")
	broker, err := startModelBroker(socket, map[string]string{BrokerVendorAnthropic: "sk-ant-real"}, map[string]string{BrokerVendorAnthropic: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	base := filepath.Join(t.TempDir(), "attempt")
	spec := nucleusSpec(t, base)
	admitted, err := Compile(Request{Resources: spec.Posture.Resources, TrustTier: "T1", NetMode: NetBroker, BrokerEndpoint: broker.Endpoint(), WorkspaceMode: "copy"}, map[string]Capabilities{"local": (&Local{}).Capabilities(), ConfinementNucleus: l.Capabilities()}, false)
	if err != nil || admitted.Provider != ConfinementNucleus {
		t.Fatalf("brokered posture admitted by %q: %v", admitted.Provider, err)
	}
	admitted.Posture.ReadOnlyWorkspace = true
	admitted.RunID, admitted.CapabilityRef, admitted.Base = "run-1", "agent", base
	admitted.Workspace = Workspace{Mode: "copy", Source: t.TempDir(), Directory: filepath.Join(base, "workspace")}
	handle, err := l.Prepare(context.Background(), admitted)
	if err != nil {
		t.Fatal(err)
	}
	script := `set -u
fail() { echo "FAIL $*"; exit 1; }
env | grep -q sk-ant-real && fail credential in environment
env | grep -qi '_proxy=' && fail proxy variables injected
test -S /run/nucleus/broker.sock && fail broker socket mounted
touch /workspace/written 2>/dev/null && fail workspace writable
answer=$(` + curl + ` -sS --max-time 10 -H 'x-api-key: ` + BrokerPlaceholderKey + `' -d '{}' ` + BrokerBaseURL(BrokerVendorAnthropic) + `/v1/messages) || fail broker unreachable
test "$answer" = ok || fail "broker answered $answer"
` + curl + ` -sS --max-time 5 -o /dev/null -x ` + "http://" + EgressLoopAddr + ` https://api.anthropic.com/ 2>/dev/null && fail tunnel admitted
` + curl + ` -sS --max-time 5 -o /dev/null http://1.1.1.1/ 2>/dev/null && fail direct route
echo brokered`
	session, err := l.Start(context.Background(), handle, Command{Argv: []string{shell, "-c", script}, NativeEnv: []string{"PATH=/usr/bin:/bin", "HOME=" + filepath.Join(base, "home")}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Result()
	if err != nil || !strings.Contains(string(output), "brokered") {
		t.Fatalf("%v: %s", err, output)
	}
	got := seen()
	if len(got) != 1 || got[0].uri != "/v1/messages" || got[0].header.Get("X-Api-Key") != "sk-ant-real" {
		t.Fatalf("upstream saw %+v", got)
	}
}

// Real Codex, its own sandbox off, inside a brokered Nucleus container: it
// runs a command in the workspace and answers through the broker, which is
// the only party holding the key. The vendor is a scripted Responses server.
//
//	MITOS_TEST_CODEX=/nix/store/...-codex/bin/codex just test-nucleus
func TestNucleusRunsCodexThroughTheBroker(t *testing.T) {
	l := realNucleus(t)
	codex := os.Getenv("MITOS_TEST_CODEX")
	if codex == "" {
		t.Skip("set MITOS_TEST_CODEX to a Codex binary in the Nix store")
	}
	var mu sync.Mutex
	var auth []string
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		calls++
		n := calls
		mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		event := func(kind string, payload map[string]any) {
			payload["type"] = kind
			raw, _ := json.Marshal(payload)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw)
		}
		id := fmt.Sprintf("resp_%d", n)
		event("response.created", map[string]any{"response": map[string]any{"id": id, "status": "in_progress"}})
		item := map[string]any{"type": "message", "id": "msg_" + id, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "pong", "annotations": []any{}}}}
		if !strings.Contains(string(body), "function_call_output") {
			var request struct {
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			}
			_ = json.Unmarshal(body, &request)
			command := map[string]any{}
			for _, tool := range request.Tools {
				switch tool.Name {
				case "exec_command":
					command["cmd"], item = "touch /workspace/made-by-codex && echo ran", map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": tool.Name}
				case "shell_command":
					command["command"], item = "touch /workspace/made-by-codex && echo ran", map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": tool.Name}
				}
			}
			raw, _ := json.Marshal(command)
			item["arguments"] = string(raw)
		}
		event("response.output_item.added", map[string]any{"output_index": 0, "item": item})
		event("response.output_item.done", map[string]any{"output_index": 0, "item": item})
		event("response.completed", map[string]any{"response": map[string]any{"id": id, "status": "completed", "output": []any{item}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 10, "total_tokens": 20}}})
	}))
	defer upstream.Close()
	broker, err := startModelBroker(filepath.Join(t.TempDir(), "b.sock"), map[string]string{BrokerVendorOpenAI: "sk-oai-real"}, map[string]string{BrokerVendorOpenAI: upstream.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	base := filepath.Join(t.TempDir(), "attempt")
	spec := nucleusSpec(t, base)
	admitted, err := Compile(Request{Resources: spec.Posture.Resources, TrustTier: "T1", NetMode: NetBroker, BrokerEndpoint: broker.Endpoint(), Provider: ConfinementNucleus}, map[string]Capabilities{ConfinementNucleus: l.Capabilities()}, false)
	if err != nil {
		t.Fatal(err)
	}
	admitted.Posture.SeccompAllow = HarnessSyscalls
	admitted.RunID, admitted.CapabilityRef, admitted.Base = "run-1", "agent", base
	admitted.Workspace = Workspace{Mode: "copy", Source: t.TempDir(), Directory: filepath.Join(base, "workspace")}
	handle, err := l.Prepare(context.Background(), admitted)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	if err = os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"auth_mode":"apikey","OPENAI_API_KEY":"`+BrokerPlaceholderKey+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	argv := []string{codex, "exec", "-c", `openai_base_url="` + BrokerBaseURL(BrokerVendorOpenAI) + `/v1"`, "-c", `model="gpt-5.4"`, "--skip-git-repo-check", "--sandbox", "danger-full-access", "Run the command, then answer."}
	// The server's PATH, as an agent inherits it; profile directories resolve
	// to the store paths Nucleus binds.
	session, err := l.Start(ctx, handle, Command{Argv: argv, NativeEnv: []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex"), "PATH=" + os.Getenv("PATH")}})
	if err != nil {
		t.Fatal(err)
	}
	output, err := session.Result()
	if err != nil || !strings.Contains(string(output), "pong") {
		t.Fatalf("%v: %s", err, output)
	}
	if _, err = os.Stat(filepath.Join(base, "workspace", "made-by-codex")); err != nil {
		t.Fatalf("codex command did not run in the workspace: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, header := range auth {
		if header != "Bearer sk-oai-real" {
			t.Fatalf("upstream saw credential %q", header)
		}
	}
}
