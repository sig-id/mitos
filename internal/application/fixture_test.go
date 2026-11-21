package application

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fixtureProcess struct {
	cmd  *exec.Cmd
	log  *os.File
	done chan error
}

func (p *fixtureProcess) stop() {
	if p == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	if p.done != nil {
		<-p.done
	} else {
		_ = p.cmd.Wait()
	}
	_ = p.log.Close()
}

// fixtureDir is a short per-test directory. t.TempDir embeds the test name,
// which can push the runtime's Unix socket paths past the kernel's sun_path
// limit (108 bytes on Linux), so the authority fails to start.
func fixtureDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "mitos-fx-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
func fixtureAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}
func fixtureEnv(extra map[string]string) []string {
	env := []string{}
	for _, v := range os.Environ() {
		key, _, _ := strings.Cut(v, "=")
		if _, ok := extra[key]; !ok {
			env = append(env, v)
		}
	}
	for key, v := range extra {
		env = append(env, key+"="+v)
	}
	return env
}
func fixtureCall(t *testing.T, client *http.Client, method, url, token string, body any) ([]byte, int) {
	t.Helper()
	var input io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		input = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, url, input)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Mitos-API-Version", "1")
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		t.Fatal(err)
	}
	return raw, resp.StatusCode
}

// Both backends run as independent subprocesses. Each fault kills the app at a
// real durability boundary and then kills the runtime without clean shutdown.
func TestIndependentApplicationCrashMatrix(t *testing.T) {
	runIndependentApplicationCrashMatrix(t, false, []string{"before_admission", "after_admission", "before_publication", "after_publication"})
}
func TestIndependentWorkerFaultMatrix(t *testing.T) {
	runIndependentApplicationCrashMatrix(t, true, []string{"before_admission", "after_admission", "before_publication", "after_publication", "worker:crash", "worker:lease_expiry", "worker:duplicate_result", "worker:lost_receipt", "worker:server_restart", "worker:missed_mqtt", "worker:external_lost_response"})
}
func TestIndependentWorkerDurableWait(t *testing.T) {
	runIndependentApplicationCrashMatrix(t, true, []string{"worker:wait_wake", "worker:wait_restart"})
}
func TestIndependentWorkerApplicationSmoke(t *testing.T) {
	runIndependentApplicationCrashMatrix(t, true, []string{"after_publication"})
}
func runIndependentApplicationCrashMatrix(t *testing.T, remoteWorker bool, points []string) {
	if testing.Short() {
		t.Skip("subprocess fixture")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap required for tenant isolation fixture")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	serverBinary := filepath.Join(build, "mitos")
	appBinary := filepath.Join(build, "appfixture")
	for _, item := range []struct{ dir, out, target string }{{root, serverBinary, "./cmd/mitos"}, {filepath.Join(root, "examples", "appfixture"), appBinary, "."}} {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", item.out, item.target)
		cmd.Dir = item.dir
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build fixture: %v\n%s", err, raw)
		}
	}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			dir := fixtureDir(t)
			data := filepath.Join(dir, "runtime")
			workspace := filepath.Join(dir, "workspace")
			bin := filepath.Join(dir, "bin")
			for _, path := range []string{workspace, bin} {
				if err = os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			cert, key := fixtureCertificate(t, dir)
			operator := fixtureAddress(t)
			remote := fixtureAddress(t)
			// The stand-in proves confinement from inside the real worker path:
			// it reports EXPOSED if the runtime database is readable or the
			// operator listener is reachable from the sandbox.
			modelFlags := "-X main.forbiddenPath=" + filepath.Join(data, "runtime.db") + " -X main.forbiddenDial=" + operator
			if strings.HasPrefix(point, "worker:wait_") {
				modelFlags += " -X main.waitForInput=1"
			}
			if point == "worker:external_lost_response" {
				modelFlags += " -X main.actionTarget=fixture-action"
			}
			modelBuild := exec.Command("go", "build", "-buildvcs=false", "-ldflags", modelFlags, "-o", filepath.Join(bin, "codex"), "./testdata/codex.go")
			modelBuild.Dir = filepath.Join(root, "examples", "appfixture")
			if raw, err := modelBuild.CombinedOutput(); err != nil {
				t.Fatalf("build confined model stand-in: %v\n%s", err, raw)
			}
			roots := x509.NewCertPool()
			pemBytes, _ := os.ReadFile(cert)
			roots.AppendCertsFromPEM(pemBytes)
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 2 * time.Second}
			wakeConfig := ""
			var wakePublishes atomic.Int64
			if point == "worker:missed_mqtt" {
				broker := mqtt.New(&mqtt.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
				if err = broker.AddHook(new(auth.AllowHook), nil); err != nil {
					t.Fatal(err)
				}
				if err = broker.AddHook(&fixtureWakeHook{count: &wakePublishes}, nil); err != nil {
					t.Fatal(err)
				}
				listener := listeners.NewTCP(listeners.Config{ID: "wake", Address: "127.0.0.1:0"})
				if err = broker.AddListener(listener); err != nil {
					t.Fatal(err)
				}
				if err = broker.Serve(); err != nil {
					t.Fatal(err)
				}
				defer broker.Close()
				wakeConfig = filepath.Join(dir, "wake.json")
				raw, _ := json.Marshal(map[string]string{"broker": "mqtt://" + listener.Address()})
				if err = os.WriteFile(wakeConfig, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}

			serverFailpoint := ""
			if point == "worker:lost_receipt" {
				serverFailpoint = "worker.result.before_reply"
			}
			start := func() *fixtureProcess {
				log, err := os.CreateTemp(dir, "server-*.log")
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(serverBinary, "serve", "--data", data, "--workspace-root", workspace, "--addr", operator, "--application-addr", remote, "--application-cert", cert, "--application-key", key)
				if wakeConfig != "" {
					cmd.Args = append(cmd.Args, "--worker-wake-config", wakeConfig)
				}
				cmd.Env = fixtureEnv(map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"), "MITOS_AGENT_CONFINEMENT": "bwrap", "MITOS_AGENT_NETWORK": "unrestricted", "OPENAI_API_KEY": "fixture-model-key", "MITOS_ENABLE_FAILPOINTS": "1", "MITOS_FAILPOINT": serverFailpoint, "MITOS_LOCAL_STRUCTURED_WORKERS": map[bool]string{true: "off", false: "on"}[remoteWorker]})
				cmd.Stdout = log
				cmd.Stderr = log
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				p := &fixtureProcess{cmd: cmd, log: log}
				deadline := time.Now().Add(15 * time.Second)
				for time.Now().Before(deadline) {
					req, _ := http.NewRequest("GET", "http://"+operator+"/api/capabilities", nil)
					req.Header.Set("Mitos-API-Version", "1")
					if response, err := client.Do(req); err == nil {
						response.Body.Close()
						if response.StatusCode == 200 {
							return p
						}
					}
					time.Sleep(50 * time.Millisecond)
				}
				p.stop()
				raw, _ := os.ReadFile(log.Name())
				t.Fatalf("runtime startup failed:\n%s", raw)
				return nil
			}
			runtime := start()
			defer func() { runtime.stop() }()
			raw, status := fixtureCall(t, client, "POST", "http://"+operator+"/api/service-principals", "", map[string]any{"id": "fixture", "name": "Independent fixture", "tenant": map[string]string{"org": "fixture", "project": "app"}, "read": true, "write": true, "expires_at": time.Now().Add(time.Hour)})
			var principal struct {
				Token string `json:"token"`
			}
			if status != 201 || json.Unmarshal(raw, &principal) != nil || principal.Token == "" {
				t.Fatal("principal", status, string(raw))
			}
			var startWorker func(string) *fixtureProcess
			var currentWorker *fixtureProcess
			var workerProcesses []*fixtureProcess
			defer func() {
				for _, worker := range workerProcesses {
					worker.stop()
				}
			}()
			if remoteWorker {
				raw, status := fixtureCall(t, client, "POST", "http://"+operator+"/api/workers", "", map[string]any{"id": "separate-worker", "pools": []string{"interactive"}, "agents": []string{"mitos.generalist"}, "expires_at": time.Now().Add(time.Hour)})
				var credential struct {
					Token string `json:"token"`
				}
				if status != 201 || json.Unmarshal(raw, &credential) != nil || credential.Token == "" {
					t.Fatal("worker credential", status, string(raw))
				}
				startWorker = func(failpoint string) *fixtureProcess {
					workerLog, err := os.CreateTemp(dir, "worker-*.log")
					if err != nil {
						t.Fatal(err)
					}
					cmd := exec.Command(serverBinary, "worker", "--url", "https://"+remote, "--id", "separate-worker", "--data", filepath.Join(dir, "worker"), "--ca-file", cert, "--codex", filepath.Join(bin, "codex"))
					cmd.Env = fixtureEnv(map[string]string{"MITOS_WORKER_TOKEN": credential.Token, "OPENAI_API_KEY": "fixture-worker-model-key", "MITOS_ENABLE_FAILPOINTS": "1", "MITOS_FAILPOINT": failpoint})
					cmd.Stdout = workerLog
					cmd.Stderr = workerLog
					if err = cmd.Start(); err != nil {
						t.Fatal(err)
					}
					worker := &fixtureProcess{cmd: cmd, log: workerLog, done: make(chan error, 1)}
					go func() { worker.done <- cmd.Wait(); close(worker.done) }()
					workerProcesses = append(workerProcesses, worker)
					return worker
				}
				fault := ""
				if point == "worker:crash" || point == "worker:lease_expiry" || point == "worker:server_restart" {
					fault = "worker.after_claim"
				}
				if point == "worker:duplicate_result" {
					fault = "worker.duplicate_result"
				}
				currentWorker = startWorker(fault)
			}
			actionTarget := ""
			var externalPosts atomic.Int64
			if point == "worker:external_lost_response" {
				actionTarget = "fixture-action"
				var mu sync.Mutex
				key := ""
				target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.Header.Get("Authorization") != "Bearer private-fixture-target" {
						w.WriteHeader(401)
						return
					}
					if r.Method == "POST" {
						externalPosts.Add(1)
						key = r.Header.Get("Idempotency-Key")
						connection, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						connection.Close()
						return
					}
					if r.URL.Path != "/operations/"+key {
						w.WriteHeader(404)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"idempotency_key": key, "state": "applied", "result": map[string]string{"reply": "ack"}})
				}))
				defer target.Close()
				schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["reply"],"properties":{"reply":{"type":"string","maxLength":8192}}}`)
				raw, status := fixtureCall(t, client, "POST", "http://"+operator+"/api/action-targets", "", map[string]any{"id": actionTarget, "tenant": map[string]string{"org": "fixture", "project": "app"}, "agents": []string{"mitos.generalist"}, "endpoint": target.URL, "bearer": "private-fixture-target", "input_schema": schema, "result_schema": schema})
				if status != 200 {
					t.Fatal("configure external target", status, string(raw))
				}
			}

			applicationDB := filepath.Join(dir, "application.db")
			invoke := func(failpoint string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, appBinary, "--database", applicationDB, "--mitos", "https://"+remote, "--ca-file", cert, "--message-id", "once", "--text", "Hello", "--timeout", "40s")
				if actionTarget != "" {
					cmd.Args = append(cmd.Args, "--action-target", actionTarget)
				}
				if strings.HasPrefix(point, "worker:wait_") {
					cmd.Args = append(cmd.Args, "--wait-for-input")
				}
				cmd.Env = fixtureEnv(map[string]string{"MITOS_APPLICATION_TOKEN": principal.Token, "MITOS_ENABLE_FAILPOINTS": "1", "MITOS_APPFIXTURE_FAILPOINT": failpoint})
				return cmd.CombinedOutput()
			}
			var output []byte
			if strings.HasPrefix(point, "worker:") {
				type appResult struct {
					raw []byte
					err error
				}
				done := make(chan appResult, 1)
				go func() { raw, err := invoke(""); done <- appResult{raw, err} }()
				if point == "worker:crash" || point == "worker:lease_expiry" || point == "worker:server_restart" {
					select {
					case err := <-currentWorker.done:
						exit, ok := err.(*exec.ExitError)
						if !ok || exit.ExitCode() != 86 {
							t.Fatal("worker did not crash after claim", err)
						}
					case <-time.After(20 * time.Second):
						t.Fatal("worker crash boundary not reached")
					}
					if point == "worker:lease_expiry" {
						deadline := time.Now().Add(40 * time.Second)
						expired := false
						for time.Now().Before(deadline) {
							raw, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs?limit=200", principal.Token, nil)
							var attempts []map[string]any
							if status == 200 && json.Unmarshal(raw, &attempts) == nil && len(attempts) >= 2 {
								expired = true
								break
							}
							time.Sleep(200 * time.Millisecond)
						}
						if !expired {
							t.Fatal("expired lease stranded responsibility")
						}
					}
					if point == "worker:server_restart" {
						runtime.stop()
						runtime = start()
					}
					currentWorker = startWorker("")
				}
				if strings.HasPrefix(point, "worker:wait_") {
					taskID := ""
					deadline := time.Now().Add(20 * time.Second)
					for time.Now().Before(deadline) {
						raw, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs?limit=200", principal.Token, nil)
						var attempts []struct {
							TaskID string `json:"task_id"`
							Status string `json:"status"`
						}
						if status == 200 && json.Unmarshal(raw, &attempts) == nil {
							for _, run := range attempts {
								if run.Status == "waiting" {
									taskID = run.TaskID
								}
							}
						}
						if taskID != "" {
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
					if taskID == "" {
						for _, worker := range workerProcesses {
							raw, _ := os.ReadFile(worker.log.Name())
							t.Log("worker:", string(raw))
						}
						t.Fatal("native wait did not release its attempt")
					}
					if point == "worker:wait_restart" {
						runtime.stop()
						runtime = start()
					}
					raw, status := fixtureCall(t, client, "POST", "https://"+remote+"/api/tasks/"+taskID+"/inputs", principal.Token, map[string]any{"key": "answer", "data": map[string]bool{"resume": true}})
					var receipt struct {
						State string `json:"state"`
					}
					if status != 201 || json.Unmarshal(raw, &receipt) != nil || receipt.State != "woke" {
						t.Fatal("native wait wake", status, string(raw))
					}
				}

				if point == "worker:lost_receipt" {
					ended := make(chan error, 1)
					go func() { ended <- runtime.cmd.Wait() }()
					select {
					case err := <-ended:
						exit, ok := err.(*exec.ExitError)
						if !ok || exit.ExitCode() != 86 {
							t.Fatal("authority did not crash after result commit", err)
						}
					case <-time.After(25 * time.Second):
						t.Fatal("lost receipt boundary not reached")
					}
					serverFailpoint = ""
					runtime.stop()
					runtime = start()
				}
				first := <-done
				output, err = first.raw, first.err
				if err != nil {
					output, err = invoke("")
				}
				if err != nil || !strings.Contains(string(output), `"reply":"ack"`) {
					fixtureRunDiagnostics(t, client, operator)
					for _, worker := range workerProcesses {
						raw, _ := os.ReadFile(worker.log.Name())
						t.Log("worker:", string(raw))
					}
					t.Fatalf("worker fault recovery: %v %s", err, output)
				}
				if point == "worker:duplicate_result" {
					select {
					case err := <-currentWorker.done:
						t.Fatal("duplicate replay failed", err)
					case <-time.After(150 * time.Millisecond):
					}
				}
				if point == "worker:external_lost_response" && externalPosts.Load() != 1 {
					t.Fatal("external action repeated", externalPosts.Load())
				}
			} else {

				output, err = invoke(point)
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 86 {
					fixtureRunDiagnostics(t, client, operator)
					raw, _ := os.ReadFile(runtime.log.Name())
					t.Fatalf("app did not reach %s: %v\n%s\nserver:\n%s", point, err, output, raw)
				}
				runtime.stop()
				runtime = start()
			}
			if output, err = invoke(""); err != nil || !strings.Contains(string(output), `"reply":"ack"`) {
				fixtureRunDiagnostics(t, client, operator)
				raw, _ := os.ReadFile(runtime.log.Name())
				t.Fatalf("recovery: %v\n%s\nserver:\n%s", err, output, raw)
			}
			if point == "worker:missed_mqtt" {
				deadline := time.Now().Add(3 * time.Second)
				for wakePublishes.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(20 * time.Millisecond)
				}
				if wakePublishes.Load() == 0 {
					t.Fatal("missed-wake case did not actually publish MQTT")
				}
			}

			if _, err = invoke(""); err != nil {
				t.Fatal("publication replay", err)
			}
			db, err := sql.Open("sqlite", applicationDB)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"messages", "dispatch_intents", "replies"} {
				var count int
				if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
					t.Fatal("duplicate domain publication", table, count, err)
				}
			}
			raw, status = fixtureCall(t, client, "GET", "https://"+remote+"/api/runs?limit=200", principal.Token, nil)
			if status != 200 {
				t.Fatal(status, string(raw))
			}
			var runs []struct {
				TaskID   string `json:"task_id"`
				Status   string `json:"status"`
				Verified bool   `json:"verified"`
			}
			if err = json.Unmarshal(raw, &runs); err != nil {
				t.Fatal(err)
			}
			tasks := map[string]bool{}
			accepted := 0
			for _, run := range runs {
				if run.Status == "done" && run.Verified {
					accepted++
				}
				tasks[run.TaskID] = true
			}
			if accepted != 1 {
				t.Fatal("accepted result applied more or less than once", accepted)
			}
			if (point == "worker:lost_receipt" || point == "worker:duplicate_result") && len(runs) != 1 {
				t.Fatal("committed result was rerun", len(runs))
			}
			if len(tasks) != 1 {
				t.Fatal("admission created multiple logical tasks", tasks)
			}
		})
	}
}
func fixtureCertificate(t *testing.T, dir string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	for path, data := range map[string][]byte{certPath: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPath: pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded})} {
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return certPath, keyPath
}

func fixtureRunDiagnostics(t *testing.T, client *http.Client, operator string) {
	t.Helper()
	raw, status := fixtureCall(t, client, "GET", "http://"+operator+"/api/runs?limit=10", "", nil)
	if status != 200 {
		return
	}
	var runs []struct{ ID, Status, Summary string }
	if json.Unmarshal(raw, &runs) != nil {
		return
	}
	for _, run := range runs {
		t.Log(run.Status, run.Summary)
		req, _ := http.NewRequest("GET", "http://"+operator+"/runs/"+run.ID+"/artifacts/output.txt", nil)
		if resp, err := client.Do(req); err == nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			t.Log(string(data))
		}
	}
}

type fixtureWakeHook struct {
	mqtt.HookBase
	count *atomic.Int64
}

func (h *fixtureWakeHook) ID() string           { return "fixture-wake" }
func (h *fixtureWakeHook) Provides(b byte) bool { return b == mqtt.OnPublished }
func (h *fixtureWakeHook) OnPublished(_ *mqtt.Client, packet packets.Packet) {
	if strings.HasPrefix(packet.TopicName, "mitos/worker/v1/") {
		h.count.Add(1)
	}
}
