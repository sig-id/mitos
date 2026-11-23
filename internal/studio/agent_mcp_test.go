package studio

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/agentcli"
	"mitos/internal/compute"
	"mitos/internal/harness"
)

// agentMCPPath is tested here against the selection made in automatic.go.
func TestAgentMCPPathSelection(t *testing.T) {
	if !agentMCPPath(false, "pi") {
		t.Fatal("unconfined pi should use the MCP path")
	}
	for runtime, confined := range map[string]bool{"pi": true, "opencode": false, "codex": false, "gemini": false} {
		if agentMCPPath(confined, runtime) {
			t.Fatalf("%s (confined=%v) selected the MCP path", runtime, confined)
		}
	}
}

func testMCPOptions() harness.CodexOptions {
	return harness.CodexOptions{
		Tools:    []harness.DynamicTool{{Name: "result_submit", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolCall: func(string, json.RawMessage) (any, error) { return map[string]string{"ok": "1"}, nil },
		Result:   func(string) error { return nil },
	}
}

func TestAgentMCPLoopbackEndpointLifecycle(t *testing.T) {
	mcp, err := startAgentMCP(context.Background(), "pi", nil, testMCPOptions(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcp.close()
	if !strings.HasPrefix(mcp.url, "http://127.0.0.1:") || !strings.HasSuffix(mcp.url, "/mcp") {
		t.Fatalf("endpoint %q", mcp.url)
	}
	request, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}})
	response, err := http.Post(mcp.url, "application/json", strings.NewReader(string(request)))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	// The per-attempt token is required.
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("endpoint answered without the attempt token: %d %s", response.StatusCode, body)
	}
	// The adapter's configuration carries the URL and the bearer token.
	var decoded map[string]any
	if json.Unmarshal(mcp.files.Files["mcp.json"], &decoded) != nil {
		t.Fatal(mcp.files.Files["mcp.json"])
	}
	entry := decoded["mcpServers"].(map[string]any)["mitos"].(map[string]any)
	if entry["url"] != mcp.url || !strings.HasPrefix(entry["headers"].(map[string]any)["Authorization"].(string), "Bearer ") {
		t.Fatalf("mcp.json: %v", entry)
	}
	mcp.close()
	if _, err = http.Post(mcp.url, "application/json", strings.NewReader(string(request))); err == nil {
		t.Fatal("endpoint still serving after revocation")
	}
}

func TestAgentMCPAttachesToTheModelBrokerLocalRoute(t *testing.T) {
	// Keep the socket path inside every supported platform's unix socket
	// address limit; the standard test temp root is longer on some machines.
	dir, derr := os.MkdirTemp("", "mitos-mcp-")
	if derr != nil {
		t.Fatal(derr)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	broker, err := compute.StartModelBroker(filepath.Join(dir, "b.sock"), map[string]string{compute.BrokerVendorAnthropic: "sk-ant-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	mcp, err := startAgentMCP(context.Background(), "pi", nil, testMCPOptions(), false, broker)
	if err != nil {
		t.Fatal(err)
	}
	defer mcp.close()
	if mcp.url != compute.BrokerLocalURL("/mcp") {
		t.Fatalf("endpoint %q", mcp.url)
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", brokerSocketPath(broker))
	}}}
	probe, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-06-18"}})
	request, _ := http.NewRequest(http.MethodPost, mcp.url, strings.NewReader(string(probe)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+mcpToken(mcp))
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "mitos") {
		t.Fatalf("local route: %d %s", response.StatusCode, body)
	}
	// Closing the attempt revokes the route ahead of the broker's own close.
	mcp.close()
	request, _ = http.NewRequest(http.MethodPost, mcp.url, strings.NewReader(string(probe)))
	request.Header.Set("Content-Type", "application/json")
	response, err = client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("detached route answered %d", response.StatusCode)
	}
}

func TestPiAttemptConfigIsolatesTheAgentDirectory(t *testing.T) {
	host := t.TempDir()
	dir := filepath.Join(host, ".pi", "agent")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"settings.json": `{"defaultProvider":"zai","defaultModel":"glm-5.3","packages":["npm:x"]}`,
		"models.json":   `{"providers":{"zai":{"apiKey":"k"}}}`,
		"auth.json":     `{"zai":{"type":"api_key","key":"zai"}}`,
		"mcp.json":      `{"mcpServers":{"personal":{"url":"https://example.com"}}}`,
	} {
		if writeErr := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	base := t.TempDir()
	config, env, err := piAttemptConfig([]string{"HOME=" + host}, base)
	if err != nil {
		t.Fatal(err)
	}
	if config != filepath.Join(base, "home", ".pi", "agent") || env != "PI_CODING_AGENT_DIR="+config {
		t.Fatalf("%q %q", config, env)
	}
	for _, name := range []string{"settings.json", "models.json", "auth.json"} {
		if _, statErr := os.Stat(filepath.Join(config, name)); statErr != nil {
			t.Fatalf("operator %s not kept: %v", name, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(config, "mcp.json")); !os.IsNotExist(statErr) {
		t.Fatal("operator MCP servers leaked into the attempt")
	}
}

func TestPiAttemptsReuseInstalledNPMPackages(t *testing.T) {
	host := t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	settings := `{"packages":["npm:adapter",{"source":"npm:@team/tools@2.0.0","skills":[]},"npm:@team/missing"]}`
	write(filepath.Join(host, "settings.json"), settings)
	write(filepath.Join(host, "npm/package.json"), `{"scripts":{"install":"exit 1"},"dependencies":{"adapter":"^1","@team/tools":"2.0.0","@team/missing":"1","../escape":"1"}}`)
	write(filepath.Join(host, "npm/escape/package.json"), "{}")
	for _, name := range []string{"adapter", "@team/tools"} {
		write(filepath.Join(host, "npm/node_modules", name, "package.json"), `{"name":"`+name+`"}`)
	}
	env := []string{"PI_CODING_AGENT_DIR=" + host}
	for range 2 {
		base := t.TempDir()
		cfg, _, err := piAttemptConfig(env, base)
		if err != nil {
			t.Fatal(err)
		}
		if raw, err := os.ReadFile(filepath.Join(cfg, "settings.json")); err != nil || string(raw) != settings {
			t.Fatalf("staged package sources and filters: %q, %v", raw, err)
		}
		for _, name := range []string{"adapter", "@team/tools"} {
			rel := filepath.Join("npm/node_modules", name, "package.json")
			source, err := os.Stat(filepath.Join(host, rel))
			if err != nil {
				t.Fatal(err)
			}
			staged, err := os.Stat(filepath.Join(cfg, rel))
			if err != nil || !os.SameFile(source, staged) {
				t.Fatalf("%s did not reuse the installed package: %v", name, err)
			}
		}
		raw, err := os.ReadFile(filepath.Join(cfg, "npm/package.json"))
		var manifest struct {
			Dependencies map[string]string
			Scripts      json.RawMessage
		}
		if err != nil || json.Unmarshal(raw, &manifest) != nil || len(manifest.Scripts) != 0 ||
			len(manifest.Dependencies) != 2 || manifest.Dependencies["adapter"] != "^1" || manifest.Dependencies["@team/tools"] != "2.0.0" {
			t.Fatalf("unexpected npm manifest: %s, %v", raw, err)
		}
		// A fallback install and a later staging pass use the attempt's own prefix.
		missing := filepath.Join("npm/node_modules/@team/missing/package.json")
		write(filepath.Join(cfg, missing), "{}")
		if _, _, err = piAttemptConfig(env, base); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(cfg, missing)); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(filepath.Join(host, missing)); !os.IsNotExist(err) {
			t.Fatalf("attempt wrote through to host: %v", err)
		}
	}
}

func TestAgentMCPArgumentsAndPrompt(t *testing.T) {
	mcp, err := startAgentMCP(context.Background(), "pi", nil, testMCPOptions(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer mcp.close()
	print := []string{"--print", "--no-session", "--no-extensions", "--no-mcp", "--tools", "read"}
	first := mcp.arguments("/usr/bin/pi", print, false)
	resume := mcp.arguments("/usr/bin/pi", print, true)
	joined := strings.Join(first, " ")
	if strings.Contains(joined, "--no-mcp") || strings.Contains(joined, "--no-session") || !strings.Contains(joined, "--session-id "+mcp.session) {
		t.Fatalf("first run: %v", first)
	}
	if !strings.Contains(strings.Join(resume, " "), "--session "+mcp.session) || strings.Contains(strings.Join(resume, " "), "--session-id") {
		t.Fatalf("resume: %v", resume)
	}
	worker := mcpPrompt("Do the work.", testMCPOptions())
	if strings.Contains(worker, "MITOS_REPLY") || !strings.Contains(worker, "mcp__mitos__result_submit") || !strings.Contains(worker, "mcp__mitos__report_submit") {
		t.Fatalf("worker prompt: %s", worker)
	}
	orchestrator := mcpPrompt("Plan.", harness.CodexOptions{OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if !strings.Contains(orchestrator, "mcp__mitos__plan_submit") || strings.Contains(orchestrator, "report_submit") {
		t.Fatalf("orchestrator prompt: %s", orchestrator)
	}
	if continuation := string(mcp.continuationInput()); !strings.Contains(continuation, "report_submit") {
		t.Fatalf("continuation: %s", continuation)
	}
	if _, err = startAgentMCP(context.Background(), "opencode", nil, testMCPOptions(), false, nil); err == nil {
		t.Fatal("opencode started an MCP endpoint")
	}
}

func TestAgentMCPRunContinuesOnceThenFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Without a terminal submission the CLI is told once to continue; a
	// second exit without one fails like the text protocol does.
	mcp := &agentMCP{server: harness.NewMCPServer(harness.MCPOptions{Terminal: true}), session: agentcli.NewSessionID(), close: func() {}}
	first := &mcpExitSession{output: []byte("first exit\n")}
	output, err := mcp.run(ctx, first, func() (compute.Session, error) {
		return &mcpExitSession{output: []byte("second exit\n")}, nil
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "result_submission_missing") || !strings.Contains(string(output), "second exit") {
		t.Fatalf("run: %v %s", err, output)
	}
	// A finished attempt returns as soon as the process exits.
	finished := harness.NewMCPServer(harness.MCPOptions{Terminal: true, Result: func(string) error { return nil }})
	if _, callErr := finishedMessage(finished); callErr != nil {
		t.Fatal(callErr)
	}
	mcp = &agentMCP{server: finished, session: agentcli.NewSessionID(), close: func() {}}
	if output, err = mcp.run(ctx, &mcpExitSession{output: []byte("done\n")}, func() (compute.Session, error) {
		t.Fatal("continuation after a terminal submission")
		return nil, nil
	}, io.Discard); err != nil || !strings.Contains(string(output), "done") {
		t.Fatalf("finished run: %v %s", err, output)
	}
}

// finishedMessage drives one terminal submission through the streamable HTTP
// transport so the server records a finished attempt.
func finishedMessage(server *harness.MCPServer) (string, error) {
	token := harness.NewMCPToken()
	handler := server.StreamableHTTP(token)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	httpServer := &http.Server{Handler: handler}
	go func() { _ = httpServer.Serve(listener) }()
	defer httpServer.Close()
	session := ""
	call := func(body string) (string, error) {
		request, err := http.NewRequest(http.MethodPost, "http://"+listener.Addr().String()+"/mcp", strings.NewReader(body))
		if err != nil {
			return "", err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+token)
		if session != "" {
			request.Header.Set("Mcp-Session-Id", session)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		if next := response.Header.Get("Mcp-Session-Id"); next != "" {
			session = next
		}
		text, _ := io.ReadAll(response.Body)
		return string(text), nil
	}
	if _, err = call(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`); err != nil {
		return "", err
	}
	return call(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"report_submit","arguments":{"report":"done"}}}`)
}

type mcpExitSession struct {
	output []byte
}

func (d *mcpExitSession) ID() string              { return "done" }
func (d *mcpExitSession) PID() int                { return 0 }
func (d *mcpExitSession) Done() <-chan struct{}   { ch := make(chan struct{}); close(ch); return ch }
func (d *mcpExitSession) Result() ([]byte, error) { return d.output, nil }
func (d *mcpExitSession) OutputOffset() uint64    { return 0 }

// brokerSocketPath and mcpToken are thin wrappers keeping the test helpers
// independent of the fields they read.
func brokerSocketPath(b *compute.ModelBroker) string { return b.Endpoint()[len("unix://"):] }
func mcpToken(m *agentMCP) string                    { return strings.TrimPrefix(m.headers["Authorization"], "Bearer ") }

// Operator-only operations are refused before delivery so the orchestrator
// corrects the plan in-session instead of spending another call.
func TestMCPPlanRulesRefuseOperatorOnlyOperations(t *testing.T) {
	plan := []byte(`{"operations":[{"kind":"accept","task":"task_1","rationale":"r"},{"kind":"add","spec":{}}],"accept":[{"task":"task_1","rationale":"r"}],"complete":true,"result":{},"explanation":"e"}`)
	problems := mcpPlanRules(plan)
	if len(problems) != 1 || !strings.Contains(problems[0], "operator-only") || !strings.Contains(problems[0], "accept list") {
		t.Fatalf("problems: %v", problems)
	}
	if problems := mcpPlanRules([]byte(`{"operations":[{"kind":"add","spec":{}}],"accept":[],"complete":false,"result":{},"explanation":"e"}`)); len(problems) != 0 {
		t.Fatalf("clean plan refused: %v", problems)
	}
}

func TestAgentMCPRunSavesRenderedLogWithDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished := harness.NewMCPServer(harness.MCPOptions{Terminal: true, Result: func(string) error { return nil }})
	if _, callErr := finishedMessage(finished); callErr != nil {
		t.Fatal(callErr)
	}
	mcp := &agentMCP{server: finished, session: agentcli.NewSessionID(), close: func() {}}
	rendered, diagnostics := &printOutput{}, &printOutput{}
	mcp.rendered, mcp.stderr, mcp.stream = rendered, diagnostics, harness.NewPiEventStream(io.MultiWriter(io.Discard, rendered), io.Discard)
	// The stream renders into the live preview and the saved log, as the
	// MCP command path assembles it. The process streamed one rendered message, then failed with a
	// diagnostic on stderr; the raw capture mixes both with the JSONL.
	if _, err := mcp.stream.Write([]byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The work is done."}]}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	diagnostics.Write([]byte("pi: provider request failed\n"))
	raw := []byte(`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"The work is done."}]}}` + "\npi: provider request failed\n")
	output, err := mcp.run(ctx, &mcpExitSession{output: raw}, func() (compute.Session, error) {
		t.Fatal("continuation after a terminal submission")
		return nil, nil
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "The work is done.") || !strings.Contains(string(output), "pi: provider request failed") {
		t.Fatalf("saved log must keep the rendered message and the diagnostics: %q", output)
	}
	if strings.Contains(string(output), `"type":"message_end"`) {
		t.Fatalf("the raw JSONL must not be saved when the rendered log exists: %q", output)
	}
}

// A CLI that died before rendering anything keeps its raw captured output,
// including whatever it printed on stderr.
func TestAgentMCPRunKeepsRawCaptureWithoutEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished := harness.NewMCPServer(harness.MCPOptions{Terminal: true, Result: func(string) error { return nil }})
	if _, callErr := finishedMessage(finished); callErr != nil {
		t.Fatal(callErr)
	}
	mcp := &agentMCP{server: finished, session: agentcli.NewSessionID(), close: func() {}}
	mcp.rendered, mcp.stderr, mcp.stream = &printOutput{}, &printOutput{}, harness.NewPiEventStream(io.Discard, io.Discard)
	raw := []byte("pi: cannot read settings.json\n")
	output, err := mcp.run(ctx, &mcpExitSession{output: raw}, func() (compute.Session, error) {
		t.Fatal("continuation after a terminal submission")
		return nil, nil
	}, io.Discard)
	if err != nil || !strings.Contains(string(output), "cannot read settings.json") {
		t.Fatalf("run: %v %q", err, output)
	}
}

// Ownership's literal rule is stateless, so the refusal the graph would
// raise only after the attempt ended (fleet.Ownership.Validate through
// resolveGraphSpec) is raised here and the orchestrator corrects the spec
// in its own session instead of spending another orchestrator call.
func TestMCPPlanRulesRefuseInvalidOwnership(t *testing.T) {
	for _, test := range []struct {
		name string
		plan string
		want []string
	}{
		{
			"duplicate paths",
			`{"operations":[{"kind":"add","spec":{"id":"t2","brief":{"ownership":{"paths":["src","src"]}}}}],"accept":[],"complete":false,"result":{},"explanation":"e"}`,
			[]string{"add", "t2", "src"},
		},
		{
			"glob path",
			`{"operations":[{"kind":"add","spec":{"id":"t3","brief":{"ownership":{"paths":["docs/*"]}}}}],"accept":[],"complete":false,"result":{},"explanation":"e"}`,
			[]string{"docs/*"},
		},
		{
			"absolute path on supersede",
			`{"operations":[{"kind":"supersede","task":"task_1","spec":{"id":"t4","brief":{"ownership":{"paths":["/repo/src"]}}},"rationale":"r"}],"accept":[],"complete":false,"result":{},"explanation":"e"}`,
			[]string{"supersede", "t4", "/repo/src"},
		},
		{
			"supersede without a spec reference names the task",
			`{"operations":[{"kind":"supersede","task":"task_9","spec":{"brief":{"ownership":{"leases":["db","db"]}}},"rationale":"r"}],"accept":[],"complete":false,"result":{},"explanation":"e"}`,
			[]string{"supersede", "task_9", "db"},
		},
	} {
		problems := mcpPlanRules([]byte(test.plan))
		if len(problems) != 1 {
			t.Fatalf("%s: problems: %v", test.name, problems)
		}
		for _, want := range test.want {
			if !strings.Contains(problems[0], want) {
				t.Fatalf("%s: refusal %q does not mention %q", test.name, problems[0], want)
			}
		}
		if !strings.Contains(problems[0], "workspace-relative") {
			t.Fatalf("%s: refusal lacks the correction guidance: %q", test.name, problems[0])
		}
	}
	clean := `{"operations":[{"kind":"add","spec":{"id":"t2","brief":{"ownership":{"paths":["src","docs/api"],"leases":["database:test"]}}}},{"kind":"add","spec":{}}],"accept":[],"complete":false,"result":{},"explanation":"e"}`
	if problems := mcpPlanRules([]byte(clean)); len(problems) != 0 {
		t.Fatalf("clean ownership refused: %v", problems)
	}
}
