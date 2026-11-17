package structuredworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"mitos/internal/compute"
	"mitos/internal/harness"
	"mitos/internal/resourcepolicy"
)

var ErrDormant = errors.New("structured_worker_dormant")

type Executor struct {
	Wait        func(context.Context, json.RawMessage) (Receipt, error)
	Action      func(context.Context, string, json.RawMessage) (Receipt, error)
	Provider    compute.Provider
	Executable  string
	Environment []string
	Base        string
	Scoped      bool
	// EgressEndpoint is the mediated model-transport socket (unix:// path)
	// for scoped workers. When set, the sandbox runs in an isolated network
	// namespace whose only egress is the allowlisted relay.
	EgressEndpoint string
	Preview        io.Writer
	Activity       io.Writer
}

// Execute is the only native structured execution loop, shared by the local
// adapter and mitos worker. It never receives application or action credentials.
func (e Executor) Execute(ctx context.Context, assignment Context) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(assignment.BudgetSeconds)*time.Second)
	defer cancel()
	scratch := filepath.Join(e.Base, "workspace")
	if err := os.MkdirAll(scratch, 0700); err != nil {
		return Result{}, err
	}
	env := e.Environment
	var err error
	if e.Scoped {
		env, err = ApplicationEnvironment(e.Base, env)
		if err != nil {
			return Result{}, err
		}
		// Route the runtime's own HTTPS through the in-sandbox loop forwarder;
		// the host relay admits only the model endpoints on its allowlist.
		if e.EgressEndpoint != "" {
			env = append(env,
				"HTTPS_PROXY=http://"+compute.EgressLoopAddr,
				"HTTP_PROXY=http://"+compute.EgressLoopAddr,
				"ALL_PROXY=http://"+compute.EgressLoopAddr,
				"NO_PROXY=localhost,127.0.0.1",
			)
		}
	}
	caps := e.Provider.Capabilities()
	if e.Scoped && (!caps.WorkspaceConfined || caps.Egress != "unrestricted") {
		return Result{}, errors.New("application_execution_unavailable: confined unrestricted model transport required")
	}
	request := compute.Request{Resources: resourcepolicy.Agent{}}
	if e.Scoped {
		request.NetMode = compute.NetOff
	}
	launch, err := compute.Compile(request, map[string]compute.Capabilities{"local": caps}, !e.Scoped)
	if err != nil {
		return Result{}, err
	}
	if e.Scoped {
		launch.Posture.NetMode = compute.NetUnrestricted
		if e.EgressEndpoint != "" {
			launch.Posture.BrokerEndpoint = e.EgressEndpoint
		}
	}
	launch.RunID = assignment.RunID
	launch.CapabilityRef = assignment.Definition.ID
	launch.Base = e.Base
	launch.Workspace.Directory = scratch
	launch.Posture.Deadline, _ = ctx.Deadline()
	handle, err := e.Provider.Prepare(ctx, launch)
	if err != nil {
		return Result{}, err
	}
	var activity bytes.Buffer
	preview := e.Preview
	if preview == nil {
		preview = io.Discard
	}
	activityPreview := e.Activity
	if activityPreview == nil {
		activityPreview = io.Discard
	}
	final := ""
	options := harness.CodexOptions{Headless: true, Model: assignment.Model, Sandbox: "read-only", MaxTurns: assignment.MaxTurns, OutputSchema: assignment.ResultSchema, Result: func(text string) error { final = text; return nil }, Activity: io.MultiWriter(&activity, activityPreview)}
	if len(assignment.Actions) > 0 && e.Action != nil {
		schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["target","key","payload"],"properties":{"target":{"type":"string"},"key":{"type":"string"},"payload":{"type":"object","additionalProperties":true}}}`)
		reconcileSchema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["target","key"],"properties":{"target":{"type":"string"},"key":{"type":"string"}}}`)
		targets, _ := json.Marshal(assignment.Actions)
		options.Tools = []harness.DynamicTool{{Type: "function", Name: "mitos_action", Description: "Request an external operation only on an admitted target. Schemas: " + string(targets) + ". A prepared or unknown outcome requires mitos_reconcile with the same target and key. Never invent a different key while reconciliation is required.", InputSchema: schema}, {Type: "function", Name: "mitos_reconcile", Description: "Query the original external operation identity; never dispatch an ambiguous action again.", InputSchema: reconcileSchema}}
	}
	var dormant atomic.Bool
	if len(assignment.CheckpointSchema) > 0 && e.Wait != nil {
		schema, _ := json.Marshal(map[string]any{"type": "object", "additionalProperties": false, "required": []string{"cursor", "checkpoint"}, "properties": map[string]any{"cursor": map[string]string{"type": "integer"}, "checkpoint": assignment.CheckpointSchema}})
		options.Tools = append(options.Tools, harness.DynamicTool{Type: "function", Name: "mitos_wait", Description: "Save a portable checkpoint and consumed per-Task input cursor. If inputs already exist after the cursor, process the returned bounded page and continue; otherwise release this attempt and resume on a later input. Never put model/session/framework state in the checkpoint.", InputSchema: schema})
	}
	options.ToolCall = func(name string, raw json.RawMessage) (any, error) {
		if name == "mitos_wait" && e.Wait != nil {
			receipt, err := e.Wait(ctx, raw)
			if err == nil && receipt.State == "dormant" {
				dormant.Store(true)
				cancel()
			}
			return receipt, err
		}
		kind := "action"
		if name == "mitos_reconcile" {
			kind = "reconcile"
		} else if name != "mitos_action" {
			return nil, errors.New("unknown structured tool")
		}
		if e.Action == nil {
			return nil, errors.New("action gateway unavailable")
		}
		return e.Action(ctx, kind, raw)
	}

	session, err := e.Provider.Start(ctx, handle, compute.Command{Argv: append([]string{e.Executable}, harness.CodexArguments()...), Directory: scratch, NativeEnv: env, Interact: func(ctx context.Context, stream io.ReadWriteCloser, output io.Writer) error {
		return harness.RunCodex(ctx, deniedBroker{}, stream, strings.NewReader(""), io.MultiWriter(output, preview), scratch, assignment.Prompt, options)
	}})
	if err != nil {
		return Result{}, err
	}
	<-session.Done()
	output, runErr := session.Result()
	stopCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err = e.Provider.Stop(stopCtx, session); err != nil {
		return Result{}, err
	}
	if dormant.Load() {
		return Result{}, ErrDormant
	}
	// Captured output and activity stay available even when the attempt fails:
	// they are the evidence needed to diagnose timeouts and crashes.
	evidence := []Artifact{{Name: "output.txt", Data: output}, {Name: "activity.jsonl", Data: activity.Bytes()}}
	if runErr != nil {
		return Result{Artifacts: evidence}, fmt.Errorf("Codex stopped: %w", runErr)
	}
	result := Result{Value: json.RawMessage(final), Artifacts: evidence}
	for _, name := range []string{"authority.json", "context.json"} {
		if raw := assignment.Artifacts[name]; raw != nil {
			result.Artifacts = append(result.Artifacts, Artifact{Name: name, Data: raw})
		}
	}
	return result, nil
}

type deniedBroker struct{}

func (deniedBroker) Submit(context.Context, json.RawMessage) (harness.Delivery, error) {
	return harness.Delivery{}, errors.New("headless_external_action_requires_gateway")
}
func (deniedBroker) Get(context.Context, string) (harness.Delivery, error) {
	return harness.Delivery{}, errors.New("headless_external_action_requires_gateway")
}
func (deniedBroker) Deliver(context.Context, string, string, string) error {
	return errors.New("headless_external_action_requires_gateway")
}
func (deniedBroker) Invalidate(context.Context, string) error { return nil }
