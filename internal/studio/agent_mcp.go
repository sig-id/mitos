package studio

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mitos/internal/agentcli"
	"mitos/internal/compute"
	"mitos/internal/harness"
)

// agentMCPPath reports whether a print runtime gets the native-tools MCP
// endpoint: it needs an adapter and a confinement that leaves an HTTP route
// to the host. Bubblewrap's egress relay is a CONNECT allowlist without HTTP
// routing, so it keeps the text protocol.
func agentMCPPath(confinementEnabled bool, runtime string) bool {
	return !confinementEnabled && agentcli.SupportsMCPHTTP(runtime)
}

// Native Mitos tools for print CLIs over a per-attempt MCP server with the
// streamable HTTP transport (issue #101): one CLI process, structured tool
// calls, and terminal submission tools. The text-frame protocol stays as
// the fallback for CLIs without an adapter and confinements without an HTTP
// route to the host.

// mcpDecisionThread identifies coordinated CLI decisions, as the text
// protocol's "coordinated-cli" thread does.
const mcpDecisionThread = "coordinated-cli"

// resultSubmissionMissing ends an MCP attempt whose process exited twice
// without a terminal tool call, as the text protocol fails it today.
const resultSubmissionMissing = "result_submission_missing: the attempt ended without a terminal tool submission; call result_submit, then report_submit or plan_submit. Saved evidence is retained"

// agentMCP is one attempt's MCP endpoint and its run state.
type agentMCP struct {
	server *harness.MCPServer
	// url and headers are what the CLI is configured with.
	url     string
	headers map[string]string
	// files are written into the CLI's configuration directory.
	files agentcli.MCPFiles
	// close revokes the endpoint: the broker route or loopback listener ends
	// and the token is dropped with it.
	close func()
	// session is the CLI's native conversation ID: Pi takes --session-id on
	// the first run and --session to continue it.
	session string

	serial int
	logs   printOutput
}

// startAgentMCP assembles the attempt's MCP server and serves it over the
// transport the confinement allows: the per-attempt model broker's local
// route under Nucleus, a loopback listener for unconfined runs. modelBroker
// is nil for unconfined runs.
func startAgentMCP(ctx context.Context, runtime string, broker harness.Broker, options harness.CodexOptions, allowCommand bool, modelBroker *compute.ModelBroker) (*agentMCP, error) {
	if !agentcli.SupportsMCPHTTP(runtime) {
		return nil, fmt.Errorf("%s has no HTTP MCP adapter", runtime)
	}
	deadline, _ := ctx.Deadline()
	timeout := 10 * time.Minute
	if !deadline.IsZero() {
		// Operator decisions block until they are answered, so the CLI's
		// per-request timeout must outlast the attempt deadline.
		timeout = time.Until(deadline) + 5*time.Minute
	}
	serial := 0
	decision := func(ctx context.Context, d harness.MCPDecision) (string, error) {
		serial++
		turn := fmt.Sprintf("mitos-mcp-%d-%s", serial, crand.Text())
		return cliDecision(ctx, broker, mcpDecisionThread, turn, d.Question, d.Command)
	}
	server := harness.NewMCPServer(harness.MCPOptions{
		Tools:        options.Tools,
		ToolCall:     options.ToolCall,
		Terminal:     true,
		Result:       options.Result,
		Decision:     decision,
		OutputSchema: options.OutputSchema,
		AllowCommand: allowCommand,
		MaxTurns:     options.MaxTurns,
		Activity:     options.Activity,
	})
	token := harness.NewMCPToken()
	headers := map[string]string{"Authorization": "Bearer " + token}
	handler := server.StreamableHTTP(token)
	a := &agentMCP{server: server, headers: headers, session: agentcli.NewSessionID()}
	if modelBroker != nil {
		modelBroker.ServeLocal(handler)
		a.url = compute.BrokerLocalURL("/mcp")
		a.close = func() { modelBroker.ServeLocal(nil) }
	} else {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 30 * time.Second}
		go func() { _ = httpServer.Serve(listener) }()
		a.url = "http://" + listener.Addr().String() + "/mcp"
		a.close = func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(shutdown)
			_ = httpServer.Close()
		}
	}
	files, err := agentcli.MCPHTTP(runtime, agentcli.MCPConfig{URL: a.url, Headers: headers, Timeout: timeout})
	if err != nil {
		a.close()
		return nil, err
	}
	a.files = files
	return a, nil
}

// writeFiles writes the adapter's configuration files relative to dir.
func (a *agentMCP) writeFiles(dir string) error {
	names := make([]string, 0, len(a.files.Files))
	for name := range a.files.Files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if name == "" || name != filepath.Base(name) {
			return fmt.Errorf("MCP adapter file %q must be a plain file name", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), a.files.Files[name], 0o600); err != nil {
			return err
		}
	}
	return nil
}

// arguments builds the CLI argv for the MCP mode: print arguments adjusted by
// the adapter, with the native session the continuation resumes.
func (a *agentMCP) arguments(executable string, printArgs []string, resume bool) []string {
	args := append([]string{executable}, a.files.ApplyArguments(printArgs)...)
	if resume {
		return append(args, "--session", a.session)
	}
	return append(args, "--session-id", a.session)
}

// mcpPrompt replaces the text protocol's frame instructions: the tools are
// native and the attempt ends with a terminal tool call.
func mcpPrompt(prompt string, options harness.CodexOptions) string {
	names := make([]string, 0, len(options.Tools))
	for _, tool := range options.Tools {
		names = append(names, "mcp__mitos__"+tool.Name)
	}
	text := prompt + "\n\nMitos coordinated CLI protocol (native tools)\nFollow the assignment in this workspace. Do not launch native subagents.\nThe Mitos tools are provided as native tools: " + strings.Join(names, ", ") + ". Call them as tools; their results arrive as tool results, never invented.\nFor an operator decision, call mcp__mitos__operator_question, or mcp__mitos__command_approval for one exact command line, and wait for the answer in the tool result.\nEnd the attempt by calling "
	if len(options.OutputSchema) > 0 {
		text += "mcp__mitos__plan_submit with the plan object, which must match the tool's input schema. A refused plan returns the reason as the tool result; correct it and submit again."
	} else {
		text += "mcp__mitos__result_submit, then mcp__mitos__report_submit with your final Markdown report. report_submit is refused until result_submit has succeeded. The attempt ends when report_submit succeeds."
	}
	return text + "\n"
}

// continuationInput tells a CLI that exited without a terminal tool call to
// continue its session and finish the attempt.
func (a *agentMCP) continuationInput() []byte {
	return []byte("Continue the same assignment in this conversation. Your process ended without completing the attempt. Finish the remaining work, call result_submit when the work is done, and end the attempt with report_submit (or plan_submit for the orchestrator).")
}

// run waits for the CLI, once continued if it exited without a terminal tool
// call. Stdout is only the execution log; nothing is parsed from it.
func (a *agentMCP) run(ctx context.Context, session compute.Session, next func() (compute.Session, error), preview io.Writer) ([]byte, error) {
	resumed := false
	for {
		<-session.Done()
		output, err := session.Result()
		a.logs.Write(output)
		if err != nil {
			return a.logs.Bytes(), err
		}
		if err = ctx.Err(); err != nil {
			return a.logs.Bytes(), err
		}
		if a.server.Finished() {
			return a.logs.Bytes(), nil
		}
		if resumed {
			return a.logs.Bytes(), errors.New(resultSubmissionMissing)
		}
		resumed = true
		fmt.Fprint(preview, "Mitos asked the coding agent to continue its session and end with the terminal tool.\n")
		var continuation compute.Session
		if continuation, err = next(); err != nil {
			return a.logs.Bytes(), err
		}
		session = continuation
	}
}

// piAttemptConfig builds the per-attempt Pi configuration directory for an
// unconfined run, the way piSandboxConfig builds the brokered one: the
// operator's provider, models and logins are kept, their other MCP servers
// and the rest of their agent directory are not. The MCP adapter's mcp.json
// is written into the returned directory by the caller.
func piAttemptConfig(server []string, base string) (string, string, error) {
	host := piConfigDir(server)
	config := filepath.Join(base, "home", ".pi", "agent")
	if err := os.MkdirAll(config, 0o700); err != nil {
		return "", "", err
	}
	for _, name := range []string{"settings.json", "models.json", "auth.json"} {
		body, err := os.ReadFile(filepath.Join(host, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		if err = os.WriteFile(filepath.Join(config, name), body, 0o600); err != nil {
			return "", "", err
		}
	}
	return config, "PI_CODING_AGENT_DIR=" + config, nil
}
