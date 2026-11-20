package harness

// The Mitos tools served over the Model Context Protocol. One JSON-RPC
// handler serves two transports: Claude Code's SDK stdio control protocol and
// the streamable HTTP transport print CLIs use to receive the tools as native
// tools instead of prose envelopes (issue #101).
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

// Names of the MCP tools that replace the print protocol's text-frame
// envelopes. They are offered only to print CLIs (MCPOptions.Terminal).
const (
	MCPReportSubmit       = "report_submit"
	MCPPlanSubmit         = "plan_submit"
	MCPOperatorQuestion   = "operator_question"
	MCPCommandApproval    = "command_approval"
	mcpResultSubmit       = "result_submit"
	mcpServerName         = "mitos"
	mcpArgumentsByteLimit = 1 << 20
	mcpResponseByteLimit  = 64 << 10
)

// MCPProtocolVersions are the MCP revisions this server speaks, newest first.
var MCPProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// MCPDecision is one operator interaction requested through the MCP tools.
// Exactly one field is set: a question or a proposed command awaiting approval.
type MCPDecision struct {
	Question string
	Command  string
}

// MCPOptions configure one attempt's MCP server. Tools and ToolCall mirror
// the native-protocol handoff; Terminal adds the submission and decision
// tools that carry what the text protocol asked for in reply frames.
type MCPOptions struct {
	Tools    []DynamicTool
	ToolCall func(string, json.RawMessage) (any, error)
	// Terminal lists report_submit/plan_submit and the decision tools.
	Terminal bool
	// Result delivers the accepted report (workers) or plan (orchestrators).
	Result func(string) error
	// Decision blocks until the operator answers an MCPDecision.
	Decision func(context.Context, MCPDecision) (string, error)
	// OutputSchema marks an orchestrator attempt: plan_submit replaces
	// report_submit and carries this schema.
	OutputSchema json.RawMessage
	// PlanRules adds policy checks for plan_submit beyond the schema, so a
	// plan Mitos would refuse after the attempt is refused in-session and
	// corrected without another process. Only used with OutputSchema.
	PlanRules func(json.RawMessage) []string
	// AllowCommand lists command_approval, which needs the command.run grant.
	AllowCommand bool
	// MaxTurns bounds every Mitos tool call plus the submission. Zero is
	// unbounded.
	MaxTurns int
	// Activity receives public tool records, as the native harnesses emit.
	Activity io.Writer
}

// MCPServer answers MCP JSON-RPC messages for one attempt.
type MCPServer struct {
	options MCPOptions

	mu         sync.Mutex
	calls      int
	submitted  bool
	finished   bool
	callSerial int
	// activityMu serializes writes to the shared activity writer: tool calls
	// arrive on concurrent HTTP handler goroutines, and the host-side writer
	// is a plain buffer.
	activityMu sync.Mutex
}

func NewMCPServer(options MCPOptions) *MCPServer {
	return &MCPServer{options: options}
}

// Finished reports that a terminal tool succeeded and ended the attempt.
func (s *MCPServer) Finished() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

// mcpError is a protocol-level refusal. toolResult, when set, is the
// tools/call result that transports return instead of a bare JSON-RPC error,
// so the model can correct the call inside its own session.
type mcpError struct {
	code    int
	message string
	// toolResult carries an isError result for refused tools/call requests.
	toolResult map[string]any
}

func (e *mcpError) Error() string { return e.message }

func mcpRefusal(format string, args ...any) *mcpError {
	return &mcpError{code: -32000, message: fmt.Sprintf(format, args...), toolResult: mcpToolText(fmt.Sprintf(format, args...), true)}
}

func mcpToolText(text string, failed bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": failed}
}

// mcpMessage is one answered JSON-RPC message.
type mcpMessage struct {
	notification bool
	id           json.RawMessage
	result       any
	err          *mcpError
}

func (m mcpMessage) response() json.RawMessage {
	if m.notification {
		return nil
	}
	body := map[string]any{"jsonrpc": "2.0", "id": m.id}
	if m.err != nil {
		if m.err.toolResult != nil {
			body["result"] = m.err.toolResult
		} else {
			body["error"] = map[string]any{"code": m.err.code, "message": m.err.message}
		}
	} else {
		body["result"] = m.result
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return json.RawMessage(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`)
	}
	return raw
}

// message answers one JSON-RPC message. Notifications return with
// notification set and no response body.
func (s *MCPServer) message(ctx context.Context, raw json.RawMessage) mcpMessage {
	var request struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &request); err != nil || request.Method == "" {
		return mcpMessage{err: &mcpError{code: -32600, message: "invalid MCP request"}}
	}
	if request.ID == nil || string(request.ID) == "null" {
		// Notifications carry no reply; initialized is the only one expected.
		return mcpMessage{notification: true}
	}
	switch request.Method {
	case "initialize":
		return mcpMessage{id: request.ID, result: s.initialize(request.Params)}
	case "notifications/initialized", "ping":
		return mcpMessage{id: request.ID, result: map[string]any{}}
	case "tools/list":
		return mcpMessage{id: request.ID, result: map[string]any{"tools": s.toolList()}}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
			return mcpMessage{id: request.ID, err: &mcpError{code: -32602, message: "invalid tools/call parameters"}}
		}
		result, err := s.callTool(ctx, params.Name, params.Arguments)
		if err != nil {
			var refusal *mcpError
			if errors.As(err, &refusal) {
				if result != nil {
					refusal.toolResult = result
				}
				return mcpMessage{id: request.ID, err: refusal}
			}
			// A failed tool call is a result the model can react to, not a
			// protocol error.
			return mcpMessage{id: request.ID, result: result}
		}
		return mcpMessage{id: request.ID, result: result}
	default:
		return mcpMessage{id: request.ID, err: &mcpError{code: -32601, message: "unsupported MCP method " + request.Method}}
	}
}

func (s *MCPServer) initialize(params json.RawMessage) map[string]any {
	version := MCPProtocolVersions[0]
	var request struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(params, &request) == nil && slices.Contains(MCPProtocolVersions, request.ProtocolVersion) {
		version = request.ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": mcpServerName, "version": "1"},
	}
}

func (s *MCPServer) toolList() []map[string]any {
	tools := []map[string]any{}
	for _, tool := range s.options.Tools {
		tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "inputSchema": tool.InputSchema})
	}
	if !s.options.Terminal {
		return tools
	}
	if len(s.options.OutputSchema) == 0 {
		tools = append(tools, map[string]any{
			"name":        MCPReportSubmit,
			"description": "End the attempt by submitting your final Markdown report. Refused until result_submit has succeeded; call that first. The attempt is complete once this call succeeds.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"report"}, "properties": map[string]any{"report": map[string]any{"type": "string", "description": "The final human-readable Markdown report."}}},
		})
	} else {
		tools = append(tools, map[string]any{
			"name":        MCPPlanSubmit,
			"description": "End the orchestrator attempt by submitting the plan object, which must match the input schema. The attempt is complete once this call succeeds.",
			"inputSchema": json.RawMessage(s.options.OutputSchema),
		})
	}
	tools = append(tools, map[string]any{
		"name":        MCPOperatorQuestion,
		"description": "Ask the operator one question and wait; the call blocks until the answer arrives as the tool result.",
		"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"question"}, "properties": map[string]any{"question": map[string]any{"type": "string", "description": "One concrete question."}}},
	})
	if s.options.AllowCommand {
		tools = append(tools, map[string]any{
			"name":        MCPCommandApproval,
			"description": "Request approval for one exact command line and wait; the call blocks until the operator decision arrives as the tool result.",
			"inputSchema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"command"}, "properties": map[string]any{"command": map[string]any{"type": "string", "description": "The exact proposed command line."}}},
		})
	}
	return tools
}

// callTool executes one tools/call. A *mcpError marks a refusal: nothing was
// applied and the model may correct the call. Other errors are returned as a
// failed result with no error, like a native tool failure.
func (s *MCPServer) callTool(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, *mcpError) {
	if len(arguments) > mcpArgumentsByteLimit {
		return nil, mcpRefusal("tool arguments exceed the %d-byte limit", mcpArgumentsByteLimit)
	}
	if s.Finished() {
		return nil, mcpRefusal("this attempt already ended with a submission")
	}
	if s.options.Terminal {
		switch name {
		case MCPReportSubmit, MCPPlanSubmit:
			return s.terminalSubmit(ctx, name, arguments)
		case MCPOperatorQuestion:
			return s.decision(ctx, name, MCPDecision{Question: stringArgument(arguments, "question")})
		case MCPCommandApproval:
			if !s.options.AllowCommand {
				return nil, mcpRefusal("command_approval requires the command.run grant")
			}
			return s.decision(ctx, name, MCPDecision{Command: stringArgument(arguments, "command")})
		}
	}
	if !slices.ContainsFunc(s.options.Tools, func(t DynamicTool) bool { return t.Name == name }) {
		return nil, mcpRefusal("tool %s is not granted to this attempt", name)
	}
	if s.options.ToolCall == nil {
		return nil, mcpRefusal("tool %s is not granted to this attempt", name)
	}
	if err := ctx.Err(); err != nil {
		return nil, mcpRefusal("attempt context ended: %v", err)
	}
	if err := s.chargeTurn(false); err != nil {
		return nil, err
	}
	id := s.recordActivity(name, "running")
	value, err := s.options.ToolCall(name, arguments)
	s.recordActivityID(id, name, "completed")
	if err == nil && name == mcpResultSubmit {
		s.mu.Lock()
		s.submitted = true
		s.mu.Unlock()
	}
	var encoded []byte
	if err != nil {
		encoded = []byte(err.Error())
	} else {
		encoded, _ = json.Marshal(value)
	}
	if len(encoded) > mcpResponseByteLimit {
		return mcpToolText(fmt.Sprintf("response_limit: the tool response exceeds %d bytes", mcpResponseByteLimit), true), nil
	}
	return mcpToolText(string(encoded), err != nil), nil
}

// chargeTurn consumes one step of the MaxTurns budget. Non-terminal calls
// must leave the last step for the submission.
func (s *MCPServer) chargeTurn(terminal bool) *mcpError {
	if s.options.MaxTurns <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if terminal {
		if s.calls >= s.options.MaxTurns {
			return mcpRefusal("turn budget of %d Mitos calls is exhausted", s.options.MaxTurns)
		}
	} else if s.calls+1 >= s.options.MaxTurns {
		return mcpRefusal("the turn budget reserves its last step for the submission; call %s or %s now", MCPReportSubmit, MCPPlanSubmit)
	}
	s.calls++
	return nil
}

func (s *MCPServer) terminalSubmit(ctx context.Context, name string, arguments json.RawMessage) (map[string]any, *mcpError) {
	if name == MCPReportSubmit && len(s.options.OutputSchema) > 0 {
		return nil, mcpRefusal("tool %s is not granted to this attempt", name)
	}
	if name == MCPPlanSubmit && len(s.options.OutputSchema) == 0 {
		return nil, mcpRefusal("tool %s is not granted to this attempt", name)
	}
	if s.options.Result == nil {
		return nil, mcpRefusal("tool %s is not granted to this attempt", name)
	}
	if name == MCPReportSubmit {
		report := stringArgument(arguments, "report")
		if strings.TrimSpace(report) == "" {
			return nil, mcpRefusal("report_submit needs a nonempty report string")
		}
		if !s.hasSubmitted() && slices.ContainsFunc(s.options.Tools, func(t DynamicTool) bool { return t.Name == mcpResultSubmit }) {
			return nil, mcpRefusal("report_submit was refused: call result_submit successfully first, then submit the report")
		}
		if err := ctx.Err(); err != nil {
			return nil, mcpRefusal("attempt context ended: %v", err)
		}
		if err := s.chargeTurn(true); err != nil {
			return nil, err
		}
		if err := s.options.Result(report); err != nil {
			return mcpToolText("report_submit failed: "+err.Error(), true), nil
		}
		s.finish()
		s.writeActivity(map[string]any{"kind": "message", "id": "mitos-report", "text": report})
		return mcpToolText("Report submitted. The attempt is complete; finish your message and exit.", false), nil
	}
	var plan any
	if err := json.Unmarshal(arguments, &plan); err != nil {
		return nil, mcpRefusal("plan_submit needs a JSON object matching the plan schema")
	}
	if problems := schemaProblems(json.RawMessage(s.options.OutputSchema), plan); len(problems) > 0 {
		return nil, mcpRefusal("plan_submit was refused, the plan does not match the schema: %s", strings.Join(problems, "; "))
	}
	if s.options.PlanRules != nil {
		if problems := s.options.PlanRules(arguments); len(problems) > 0 {
			return nil, mcpRefusal("plan_submit was refused: %s", strings.Join(problems, "; "))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, mcpRefusal("attempt context ended: %v", err)
	}
	if err := s.chargeTurn(true); err != nil {
		return nil, err
	}
	if err := s.options.Result(string(arguments)); err != nil {
		return mcpToolText("plan_submit failed: "+err.Error(), true), nil
	}
	s.finish()
	return mcpToolText("Plan submitted. The attempt is complete; finish your message and exit.", false), nil
}

func (s *MCPServer) decision(ctx context.Context, name string, d MCPDecision) (map[string]any, *mcpError) {
	if s.options.Decision == nil {
		return nil, mcpRefusal("tool %s is not granted to this attempt", name)
	}
	field := "question"
	if d.Question == "" {
		field = "command"
	}
	if strings.TrimSpace(d.Question) == "" && strings.TrimSpace(d.Command) == "" {
		return nil, mcpRefusal("%s needs a nonempty %s string", name, field)
	}
	if d.Question != "" && d.Command != "" {
		return nil, mcpRefusal("%s carries both a question and a command", name)
	}
	if err := s.chargeTurn(false); err != nil {
		return nil, err
	}
	id := s.recordActivity(name, "running")
	answer, err := s.options.Decision(ctx, d)
	s.recordActivityID(id, name, "completed")
	if err != nil {
		return mcpToolText(name+" failed: "+err.Error(), true), nil
	}
	return mcpToolText(answer, false), nil
}

func (s *MCPServer) hasSubmitted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitted
}

func (s *MCPServer) finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
}

func (s *MCPServer) recordActivity(tool, status string) string {
	s.mu.Lock()
	s.callSerial++
	id := fmt.Sprintf("mcp-%d", s.callSerial)
	s.mu.Unlock()
	s.writeActivity(map[string]any{"kind": "tool", "id": id, "tool": tool, "status": status})
	return id
}

func (s *MCPServer) recordActivityID(id, tool, status string) {
	s.writeActivity(map[string]any{"kind": "tool", "id": id, "tool": tool, "status": status})
}

// writeActivity renders one public record under the activity lock, so
// concurrent tool calls cannot interleave writes.
func (s *MCPServer) writeActivity(value map[string]any) {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	writeActivityRecord(s.options.Activity, value)
}

func stringArgument(raw json.RawMessage, name string) string {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	text, _ := value[name].(string)
	return text
}

// schemaProblems validates a decoded JSON value against the JSON Schema
// subset Mitos itself generates for structured output: types, enums, anyOf,
// object properties with required and additionalProperties, and array items.
func schemaProblems(schema json.RawMessage, value any) []string {
	var decoded map[string]any
	if json.Unmarshal(schema, &decoded) != nil {
		return []string{"the schema is unreadable"}
	}
	return collectSchemaProblems(decoded, value, "$")
}

func collectSchemaProblems(schema map[string]any, value any, path string) []string {
	problems := []string{}
	if variants, ok := schema["anyOf"].([]any); ok {
		var branches []string
		for _, variant := range variants {
			branch, ok := variant.(map[string]any)
			if !ok {
				continue
			}
			if failed := collectSchemaProblems(branch, value, path); len(failed) == 0 {
				return nil
			} else if len(branches) < 2 {
				branches = append(branches, strings.Join(failed, ", "))
			}
		}
		return []string{path + " matches none of the allowed variants (" + strings.Join(branches, " / ") + ")"}
	}
	if expected, ok := schema["type"].(string); ok {
		if !schemaTypeMatches(expected, value) {
			problems = append(problems, fmt.Sprintf("%s must be %s", path, expected))
		}
	}
	if options, ok := schema["enum"].([]any); ok {
		found := false
		for _, option := range options {
			if fmt.Sprintf("%v", option) == fmt.Sprintf("%v", value) {
				found = true
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("%s must be one of %v", path, options))
		}
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		if object, ok := value.(map[string]any); ok {
			for _, name := range schemaRequired(schema) {
				if _, present := object[name]; !present {
					problems = append(problems, path+" misses the required field "+name)
				}
			}
			if additional, restricted := schema["additionalProperties"].(bool); restricted && !additional {
				for name := range object {
					if _, known := properties[name]; !known {
						problems = append(problems, path+" has the unexpected field "+name)
					}
				}
			}
			for name, field := range properties {
				if fieldSchema, ok := field.(map[string]any); ok {
					if fieldvalue, present := object[name]; present {
						problems = append(problems, collectSchemaProblems(fieldSchema, fieldvalue, path+"."+name)...)
					}
				}
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if list, ok := value.([]any); ok {
			for i, entry := range list {
				problems = append(problems, collectSchemaProblems(items, entry, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return problems
}

func schemaRequired(schema map[string]any) []string {
	names := []string{}
	if required, ok := schema["required"].([]any); ok {
		for _, name := range required {
			if text, ok := name.(string); ok {
				names = append(names, text)
			}
		}
	}
	return names
}

func schemaTypeMatches(expected string, value any) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "integer":
		number, ok := value.(float64)
		return ok && number == float64(int64(number))
	case "number":
		_, ok := value.(float64)
		return ok
	}
	return true
}
