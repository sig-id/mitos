package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mitos/internal/apiversion"
	"mitos/internal/compute"
	"mitos/internal/messaging"
	"mitos/internal/structuredworker"
	"mitos/internal/studio"
	"mitos/internal/workerwake"
)

func workerClient(args []string) error {
	if len(args) > 0 && (args[0] == "credential" || args[0] == "revoke") {
		return workerCredentialClient(args)
	}
	f := flag.NewFlagSet("worker", flag.ContinueOnError)
	wakeConfig := f.String("wake-config", "", "Optional MQTT wake configuration JSON")
	wakePool := f.String("wake-pool", "interactive", "Shared-subscription wake pool (claims still check all permitted pools)")
	authority := f.String("url", "", "Application TLS authority URL")
	id := f.String("id", "", "Operator-issued worker ID")
	tokenEnv := f.String("token-env", "MITOS_WORKER_TOKEN", "Environment variable holding the worker credential")
	caFile := f.String("ca-file", "", "Additional PEM CA certificate")
	data := f.String("data", "", "Private worker scratch directory")
	codex := f.String("codex", "codex", "Native Codex executable")
	once := f.Bool("once", false, "Exit after one attempt")
	capabilities := f.String("agents", "mitos.generalist", "Comma-separated admitted agent IDs")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || !studio.ValidDeviceID(*id) || *data == "" || *authority == "" {
		return errors.New("worker requires --id, --url and --data")
	}
	token := os.Getenv(*tokenEnv)
	if len(token) != 64 {
		return errors.New("worker credential missing from token environment variable")
	}
	confinement, err := compute.NewConfinement(compute.ConfinementBwrap, compute.NetUnrestricted)
	if err != nil {
		return err
	}
	// The worker mediates its sandbox model transport itself: the confined
	// workload runs in an isolated network namespace whose only egress is this
	// allowlisted relay, never the host network.
	egressPath := filepath.Join(*data, "egress", "model.sock")
	relay, err := compute.StartEgressRelay(egressPath, compute.DefaultModelHosts)
	if err != nil {
		return err
	}
	defer relay.Close()
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	confinement.EgressSocket = egressPath
	confinement.HelperExecutable = helper
	provider := &compute.Local{Confinement: confinement}
	provider.SetEgressHelper(helper)
	executable, err := exec.LookPath(*codex)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(*data, 0700); err != nil {
		return err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if *caFile != "" {
		raw, err := os.ReadFile(*caFile)
		if err != nil {
			return err
		}
		if !roots.AppendCertsFromPEM(raw) {
			return errors.New("no CA certificate in --ca-file")
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	client := &structuredworker.HTTPClient{URL: *authority, Token: token, HTTP: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	executor := structuredworker.Executor{Provider: provider, Executable: executable, Environment: os.Environ(), Base: *data, Scoped: true, EgressEndpoint: "unix://" + egressPath}
	if *wakeConfig != "" {
		cfg, err := messaging.LoadConfig(*wakeConfig)
		if err != nil {
			return err
		}
		client.WakeFactory = func(ctx context.Context, authority string) (<-chan struct{}, func(), error) {
			wake, err := workerwake.New(ctx, cfg, authority, *wakePool)
			if err != nil {
				return nil, nil, err
			}
			return wake.Wake, wake.Close, nil
		}
	}

	err = client.Work(ctx, *id, strings.Split(*capabilities, ","), executor, *once)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func workerCredentialClient(args []string) error {
	f := flag.NewFlagSet("worker "+args[0], flag.ContinueOnError)
	server := f.String("server", "http://127.0.0.1:4646", "Local operator URL")
	id := f.String("id", "", "Worker ID")
	pools := f.String("pools", "interactive", "Comma-separated permitted pools")
	agents := f.String("agents", "mitos.generalist", "Comma-separated permitted agent IDs")
	expires := f.Duration("expires", 30*24*time.Hour, "Credential lifetime")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 || !studio.ValidDeviceID(*id) {
		return errors.New("worker credential requires --id")
	}
	method, path := "POST", "/api/workers"
	var body any = studio.WorkerCredential{ID: *id, Pools: strings.Split(*pools, ","), Agents: strings.Split(*agents, ","), ExpiresAt: time.Now().Add(*expires)}
	if args[0] == "revoke" {
		method = "DELETE"
		path += "/" + url.PathEscape(*id)
		body = nil
	}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(method, strings.TrimRight(*server, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	apiversion.Set(req)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	result, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("worker credential HTTP %d: %s", resp.StatusCode, result)
	}
	fmt.Print(string(result))
	return nil
}
