package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func mcpTestServer(t *testing.T) (*MCPServer, *httptest.Server, string) {
	t.Helper()
	server := NewMCPServer(MCPOptions{
		Tools: []DynamicTool{
			{Name: "result_get", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "result_submit", Description: "submit", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		ToolCall: func(name string, raw json.RawMessage) (any, error) {
			return map[string]string{"ok": name}, nil
		},
		Result: func(string) error { return nil },
		Decision: func(ctx context.Context, d MCPDecision) (string, error) {
			if d.Question != "" {
				return `{"answer":"42"}`, nil
			}
			return `{"approved":true}`, nil
		},
		Terminal:     true,
		AllowCommand: true,
		MaxTurns:     4,
	})
	token := NewMCPToken()
	httpServer := httptest.NewServer(server.StreamableHTTP(token))
	t.Cleanup(httpServer.Close)
	return server, httpServer, token
}

type mcpClient struct {
	t       *testing.T
	base    string
	token   string
	session string
}

func (c *mcpClient) call(t *testing.T, method string, params any, expectStatus int) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": fmt.Sprintf("%d", time.Now().UnixNano()), "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, _ := json.Marshal(body)
	request, err := http.NewRequest(http.MethodPost, c.base, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	if c.session != "" {
		request.Header.Set(mcpSessionHeader, c.session)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != expectStatus {
		text, _ := io.ReadAll(response.Body)
		t.Fatalf("%s: status %d: %s", method, response.StatusCode, text)
	}
	if session := response.Header.Get(mcpSessionHeader); session != "" {
		c.session = session
	}
	if expectStatus != http.StatusOK {
		return nil
	}
	if version := response.Header.Get(mcpVersionHeader); version != "" && !mcpVersionSupported(version) {
		t.Fatalf("unsupported version echoed: %s", version)
	}
	var answer map[string]any
	if err = json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	return answer
}

func (c *mcpClient) initialize(t *testing.T) {
	t.Helper()
	answer := c.call(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test"}}, http.StatusOK)
	if result, ok := answer["result"].(map[string]any); !ok || result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %v", answer)
	}
	if c.session == "" {
		t.Fatal("initialize issued no session")
	}
}

func (c *mcpClient) tool(t *testing.T, name string, arguments any) (string, bool) {
	t.Helper()
	answer := c.call(t, "tools/call", map[string]any{"name": name, "arguments": arguments}, http.StatusOK)
	result, ok := answer["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s: %v", name, answer)
	}
	content, _ := result["content"].([]any)
	text := ""
	if len(content) > 0 {
		if block, ok := content[0].(map[string]any); ok {
			text, _ = block["text"].(string)
		}
	}
	failed, _ := result["isError"].(bool)
	return text, failed
}

func TestMCPStreamableHTTPTransport(t *testing.T) {
	_, httpServer, token := mcpTestServer(t)
	client := &mcpClient{t: t, base: httpServer.URL, token: token}
	client.initialize(t)

	// A request without the session is refused, and an unknown session too.
	missing := &mcpClient{t: t, base: httpServer.URL, token: token}
	missing.call(t, "tools/list", nil, http.StatusNotFound)
	unknown := &mcpClient{t: t, base: httpServer.URL, token: token, session: "not-a-session"}
	unknown.call(t, "tools/list", nil, http.StatusNotFound)

	// A wrong or missing bearer token is refused.
	for _, authorization := range []string{"Bearer wrong", "", "Basic x"} {
		request, _ := http.NewRequest(http.MethodPost, httpServer.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		request.Header.Set("Content-Type", "application/json")
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("authorization %q accepted with %d", authorization, response.StatusCode)
		}
	}

	// An unsupported protocol version is refused; a supported one is echoed.
	request, _ := http.NewRequest(http.MethodPost, httpServer.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(mcpSessionHeader, client.session)
	request.Header.Set(mcpVersionHeader, "2024-10-07")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported version accepted with %d", response.StatusCode)
	}

	// Notifications are acknowledged with 202 and carry no body.
	notification, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	request, _ = http.NewRequest(http.MethodPost, httpServer.URL, bytes.NewReader(notification))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(mcpSessionHeader, client.session)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted || len(body) != 0 {
		t.Fatalf("notification: %d %s", response.StatusCode, body)
	}

	// Unknown methods get a JSON-RPC error, not a transport failure.
	answer := client.call(t, "resources/list", nil, http.StatusOK)
	if _, ok := answer["error"]; !ok {
		t.Fatalf("unknown method answered: %v", answer)
	}

	// GET offers no server-initiated stream; DELETE ends the session.
	request, _ = http.NewRequest(http.MethodGet, httpServer.URL, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET answered with %d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodDelete, httpServer.URL, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set(mcpSessionHeader, client.session)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("DELETE answered with %d", response.StatusCode)
	}
	client.call(t, "ping", nil, http.StatusNotFound)
}

func TestMCPTerminalToolsAndGates(t *testing.T) {
	server, httpServer, token := mcpTestServer(t)
	client := &mcpClient{t: t, base: httpServer.URL, token: token}
	client.initialize(t)
	answer := client.call(t, "tools/list", nil, http.StatusOK)
	result, _ := answer["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	names := []string{}
	for _, entry := range tools {
		if tool, ok := entry.(map[string]any); ok {
			names = append(names, tool["name"].(string))
		}
	}
	for _, expected := range []string{"result_get", "result_submit", MCPReportSubmit, MCPOperatorQuestion, MCPCommandApproval} {
		if !strings.Contains(strings.Join(names, ","), expected) {
			t.Fatalf("tools/list misses %s: %v", expected, names)
		}
	}
	if strings.Contains(strings.Join(names, ","), MCPPlanSubmit) {
		t.Fatal("worker attempt lists plan_submit")
	}

	// A question blocks through the decision path and returns the answer.
	if text, failed := client.tool(t, MCPOperatorQuestion, map[string]any{"question": "Meaning?"}); failed || text != `{"answer":"42"}` {
		t.Fatalf("operator_question: %q %v", text, failed)
	}
	// report_submit before result_submit is refused and correctable in-session.
	if _, failed := client.tool(t, MCPReportSubmit, map[string]any{"report": "done"}); !failed {
		t.Fatal("report accepted before result_submit")
	}
	if _, failed := client.tool(t, "result_submit", map[string]any{}); failed {
		t.Fatal("result_submit refused")
	}
	if text, failed := client.tool(t, MCPReportSubmit, map[string]any{"report": "The answer."}); failed || !strings.Contains(text, "complete") {
		t.Fatalf("report_submit: %q %v", text, failed)
	}
	if !server.Finished() {
		t.Fatal("server did not finish")
	}
	// Every call after the terminal one is refused.
	if _, failed := client.tool(t, "result_get", map[string]any{}); !failed {
		t.Fatal("call accepted after the attempt finished")
	}

	// Ungranted tools and empty reports are refused, not transport errors.
	strict := &mcpClient{t: t, base: httpServer.URL, token: token}
	strict.initialize(t)
	if _, failed := strict.tool(t, "artifact_read", map[string]any{}); !failed {
		t.Fatal("ungranted tool accepted")
	}
	if _, failed := strict.tool(t, MCPCommandApproval, map[string]any{"command": ""}); !failed {
		t.Fatal("empty approval accepted")
	}
}

func TestMCPTurnBudget(t *testing.T) {
	_, httpServer, token := mcpTestServer(t)
	client := &mcpClient{t: t, base: httpServer.URL, token: token}
	client.initialize(t)
	// MaxTurns is 4: three tool calls fit, the fourth must be the submission.
	for i := 0; i < 3; i++ {
		if _, failed := client.tool(t, "result_get", map[string]any{}); failed {
			t.Fatalf("call %d refused", i)
		}
	}
	if text, failed := client.tool(t, "result_get", map[string]any{}); !failed || !strings.Contains(text, "submission") {
		t.Fatalf("budget not reserved: %q %v", text, failed)
	}
	if _, failed := client.tool(t, MCPReportSubmit, map[string]any{"report": "late"}); !failed {
		t.Fatal("report accepted without result_submit")
	}
}

func TestMCPPlanSubmitValidatesTheSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["operations","complete"],"properties":{"operations":{"type":"array","items":{"type":"object","required":["kind"],"properties":{"kind":{"type":"string","enum":["add","accept"]}}}},"complete":{"type":"boolean"}}}`)
	server := NewMCPServer(MCPOptions{
		Tools:        []DynamicTool{{Name: "graph_read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolCall:     func(string, json.RawMessage) (any, error) { return nil, nil },
		Result:       func(string) error { return nil },
		Terminal:     true,
		OutputSchema: schema,
		MaxTurns:     3,
	})
	token := NewMCPToken()
	httpServer := httptest.NewServer(server.StreamableHTTP(token))
	t.Cleanup(httpServer.Close)
	client := &mcpClient{t: t, base: httpServer.URL, token: token}
	client.initialize(t)
	answer := client.call(t, "tools/list", nil, http.StatusOK)
	result, _ := answer["result"].(map[string]any)
	var listed map[string]any
	for _, entry := range result["tools"].([]any) {
		tool := entry.(map[string]any)
		if tool["name"] == MCPPlanSubmit {
			body, _ := json.Marshal(tool["inputSchema"])
			if json.Unmarshal(body, &listed) != nil {
				t.Fatal("plan_submit schema is unreadable")
			}
		}
	}
	if listed == nil || len(listed) != 4 || listed["type"] != "object" || listed["additionalProperties"] != false {
		t.Fatalf("plan_submit schema: %v", listed)
	}
	// A malformed plan is refused with its reasons and corrected in-session.
	if text, failed := client.tool(t, MCPPlanSubmit, map[string]any{"operations": []any{map[string]any{"kind": "explode"}}, "complete": "yes"}); !failed || !strings.Contains(text, "kind") || !strings.Contains(text, "complete") {
		t.Fatalf("invalid plan accepted: %q", text)
	}
	if server.Finished() {
		t.Fatal("refused plan finished the attempt")
	}
	if text, failed := client.tool(t, MCPPlanSubmit, map[string]any{"operations": []any{map[string]any{"kind": "add"}}, "complete": true}); failed || !strings.Contains(text, "complete") {
		t.Fatalf("valid plan refused: %q %v", text, failed)
	}
	if !server.Finished() {
		t.Fatal("plan submission did not finish the attempt")
	}
}

func TestMCPReportSubmitSchemaNullPlanVariants(t *testing.T) {
	// anyOf with a null branch, as pointer fields produce.
	schema := json.RawMessage(`{"type":"object","required":["value"],"properties":{"value":{"anyOf":[{"type":"string"},{"type":"null"}]}}}`)
	if problems := schemaProblems(schema, map[string]any{"value": nil}); len(problems) != 0 {
		t.Fatal(problems)
	}
	if problems := schemaProblems(schema, map[string]any{"value": "text"}); len(problems) != 0 {
		t.Fatal(problems)
	}
	if problems := schemaProblems(schema, map[string]any{"value": 3.0}); len(problems) == 0 {
		t.Fatal("number accepted as string")
	}
}

// Concurrent tool calls arrive on separate HTTP handler goroutines; activity
// records must be serialized instead of racing on the shared writer.
func TestMCPConcurrentToolCallsSerializeActivity(t *testing.T) {
	var activity bytes.Buffer
	server := NewMCPServer(MCPOptions{
		Tools:    []DynamicTool{{Name: "result_get", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolCall: func(string, json.RawMessage) (any, error) { return map[string]int{"n": 1}, nil },
		Activity: &activity,
		MaxTurns: 0,
	})
	token := NewMCPToken()
	httpServer := httptest.NewServer(server.StreamableHTTP(token))
	t.Cleanup(httpServer.Close)
	client := &mcpClient{t: t, base: httpServer.URL, token: token}
	client.initialize(t)
	const calls = 16
	done := make(chan struct{}, calls)
	for i := 0; i < calls; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			if text, failed := client.tool(t, "result_get", map[string]any{}); failed || text == "" {
				t.Errorf("concurrent call: %q %v", text, failed)
			}
		}()
	}
	for i := 0; i < calls; i++ {
		<-done
	}
	records := strings.Split(activity.String(), "\n")
	completed := 0
	for _, line := range records {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			t.Fatalf("interleaved activity record: %q", line)
		}
		if record["status"] == "completed" {
			completed++
		}
	}
	if completed != calls {
		t.Fatalf("completed records: %d of %d", completed, calls)
	}
}
