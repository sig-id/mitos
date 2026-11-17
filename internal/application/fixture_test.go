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
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fixtureProcess struct {
	cmd *exec.Cmd
	log *os.File
}

func (p *fixtureProcess) stop() {
	if p == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
	_ = p.log.Close()
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
			dir := t.TempDir()
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
			modelBuild := exec.Command("go", "build", "-buildvcs=false", "-ldflags", "-X main.forbiddenPath="+filepath.Join(data, "runtime.db")+" -X main.forbiddenDial="+operator, "-o", filepath.Join(bin, "codex"), "./testdata/codex.go")
			modelBuild.Dir = filepath.Join(root, "examples", "appfixture")
			if raw, err := modelBuild.CombinedOutput(); err != nil {
				t.Fatalf("build confined model stand-in: %v\n%s", err, raw)
			}
			roots := x509.NewCertPool()
			pemBytes, _ := os.ReadFile(cert)
			roots.AppendCertsFromPEM(pemBytes)
			client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}, Timeout: 2 * time.Second}
			start := func() *fixtureProcess {
				log, err := os.CreateTemp(dir, "server-*.log")
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(serverBinary, "serve", "--data", data, "--workspace-root", workspace, "--addr", operator, "--application-addr", remote, "--application-cert", cert, "--application-key", key)
				cmd.Env = fixtureEnv(map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"), "MITOS_AGENT_CONFINEMENT": "bwrap", "MITOS_AGENT_NETWORK": "unrestricted", "OPENAI_API_KEY": "fixture-model-key", "MITOS_ENABLE_FAILPOINTS": "0", "MITOS_LOCAL_STRUCTURED_WORKERS": map[bool]string{true: "off", false: "on"}[remoteWorker]})
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
			if remoteWorker {
				raw, status := fixtureCall(t, client, "POST", "http://"+operator+"/api/workers", "", map[string]any{"id": "separate-worker", "pools": []string{"interactive"}, "agents": []string{"mitos.generalist"}, "expires_at": time.Now().Add(time.Hour)})
				var credential struct {
					Token string `json:"token"`
				}
				if status != 201 || json.Unmarshal(raw, &credential) != nil || credential.Token == "" {
					t.Fatal("worker credential", status, string(raw))
				}
				workerLog, err := os.CreateTemp(dir, "worker-*.log")
				if err != nil {
					t.Fatal(err)
				}
				workerCmd := exec.Command(serverBinary, "worker", "--url", "https://"+remote, "--id", "separate-worker", "--data", filepath.Join(dir, "worker"), "--ca-file", cert, "--codex", filepath.Join(bin, "codex"))
				workerCmd.Env = fixtureEnv(map[string]string{"MITOS_WORKER_TOKEN": credential.Token, "OPENAI_API_KEY": "fixture-worker-model-key"})
				workerCmd.Stdout = workerLog
				workerCmd.Stderr = workerLog
				if err = workerCmd.Start(); err != nil {
					t.Fatal(err)
				}
				worker := &fixtureProcess{cmd: workerCmd, log: workerLog}
				defer worker.stop()
			}

			applicationDB := filepath.Join(dir, "application.db")
			invoke := func(failpoint string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, appBinary, "--database", applicationDB, "--mitos", "https://"+remote, "--ca-file", cert, "--message-id", "once", "--text", "Hello", "--timeout", "40s")
				cmd.Env = fixtureEnv(map[string]string{"MITOS_APPLICATION_TOKEN": principal.Token, "MITOS_ENABLE_FAILPOINTS": "1", "MITOS_APPFIXTURE_FAILPOINT": failpoint})
				return cmd.CombinedOutput()
			}
			output, err := invoke(point)
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 86 {
				fixtureRunDiagnostics(t, client, operator)
				raw, _ := os.ReadFile(runtime.log.Name())
				t.Fatalf("app did not reach %s: %v\n%s\nserver:\n%s", point, err, output, raw)
			}
			runtime.stop()
			runtime = start()
			if output, err = invoke(""); err != nil || !strings.Contains(string(output), `"reply":"ack"`) {
				fixtureRunDiagnostics(t, client, operator)
				raw, _ := os.ReadFile(runtime.log.Name())
				t.Fatalf("recovery: %v\n%s\nserver:\n%s", err, output, raw)
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
				TaskID string `json:"task_id"`
			}
			if err = json.Unmarshal(raw, &runs); err != nil {
				t.Fatal(err)
			}
			tasks := map[string]bool{}
			for _, run := range runs {
				tasks[run.TaskID] = true
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
