package application

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

type conversationWakeCapture struct {
	mqtt.HookBase
	subscriptions atomic.Int64
	mu            sync.Mutex
	payloads      [][]byte
}

func (h *conversationWakeCapture) ID() string { return "conversation-wake-capture" }
func (h *conversationWakeCapture) Provides(b byte) bool {
	return b == mqtt.OnPublished || b == mqtt.OnSubscribed
}
func (h *conversationWakeCapture) OnSubscribed(_ *mqtt.Client, pk packets.Packet, _ []byte) {
	for _, filter := range pk.Filters {
		if strings.HasPrefix(filter.Filter, "$share/mitos-workers/mitos/worker/v1/") {
			h.subscriptions.Add(1)
		}
	}
}
func (h *conversationWakeCapture) OnPublished(_ *mqtt.Client, pk packets.Packet) {
	if strings.HasPrefix(pk.TopicName, "mitos/worker/v1/") {
		h.mu.Lock()
		h.payloads = append(h.payloads, append([]byte(nil), pk.Payload...))
		h.mu.Unlock()
	}
}
func conversationWait(t *testing.T, why string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("conversation fixture timed out:", why)
}

func TestPersonalNegotiationConfinementAndConsistency(t *testing.T) {
	if testing.Short() {
		t.Skip("independent subprocess fixture")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap required")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	build := t.TempDir()
	serverBinary := filepath.Join(build, "mitos")
	appBinary := filepath.Join(build, "conversationfixture")
	for _, item := range []struct{ dir, out, target string }{{root, serverBinary, "./cmd/mitos"}, {filepath.Join(root, "examples", "conversationfixture"), appBinary, "."}} {
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", item.out, item.target)
		cmd.Dir = item.dir
		if raw, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build conversation fixture: %v %s", err, raw)
		}
	}
	for _, race := range []string{"registration", "restart", "coordinator_replacement", "preferences"} {
		t.Run(race, func(t *testing.T) {
			dir := fixtureDir(t)
			data := filepath.Join(dir, "runtime")
			workspace := filepath.Join(dir, "workspace")
			bin := filepath.Join(dir, "bin")
			workerData := filepath.Join(dir, "worker")
			applicationDB := filepath.Join(dir, "conversation.db")
			for _, path := range []string{workspace, bin} {
				if err = os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			flags := "-X main.conversationMode=1 -X main.forbiddenPath=" + applicationDB
			if race == "registration" {
				flags += " -X main.waitGate=1"
			}
			if race == "preferences" {
				flags += " -X main.personalGate=1"
			}
			model := exec.Command("go", "build", "-buildvcs=false", "-ldflags", flags, "-o", filepath.Join(bin, "codex"), "./testdata/codex.go")
			model.Dir = filepath.Join(root, "examples", "appfixture")
			if raw, err := model.CombinedOutput(); err != nil {
				t.Fatalf("build conversation native stand-in: %v %s", err, raw)
			}
			broker := mqtt.New(&mqtt.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
			capture := &conversationWakeCapture{}
			if err = broker.AddHook(new(auth.AllowHook), nil); err != nil {
				t.Fatal(err)
			}
			if err = broker.AddHook(capture, nil); err != nil {
				t.Fatal(err)
			}
			listener := listeners.NewTCP(listeners.Config{ID: "conversation-wake", Address: "127.0.0.1:0"})
			if err = broker.AddListener(listener); err != nil {
				t.Fatal(err)
			}
			if err = broker.Serve(); err != nil {
				t.Fatal(err)
			}
			defer broker.Close()
			wakeConfig := filepath.Join(dir, "wake.json")
			raw, _ := json.Marshal(map[string]string{"broker": "mqtt://" + listener.Address()})
			if err = os.WriteFile(wakeConfig, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cert, key := fixtureCertificate(t, dir)
			operator, remote := fixtureAddress(t), fixtureAddress(t)
			roots := x509.NewCertPool()
			pem, _ := os.ReadFile(cert)
			roots.AppendCertsFromPEM(pem)
			client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}
			startServer := func() *fixtureProcess {
				log, err := os.CreateTemp(dir, "authority-*.log")
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(serverBinary, "serve", "--data", data, "--workspace-root", workspace, "--addr", operator, "--application-addr", remote, "--application-cert", cert, "--application-key", key, "--worker-wake-config", wakeConfig)
				cmd.Env = fixtureEnv(map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"), "MITOS_AGENT_CONFINEMENT": "bwrap", "MITOS_AGENT_NETWORK": "unrestricted", "MITOS_LOCAL_STRUCTURED_WORKERS": "off", "MITOS_ENABLE_FAILPOINTS": "0"})
				cmd.Stdout = log
				cmd.Stderr = log
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				p := &fixtureProcess{cmd: cmd, log: log}
				defer func() {
					if t.Failed() {
						raw, _ := os.ReadFile(log.Name())
						t.Logf("authority log:\n%s", raw[max(0, len(raw)-6000):])
					}
				}()
				conversationWait(t, "authority startup", func() bool {
					req, _ := http.NewRequest("GET", "http://"+operator+"/api/capabilities", nil)
					req.Header.Set("Mitos-API-Version", "1")
					response, err := client.Do(req)
					if err != nil {
						return false
					}
					response.Body.Close()
					return response.StatusCode == 200
				})
				return p
			}
			runtime := startServer()
			defer func() { runtime.stop() }()
			raw, status := fixtureCall(t, client, "POST", "http://"+operator+"/api/service-principals", "", map[string]any{"id": "conversation-app", "name": "Conversation fixture", "tenant": map[string]string{"org": "conversation", "project": "app"}, "read": true, "write": true, "expires_at": time.Now().Add(time.Hour)})
			var applicationCredential struct {
				Token string `json:"token"`
			}
			if status != 201 || json.Unmarshal(raw, &applicationCredential) != nil {
				t.Fatal("application credential", status, string(raw))
			}
			raw, status = fixtureCall(t, client, "POST", "http://"+operator+"/api/workers", "", map[string]any{"id": "conversation-worker", "pools": []string{"interactive"}, "agents": []string{"mitos.generalist"}, "expires_at": time.Now().Add(time.Hour)})
			var workerCredential struct {
				Token string `json:"token"`
			}
			if status != 201 || json.Unmarshal(raw, &workerCredential) != nil {
				t.Fatal("worker credential", status, string(raw))
			}
			workers := []*fixtureProcess{}
			defer func() {
				for _, p := range workers {
					p.stop()
				}
			}()
			startWorker := func() *fixtureProcess {
				log, err := os.CreateTemp(dir, "worker-*.log")
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(serverBinary, "worker", "--url", "https://"+remote, "--id", "conversation-worker", "--data", workerData, "--codex", filepath.Join(bin, "codex"), "--ca-file", cert, "--wake-config", wakeConfig)
				cmd.Env = fixtureEnv(map[string]string{"MITOS_WORKER_TOKEN": workerCredential.Token, "OPENAI_API_KEY": "conversation-model-key", "MITOS_ENABLE_FAILPOINTS": "0"})
				cmd.Stdout = log
				cmd.Stderr = log
				if err = cmd.Start(); err != nil {
					t.Fatal(err)
				}
				p := &fixtureProcess{cmd: cmd, log: log, done: make(chan error, 1)}
				go func() { p.done <- cmd.Wait(); close(p.done) }()
				workers = append(workers, p)
				return p
			}
			originalWorker := startWorker()
			conversationWait(t, "worker registration and shared subscription", func() bool { return capture.subscriptions.Load() >= 1 })
			candidate := "candidate-canary-" + race
			company := "company-canary-" + race
			invoke := func(point, version string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
				defer cancel()
				args := []string{"--database", applicationDB, "--mitos", "https://" + remote, "--ca-file", cert, "--conversation", "personal-one", "--candidate-context", candidate, "--company-context", company, "--preferences-version", version, "--tone", "concise", "--timeout", "75s"}
				if race == "registration" {
					args = append(args, "--answer-early")
				}
				cmd := exec.CommandContext(ctx, appBinary, args...)
				cmd.Env = fixtureEnv(map[string]string{"MITOS_APPLICATION_TOKEN": applicationCredential.Token, "MITOS_ENABLE_FAILPOINTS": "1", "MITOS_CONVERSATION_FAILPOINT": point})
				return cmd.CombinedOutput()
			}
			type appResult struct {
				raw []byte
				err error
			}
			var output []byte
			if race == "restart" || race == "coordinator_replacement" {
				output, err = invoke("after_wait", "0")
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 86 {
					t.Fatalf("coordinator did not checkpoint: %v %s", err, output)
				}
				if race == "restart" {
					runtime.stop()
					runtime = startServer()
				} else {
					_ = startWorker()
					conversationWait(t, "replacement incarnation registration", func() bool { return capture.subscriptions.Load() >= 2 })
					originalWorker.stop()
				}
				output, err = invoke("", "0")
			} else if race == "registration" {
				done := make(chan appResult, 1)
				go func() { raw, err := invoke("", "0"); done <- appResult{raw, err} }()
				conversationWait(t, "answer committed before native wait", func() bool {
					db, err := sql.Open("sqlite", applicationDB)
					if err != nil {
						return false
					}
					defer db.Close()
					var count int
					err = db.QueryRow("SELECT count(*) FROM input_intents WHERE done=1").Scan(&count)
					return err == nil && count == 1
				})
				var negotiationRun string
				conversationWait(t, "native wait registration barrier", func() bool {
					raw, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs?limit=200", applicationCredential.Token, nil)
					var runs []struct {
						ID    string `json:"id"`
						Brief struct {
							Structured struct {
								Input struct {
									Kind string `json:"kind"`
								} `json:"input"`
							} `json:"structured"`
						} `json:"brief"`
					}
					if status != 200 || json.Unmarshal(raw, &runs) != nil {
						return false
					}
					for _, run := range runs {
						if run.Brief.Structured.Input.Kind == "negotiation" {
							if _, err := os.Stat(filepath.Join(workerData, run.ID, "workspace", "wait-started")); err == nil {
								negotiationRun = run.ID
								return true
							}
						}
					}
					return false
				})
				if err = os.WriteFile(filepath.Join(workerData, negotiationRun, "workspace", "allow-wait-registration"), []byte("continue"), 0600); err != nil {
					t.Fatal(err)
				}
				result := <-done
				output, err = result.raw, result.err
			} else {
				output, err = invoke("after_personal_admission", "0")
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 86 {
					t.Fatalf("personal admission boundary: %v %s", err, output)
				}
				var oldRun string
				conversationWait(t, "personal consumed old preference snapshot", func() bool {
					raw, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs?limit=200", applicationCredential.Token, nil)
					var runs []struct {
						ID    string `json:"id"`
						Brief struct {
							Structured struct {
								Input struct {
									Kind string `json:"kind"`
								} `json:"input"`
							} `json:"structured"`
						} `json:"brief"`
					}
					if status != 200 || json.Unmarshal(raw, &runs) != nil {
						return false
					}
					for _, run := range runs {
						if run.Brief.Structured.Input.Kind == "personal" {
							if _, err := os.Stat(filepath.Join(workerData, run.ID, "workspace", "personal-started")); err == nil {
								oldRun = run.ID
								return true
							}
						}
					}
					return false
				})
				done := make(chan appResult, 1)
				go func() { raw, err := invoke("", "1"); done <- appResult{raw, err} }()
				conversationWait(t, "new preferences committed", func() bool {
					raw, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/events?after=0&limit=200", applicationCredential.Token, nil)
					var events []struct {
						Kind string `json:"kind"`
					}
					if status != 200 || json.Unmarshal(raw, &events) != nil {
						return false
					}
					for _, event := range events {
						if event.Kind == "tenant_versions_advanced" {
							return true
						}
					}
					return false
				})
				conversationWait(t, "current personal update queued behind old holder", func() bool {
					raw, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs?limit=200", applicationCredential.Token, nil)
					var pending []struct {
						Status string `json:"status"`
						Brief  struct {
							Structured struct {
								Input struct {
									Kind string `json:"kind"`
								} `json:"input"`
								ConsumedVersions map[string]uint64 `json:"consumed_versions"`
							} `json:"structured"`
						} `json:"brief"`
					}
					if status != 200 || json.Unmarshal(raw, &pending) != nil {
						return false
					}
					for _, run := range pending {
						if run.Brief.Structured.Input.Kind == "personal" && run.Brief.Structured.ConsumedVersions["preferences"] == 1 {
							if run.Status == "running" {
								t.Fatal("two active personal conversation holders")
							}
							return run.Status == "queued"
						}
					}
					return false
				})

				if err = os.WriteFile(filepath.Join(workerData, oldRun, "workspace", "allow-personal-result"), []byte("publish candidate"), 0600); err != nil {
					t.Fatal(err)
				}
				result := <-done
				output, err = result.raw, result.err
				raw, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs/"+oldRun, applicationCredential.Token, nil)
				var failed struct {
					Status   string `json:"status"`
					Verified bool   `json:"verified"`
				}
				if status != 200 || json.Unmarshal(raw, &failed) != nil || failed.Status != "failed" || failed.Verified {
					t.Fatal("superseded personal result published", status, string(raw))
				}
			}
			if err != nil || !strings.Contains(string(output), "Negotiation accepted.") {
				for _, p := range workers {
					raw, _ := os.ReadFile(p.log.Name())
					t.Log("worker:", string(raw))
				}
				t.Fatalf("personal/negotiation fixture: %v %s", err, output)
			}
			version := "0"
			if race == "preferences" {
				version = "1"
			}
			if again, err := invoke("", version); err != nil || string(again) != string(output) {
				t.Fatal("coordinator replay changed publication", err, string(again))
			}
			db, err := sql.Open("sqlite", applicationDB)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"outcomes", "replies", "input_intents"} {
				var count int
				if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
					t.Fatal("duplicate application", table, count, err)
				}
			}
			raw, status = fixtureCall(t, client, "GET", "https://"+remote+"/api/runs?limit=200", applicationCredential.Token, nil)
			var runs []struct {
				ID        string `json:"id"`
				Status    string `json:"status"`
				Verified  bool   `json:"verified"`
				Artifacts []struct {
					Name string `json:"name"`
				} `json:"artifacts"`
				Brief struct {
					Structured struct {
						Input struct {
							Kind string `json:"kind"`
						} `json:"input"`
						ConsumedVersions map[string]uint64 `json:"consumed_versions"`
					} `json:"structured"`
				} `json:"brief"`
			}
			if status != 200 || json.Unmarshal(raw, &runs) != nil {
				t.Fatal("read conversations", status, string(raw))
			}
			acceptedPersonal, acceptedNegotiation := 0, 0
			continued := false
			for _, run := range runs {
				if run.Status == "done" && run.Verified {
					if run.Brief.Structured.Input.Kind == "personal" {
						acceptedPersonal++
						if race == "preferences" && run.Brief.Structured.ConsumedVersions["preferences"] != 1 {
							t.Fatal("published outdated preferences")
						}
					} else {
						acceptedNegotiation++
					}
				}
				events, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs/"+run.ID+"/events?limit=200", applicationCredential.Token, nil)
				if status != 200 {
					t.Fatal(status)
				}
				if run.Brief.Structured.Input.Kind == "personal" {
					if strings.Contains(string(events), candidate) || strings.Contains(string(events), company) {
						t.Fatal("private context escaped into personal thread events")
					}
					for _, artifact := range run.Artifacts {
						body, status := fixtureCall(t, client, "GET", "https://"+remote+"/api/runs/"+run.ID+"/artifacts/"+artifact.Name, applicationCredential.Token, nil)
						if status != 200 || strings.Contains(string(body), candidate) || strings.Contains(string(body), company) {
							t.Fatal("private context escaped into personal thread artifact", artifact.Name, status)
						}
					}
				}
				if strings.Contains(string(events), `"state":"continue"`) {
					continued = true
				}
			}
			if acceptedPersonal != 1 || acceptedNegotiation != 1 {
				t.Fatal("duplicate accepted conversation outcome", acceptedPersonal, acceptedNegotiation)
			}
			if race == "registration" && !continued {
				t.Fatal("registration race did not exercise existing-input continuation")
			}
			conversationWait(t, "committed MQTT notification", func() bool { capture.mu.Lock(); defer capture.mu.Unlock(); return len(capture.payloads) >= 2 })
			capture.mu.Lock()
			defer capture.mu.Unlock()
			for _, payload := range capture.payloads {
				var hint map[string]json.RawMessage
				if json.Unmarshal(payload, &hint) != nil || len(hint) > 2 || len(payload) > 512 || hint["pool"] == nil || strings.Contains(string(payload), candidate) || strings.Contains(string(payload), company) {
					t.Fatal("private context escaped into notification", string(payload))
				}
				for name := range hint {
					if name != "pool" && name != "capability" {
						t.Fatal("unexpected notification field", name)
					}
				}
			}
		})
	}
}
