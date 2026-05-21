const std = @import("std");
const adapters = @import("../adapters/types.zig");
const approvals = @import("../approvals/store.zig");
const policy = @import("../policy/types.zig");
const policy_engine = @import("../policy/engine.zig");
const redaction = @import("../redaction/redactor.zig");
const report = @import("../report/generator.zig");
const runtime = @import("types.zig");
const sandbox = @import("../sandbox/backend.zig");
const trace_file = @import("../trace/file_store.zig");
const trace_types = @import("../trace/types.zig");

pub const LifecycleError = error{
    InvalidTransition,
};

pub const RunLifecycle = struct {
    state: runtime.RunState = .created,

    pub fn prepare(self: *RunLifecycle, reason: ?[]const u8) LifecycleError!runtime.RunStateChanged {
        return self.transition(.preparing_workspace, reason);
    }

    pub fn startAdapter(self: *RunLifecycle, reason: ?[]const u8) LifecycleError!runtime.RunStateChanged {
        return self.transition(.starting_adapter, reason);
    }

    pub fn markRunning(self: *RunLifecycle, reason: ?[]const u8) LifecycleError!runtime.RunStateChanged {
        return self.transition(.running, reason);
    }

    pub fn finalize(self: *RunLifecycle, reason: ?[]const u8) LifecycleError!runtime.RunStateChanged {
        return self.transition(.finalizing, reason);
    }

    pub fn complete(self: *RunLifecycle, reason: ?[]const u8) LifecycleError!runtime.RunStateChanged {
        return self.transition(.completed, reason);
    }

    pub fn fail(self: *RunLifecycle, reason: ?[]const u8) LifecycleError!runtime.RunStateChanged {
        return self.transition(.failed, reason);
    }

    pub fn cancel(self: *RunLifecycle, reason: ?[]const u8) LifecycleError!runtime.RunStateChanged {
        return self.transition(.cancelled, reason);
    }

    fn transition(
        self: *RunLifecycle,
        to: runtime.RunState,
        reason: ?[]const u8,
    ) LifecycleError!runtime.RunStateChanged {
        const from = self.state;
        if (!isValidTransition(from, to)) return error.InvalidTransition;
        self.state = to;
        return .{ .from = from, .to = to, .reason = reason };
    }
};

pub const RuntimeEventSink = struct {
    context: *anyopaque,
    emit: *const fn (*anyopaque, runtime.RuntimeEventRecord) anyerror!void,
};

pub const EventFanout = struct {
    trace: *trace_file.FileTrace,
    listeners: []const RuntimeEventSink = &.{},

    pub fn emit(
        self: *EventFanout,
        timestamp_ms: runtime.TimestampMs,
        event: runtime.RuntimeEvent,
    ) !runtime.RuntimeEventRecord {
        const record = try self.trace.appendEvent(timestamp_ms, event);
        for (self.listeners) |listener| {
            try listener.emit(listener.context, record);
        }
        return record;
    }
};

pub const FakeRunOutcome = enum {
    completed,
    failed,
    cancelled,
};

pub const FakeRunOptions = struct {
    task: []const u8,
    run_id: []const u8 = "01HFAKE",
    provider: []const u8 = "fake",
    adapter: []const u8 = "fake-headless",
    outcome: FakeRunOutcome = .completed,
    final_output: []const u8 = "Fake run completed.",
    error_message: []const u8 = "fake run failed",
    diff_patch: []const u8 = "",
    redactor: redaction.Redactor = .{},
    policy_config: policy_engine.PolicyConfig = .{
        .default_effect = .ask,
        .default_reason = "fake run requires approval",
    },
    base_timestamp_ms: runtime.TimestampMs = 1,
};

pub const FakeRunResult = struct {
    run_id: runtime.RunId,
    relative_path: []u8,
    report_path: []const u8,
    final_state: runtime.RunState = .completed,

    pub fn deinit(self: FakeRunResult, allocator: std.mem.Allocator) void {
        allocator.free(self.relative_path);
    }
};

pub const AdapterRunOptions = struct {
    task: adapters.Task,
    run_id: []const u8,
    workspace_host_path: []const u8,
    workspace_container_path: ?[]const u8 = null,
    provider: ?[]const u8 = null,
    redactor: redaction.Redactor = .{},
    base_timestamp_ms: runtime.TimestampMs = 1,
    env: []const adapters.EnvVar = &.{},
    listeners: []const RuntimeEventSink = &.{},
    sandbox_event_stream: ?*sandbox.SandboxEventStream = null,
    sandbox_backend: ?AdapterSandboxOptions = null,
};

pub const AdapterSandboxOptions = struct {
    backend: *sandbox.SandboxBackend,
    launch: sandbox.SandboxLaunchConfig,
};

pub const AdapterRunResult = struct {
    run_id: runtime.RunId,
    relative_path: []u8,
    report_path: []const u8,
    adapter_status: adapters.AdapterRunStatus,
    final_state: runtime.RunState,

    pub fn deinit(self: AdapterRunResult, allocator: std.mem.Allocator) void {
        allocator.free(self.relative_path);
    }
};

const AdapterEventBridge = struct {
    allocator: std.mem.Allocator,
    fanout: *EventFanout,
    redactor: redaction.Redactor,
    timestamp_ms: *runtime.TimestampMs,
    trace: *trace_file.FileTrace,

    fn emit(context: *anyopaque, event: adapters.AdapterEvent) anyerror!void {
        const self: *AdapterEventBridge = @ptrCast(@alignCast(context));
        try self.emitEvent(event);
    }

    fn emitEvent(self: *AdapterEventBridge, event: adapters.AdapterEvent) !void {
        switch (event) {
            .process_spawned => |process| try self.emitRuntime(.{ .process_spawned = process }),
            .process_exited => |process| try self.emitRuntime(.{ .process_exited = process }),
            .prompt_detected => |prompt| {
                const redacted = try self.redactor.redactAlloc(
                    self.allocator,
                    .provider_event,
                    prompt.text,
                );
                defer self.allocator.free(redacted);
                try self.emitRuntime(.{ .prompt_detected = .{
                    .prompt_kind = prompt.prompt_kind,
                    .text = redacted,
                    .risk = prompt.risk,
                } });
            },
            .stdout => |bytes| try self.emitStream(.stdout, bytes),
            .stderr => |bytes| try self.emitStream(.stderr, bytes),
            .structured => |bytes| try self.emitStream(.structured, bytes),
            .terminal_bytes => |bytes| {
                const redacted = try self.redactor.redactAlloc(
                    self.allocator,
                    .terminal_text,
                    bytes,
                );
                defer self.allocator.free(redacted);
                try self.trace.appendJsonl(.terminal_raw, bytes);
                try self.trace.appendJsonl(.terminal_redacted, redacted);
                try self.emitRuntime(.{ .adapter_output = .{
                    .stream = .terminal,
                    .bytes = redacted,
                } });
            },
            .final_output => |bytes| {
                const redacted = try self.redactor.redactAlloc(
                    self.allocator,
                    .provider_event,
                    bytes,
                );
                defer self.allocator.free(redacted);
                _ = try self.trace.writeArtifact(.final_output, redacted);
                try self.emitRuntime(.{ .final_output = .{ .text = redacted } });
            },
            .error_message => |message| {
                const redacted = try self.redactor.redactAlloc(
                    self.allocator,
                    .provider_event,
                    message,
                );
                defer self.allocator.free(redacted);
                try self.emitRuntime(.{ .error_reported = .{
                    .message = redacted,
                    .component = "adapter",
                } });
            },
        }
    }

    fn emitStream(
        self: *AdapterEventBridge,
        stream: runtime.StreamName,
        bytes: []const u8,
    ) !void {
        const redacted = try self.redactor.redactAlloc(
            self.allocator,
            .provider_event,
            bytes,
        );
        defer self.allocator.free(redacted);
        try self.emitRuntime(.{ .adapter_output = .{
            .stream = stream,
            .bytes = redacted,
        } });
    }

    fn emitRuntime(self: *AdapterEventBridge, event: runtime.RuntimeEvent) !void {
        _ = try self.fanout.emit(self.timestamp_ms.*, event);
        self.timestamp_ms.* += 1;
    }
};

pub fn runAdapter(
    allocator: std.mem.Allocator,
    base_dir: std.fs.Dir,
    adapter: *adapters.AgentAdapter,
    options: AdapterRunOptions,
) !AdapterRunResult {
    const run_id = try runtime.RunId.init(options.run_id);
    const provider = options.provider orelse adapter.provider.value;
    const redacted_task = try options.redactor.redactAlloc(
        allocator,
        .provider_event,
        options.task.text,
    );
    defer allocator.free(redacted_task);

    var trace = try trace_file.FileTrace.create(allocator, base_dir, run_id, .{
        .created_at_ms = options.base_timestamp_ms,
        .provider = provider,
        .task = redacted_task,
    });
    defer trace.deinit();

    var fanout: EventFanout = .{
        .trace = &trace,
        .listeners = options.listeners,
    };
    var lifecycle: RunLifecycle = .{};
    var timestamp_ms = options.base_timestamp_ms;

    _ = try fanout.emit(timestamp_ms, .{ .run_created = .{
        .task = redacted_task,
        .provider = provider,
    } });
    timestamp_ms += 1;
    try emitTransition(&fanout, timestamp_ms, try lifecycle.prepare("runtime prepared"));
    timestamp_ms += 1;

    var sandbox_handle: ?sandbox.SandboxHandle = null;
    const sandbox_backend = if (options.sandbox_backend) |sandbox_options| sandbox_options.backend else null;
    defer if (sandbox_handle) |handle| {
        if (sandbox_backend) |backend| {
            backend.stop(backend, handle) catch {};
        }
    };

    var effective_workspace_container_path = options.workspace_container_path;
    if (options.sandbox_backend) |sandbox_options| {
        const handle = sandbox_options.backend.launch(
            sandbox_options.backend,
            sandbox_options.launch,
        ) catch |err| {
            _ = try fanout.emit(timestamp_ms, .{ .error_reported = .{
                .message = @errorName(err),
                .component = "sandbox",
            } });
            timestamp_ms += 1;
            try emitTransition(&fanout, timestamp_ms, try lifecycle.fail(@errorName(err)));
            timestamp_ms += 1;
            const report_ref = try report.generate(allocator, trace.run_dir, options.redactor);
            _ = try fanout.emit(timestamp_ms, .{ .trace_artifact = .{
                .kind = @tagName(report_ref.kind),
                .path = report_ref.path,
            } });
            return adapterResult(allocator, run_id, .failed, .failed);
        };
        sandbox_handle = handle;
        effective_workspace_container_path = handle.workspace_mount;
        _ = try fanout.emit(timestamp_ms, .{ .sandbox_prepared = .{
            .backend = @tagName(handle.backend),
            .mode = @tagName(sandbox_options.launch.mode),
            .workspace_mount = handle.workspace_mount,
        } });
        if (handle.event_stream) |stream_value| {
            var stream = stream_value;
            _ = try drainSandboxEvents(allocator, &fanout, &stream);
        }
    } else {
        _ = try fanout.emit(timestamp_ms, .{ .sandbox_prepared = .{
            .backend = "none",
            .mode = "host-process",
            .workspace_mount = options.workspace_container_path orelse options.workspace_host_path,
        } });
    }
    timestamp_ms += 1;
    try emitTransition(&fanout, timestamp_ms, try lifecycle.startAdapter("starting adapter"));
    timestamp_ms += 1;
    _ = try fanout.emit(timestamp_ms, .{ .adapter_started = .{
        .provider = provider,
        .adapter = adapter.id.value,
        .mode = "adapter",
    } });
    timestamp_ms += 1;

    var bridge: AdapterEventBridge = .{
        .allocator = allocator,
        .fanout = &fanout,
        .redactor = options.redactor,
        .timestamp_ms = &timestamp_ms,
        .trace = &trace,
    };

    const adapter_run = adapter.start(adapter, .{
        .allocator = allocator,
        .run_id = run_id,
        .workspace_host_path = options.workspace_host_path,
        .workspace_container_path = effective_workspace_container_path,
        .env = options.env,
        .event_sink = .{
            .context = &bridge,
            .emit = AdapterEventBridge.emit,
        },
        .started_at_ms = timestamp_ms,
    }, options.task) catch |err| {
        _ = try fanout.emit(timestamp_ms, .{ .error_reported = .{
            .message = @errorName(err),
            .component = "adapter",
        } });
        timestamp_ms += 1;
        try stopSandboxIfActive(&fanout, &timestamp_ms, sandbox_backend, &sandbox_handle);
        try emitTransition(&fanout, timestamp_ms, try lifecycle.fail(@errorName(err)));
        timestamp_ms += 1;
        const report_ref = try report.generate(allocator, trace.run_dir, options.redactor);
        _ = try fanout.emit(timestamp_ms, .{ .trace_artifact = .{
            .kind = @tagName(report_ref.kind),
            .path = report_ref.path,
        } });
        return adapterResult(allocator, run_id, .failed, .failed);
    };

    if (options.sandbox_event_stream) |stream| {
        _ = try drainSandboxEvents(allocator, &fanout, stream);
    }
    try stopSandboxIfActive(&fanout, &timestamp_ms, sandbox_backend, &sandbox_handle);

    try emitTransition(&fanout, timestamp_ms, try lifecycle.markRunning("adapter returned"));
    timestamp_ms += 1;
    try emitTransition(&fanout, timestamp_ms, try lifecycle.finalize("collecting artifacts"));
    timestamp_ms += 1;

    const final_state: runtime.RunState = switch (adapter_run.status) {
        .completed => .completed,
        .failed => .failed,
        .cancelled => .cancelled,
        .starting, .running, .waiting_for_approval => .failed,
    };
    switch (final_state) {
        .completed => try emitTransition(&fanout, timestamp_ms, try lifecycle.complete("adapter completed")),
        .failed => {
            if (!adapter_run.status.isTerminal()) {
                _ = try fanout.emit(timestamp_ms, .{ .error_reported = .{
                    .message = "adapter returned non-terminal status",
                    .component = "runtime",
                } });
                timestamp_ms += 1;
            }
            try emitTransition(&fanout, timestamp_ms, try lifecycle.fail("adapter failed"));
        },
        .cancelled => try emitTransition(&fanout, timestamp_ms, try lifecycle.cancel("adapter cancelled")),
        else => unreachable,
    }
    timestamp_ms += 1;

    const report_ref = try report.generate(allocator, trace.run_dir, options.redactor);
    _ = try fanout.emit(timestamp_ms, .{ .trace_artifact = .{
        .kind = @tagName(report_ref.kind),
        .path = report_ref.path,
    } });

    return adapterResult(allocator, run_id, adapter_run.status, final_state);
}

pub fn ingestSandboxEvent(fanout: *EventFanout, event: sandbox.SandboxEvent) !runtime.RuntimeEventRecord {
    return fanout.emit(event.timestamp_ms, .{ .sandbox_event = .{
        .event_type = event.event_type,
        .detail = event.detail,
        .is_error = event.is_error,
    } });
}

pub fn drainSandboxEvents(
    allocator: std.mem.Allocator,
    fanout: *EventFanout,
    stream: *sandbox.SandboxEventStream,
) !usize {
    var count: usize = 0;
    while (try stream.next(stream.context, allocator)) |event| {
        defer if (stream.deinit_event) |deinit_event| {
            deinit_event(stream.context, allocator, event);
        };
        _ = try ingestSandboxEvent(fanout, event);
        count += 1;
    }
    return count;
}

fn stopSandboxIfActive(
    fanout: *EventFanout,
    timestamp_ms: *runtime.TimestampMs,
    backend: ?*sandbox.SandboxBackend,
    handle: *?sandbox.SandboxHandle,
) !void {
    const active = handle.* orelse return;
    const sandbox_backend = backend orelse return;
    sandbox_backend.stop(sandbox_backend, active) catch |err| {
        _ = try fanout.emit(timestamp_ms.*, .{ .error_reported = .{
            .message = @errorName(err),
            .component = "sandbox",
        } });
        timestamp_ms.* += 1;
        return;
    };
    handle.* = null;
}

fn adapterResult(
    allocator: std.mem.Allocator,
    run_id: runtime.RunId,
    status: adapters.AdapterRunStatus,
    final_state: runtime.RunState,
) !AdapterRunResult {
    return .{
        .run_id = run_id,
        .relative_path = try std.fs.path.join(allocator, &.{ ".runs", run_id.value }),
        .report_path = trace_types.defaultArtifactPath(.report),
        .adapter_status = status,
        .final_state = final_state,
    };
}

pub fn runFake(
    allocator: std.mem.Allocator,
    base_dir: std.fs.Dir,
    options: FakeRunOptions,
) !FakeRunResult {
    const run_id = try runtime.RunId.init(options.run_id);
    const redacted_task = try options.redactor.redactAlloc(
        allocator,
        .provider_event,
        options.task,
    );
    defer allocator.free(redacted_task);

    var trace = try trace_file.FileTrace.create(allocator, base_dir, run_id, .{
        .created_at_ms = options.base_timestamp_ms,
        .provider = options.provider,
        .task = redacted_task,
    });
    defer trace.deinit();

    var fanout: EventFanout = .{ .trace = &trace };
    var lifecycle: RunLifecycle = .{};
    var timestamp_ms = options.base_timestamp_ms;

    _ = try fanout.emit(timestamp_ms, .{ .run_created = .{
        .task = redacted_task,
        .provider = options.provider,
    } });
    timestamp_ms += 1;

    try emitTransition(&fanout, timestamp_ms, try lifecycle.prepare("workspace allocated"));
    timestamp_ms += 1;

    _ = try fanout.emit(timestamp_ms, .{ .sandbox_prepared = .{
        .backend = "none",
        .mode = "host-process",
        .workspace_mount = "/workspace",
    } });
    timestamp_ms += 1;

    try emitTransition(&fanout, timestamp_ms, try lifecycle.startAdapter("adapter selected"));
    timestamp_ms += 1;

    _ = try fanout.emit(timestamp_ms, .{ .adapter_started = .{
        .provider = options.provider,
        .adapter = options.adapter,
        .mode = @tagName(adapters.AdapterMode.headless_json),
    } });
    timestamp_ms += 1;

    try emitTransition(&fanout, timestamp_ms, try lifecycle.markRunning("adapter running"));
    timestamp_ms += 1;

    _ = try fanout.emit(timestamp_ms, .{ .process_spawned = .{
        .argv = &.{ "fake-agent", "--headless" },
        .cwd = "/workspace",
        .pid = 100,
    } });
    timestamp_ms += 1;

    const provider_output = try options.redactor.redactAlloc(
        allocator,
        .provider_event,
        "fake provider prepared final output",
    );
    defer allocator.free(provider_output);
    _ = try fanout.emit(timestamp_ms, .{ .adapter_output = .{
        .stream = .structured,
        .bytes = provider_output,
    } });
    timestamp_ms += 1;

    var deterministic_policy: policy_engine.DeterministicPolicy = .{
        .config = options.policy_config,
    };
    const action: policy.Action = .{ .run_command = .{
        .argv = &.{ "zig", "build", "test" },
        .cwd = "/workspace",
    } };
    const audited = deterministic_policy.evaluateAudited(action, .{
        .run_id = run_id,
        .cwd = "/workspace",
        .timestamp_ms = timestamp_ms,
    });
    _ = try fanout.emit(timestamp_ms, .{ .policy_decision = .{
        .action_kind = @tagName(audited.action_kind),
        .decision = audited.decisionName(),
        .rule_id = if (audited.rule_id) |rule| rule.value else null,
        .reason = audited.reason,
    } });
    timestamp_ms += 1;

    var approval_store: approvals.FileApprovalStore = .{
        .allocator = allocator,
        .run_dir = trace.run_dir,
    };
    switch (audited.decision) {
        .ask => {
            const approval_id = try policy.ApprovalId.init("approval-1");
            const request: policy.ApprovalRequest = .{
                .id = approval_id,
                .run_id = run_id,
                .action = action,
                .source = .runtime,
                .risk = .medium,
                .matched_rule = audited.rule_id,
                .suggested_decision = audited.decision,
            };
            try approval_store.persistRequest(request, timestamp_ms);
            _ = try fanout.emit(timestamp_ms, .{ .approval_requested = .{
                .approval_id = approval_id.value,
                .action_kind = @tagName(action.kind()),
                .risk = @tagName(policy.RiskLevel.medium),
            } });
            timestamp_ms += 1;

            try approval_store.recordDecision(approval_id, .allow_once, "runtime", timestamp_ms);
            _ = try fanout.emit(timestamp_ms, .{ .approval_decision = .{
                .approval_id = approval_id.value,
                .decision = approvals.approvalDecisionName(.allow_once),
                .decided_by = "runtime",
            } });
            timestamp_ms += 1;
        },
        .allow, .block => {},
    }

    _ = try fanout.emit(timestamp_ms, .{ .resource_sample = .{
        .memory_bytes = 64 * 1024 * 1024,
        .cpu_millis = 120,
        .pids = 2,
    } });
    timestamp_ms += 1;

    _ = try fanout.emit(timestamp_ms, .{ .sandbox_event = .{
        .event_type = "container_stop",
        .detail = "fake runtime completed",
        .is_error = false,
    } });
    timestamp_ms += 1;

    const redacted_final = try options.redactor.redactAlloc(
        allocator,
        .provider_event,
        options.final_output,
    );
    defer allocator.free(redacted_final);
    _ = try trace.writeArtifact(.final_output, redacted_final);
    _ = try fanout.emit(timestamp_ms, .{ .final_output = .{ .text = redacted_final } });
    timestamp_ms += 1;

    const redacted_diff = try options.redactor.redactAlloc(
        allocator,
        .report,
        options.diff_patch,
    );
    defer allocator.free(redacted_diff);
    _ = try trace.writeArtifact(.diff_patch, redacted_diff);

    _ = try trace.writeArtifact(.terminal_redacted, provider_output);
    _ = try fanout.emit(timestamp_ms, .{ .trace_artifact = .{
        .kind = @tagName(trace_types.ArtifactKind.final_output),
        .path = trace_types.defaultArtifactPath(.final_output),
    } });
    timestamp_ms += 1;

    const exit_code: ?u8 = switch (options.outcome) {
        .completed => 0,
        .failed => 1,
        .cancelled => null,
    };
    const signal: ?u8 = switch (options.outcome) {
        .cancelled => 15,
        else => null,
    };
    _ = try fanout.emit(timestamp_ms, .{ .process_exited = .{
        .pid = 100,
        .exit_code = exit_code,
        .signal = signal,
    } });
    timestamp_ms += 1;

    try emitTransition(&fanout, timestamp_ms, try lifecycle.finalize("collecting artifacts"));
    timestamp_ms += 1;

    switch (options.outcome) {
        .completed => try emitTransition(&fanout, timestamp_ms, try lifecycle.complete("fake run completed")),
        .failed => {
            const redacted_error = try options.redactor.redactAlloc(
                allocator,
                .provider_event,
                options.error_message,
            );
            defer allocator.free(redacted_error);
            _ = try fanout.emit(timestamp_ms, .{ .error_reported = .{
                .message = redacted_error,
                .component = "runtime",
            } });
            timestamp_ms += 1;
            try emitTransition(&fanout, timestamp_ms, try lifecycle.fail(redacted_error));
        },
        .cancelled => {
            _ = try fanout.emit(timestamp_ms, .{ .error_reported = .{
                .message = "fake run cancelled",
                .component = "runtime",
            } });
            timestamp_ms += 1;
            try emitTransition(&fanout, timestamp_ms, try lifecycle.cancel("fake run cancelled"));
        },
    }
    timestamp_ms += 1;

    const report_ref = try report.generate(allocator, trace.run_dir, options.redactor);
    _ = try fanout.emit(timestamp_ms, .{ .trace_artifact = .{
        .kind = @tagName(report_ref.kind),
        .path = report_ref.path,
    } });

    const relative_path = try std.fs.path.join(allocator, &.{ ".runs", run_id.value });
    errdefer allocator.free(relative_path);
    return .{
        .run_id = run_id,
        .relative_path = relative_path,
        .report_path = trace_types.defaultArtifactPath(.report),
    };
}

fn emitTransition(
    fanout: *EventFanout,
    timestamp_ms: runtime.TimestampMs,
    change: runtime.RunStateChanged,
) !void {
    _ = try fanout.emit(timestamp_ms, .{ .run_state_changed = change });
}

fn isValidTransition(from: runtime.RunState, to: runtime.RunState) bool {
    if (from.isTerminal()) return false;

    return switch (from) {
        .created => switch (to) {
            .preparing_workspace, .failed, .cancelled => true,
            else => false,
        },
        .preparing_workspace => switch (to) {
            .starting_adapter, .failed, .cancelled => true,
            else => false,
        },
        .starting_adapter => switch (to) {
            .running, .failed, .cancelled => true,
            else => false,
        },
        .running => switch (to) {
            .finalizing, .failed, .cancelled => true,
            else => false,
        },
        .finalizing => switch (to) {
            .completed, .failed, .cancelled => true,
            else => false,
        },
        .completed, .failed, .cancelled => false,
    };
}

test "run lifecycle rejects transitions after terminal state" {
    var lifecycle: RunLifecycle = .{};
    _ = try lifecycle.prepare(null);
    _ = try lifecycle.startAdapter(null);
    _ = try lifecycle.markRunning(null);
    _ = try lifecycle.finalize(null);
    _ = try lifecycle.complete(null);

    try std.testing.expectError(error.InvalidTransition, lifecycle.fail("too late"));
}

test "fake run produces a complete redacted run directory" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const rules = [_]redaction.RedactionRule{
        .{ .id = "token", .pattern = "SECRET_TOKEN" },
    };
    const result = try runFake(allocator, tmp.dir, .{
        .task = "summarize SECRET_TOKEN",
        .run_id = "01HFAKE",
        .final_output = "done with SECRET_TOKEN\n",
        .diff_patch = "diff --git a/a b/a\n+SECRET_TOKEN\n",
        .redactor = .{ .rules = &rules },
    });
    defer result.deinit(allocator);

    var run_dir = try tmp.dir.openDir(result.relative_path, .{});
    defer run_dir.close();

    const events = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.events),
        16 * 1024,
    );
    defer allocator.free(events);
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"run_created\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"approval_requested\""));
    try std.testing.expect(!std.mem.containsAtLeast(u8, events, 1, "SECRET_TOKEN"));

    const report_text = try run_dir.readFileAlloc(
        allocator,
        result.report_path,
        16 * 1024,
    );
    defer allocator.free(report_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, report_text, 1, "Final state: completed"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, report_text, 1, "SECRET_TOKEN"));

    const approvals_text = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.approvals),
        16 * 1024,
    );
    defer allocator.free(approvals_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, approvals_text, 1, "approval_decision"));

    const sandbox_text = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.sandbox_events),
        16 * 1024,
    );
    defer allocator.free(sandbox_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, sandbox_text, 1, "container_stop"));
}

test "fake run failure path persists terminal state and error report" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const result = try runFake(allocator, tmp.dir, .{
        .task = "fail",
        .run_id = "01HFAIL",
        .outcome = .failed,
        .error_message = "provider exited with sk-test-1234567890",
    });
    defer result.deinit(allocator);

    var run_dir = try tmp.dir.openDir(result.relative_path, .{});
    defer run_dir.close();

    const events = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.events),
        16 * 1024,
    );
    defer allocator.free(events);
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"error_reported\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"to\":\"failed\""));
    try std.testing.expect(!std.mem.containsAtLeast(u8, events, 1, "sk-test-1234567890"));

    const report_text = try run_dir.readFileAlloc(
        allocator,
        result.report_path,
        16 * 1024,
    );
    defer allocator.free(report_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, report_text, 1, "Final state: failed"));
    try std.testing.expect(std.mem.containsAtLeast(u8, report_text, 1, "Runtime errors: 1"));
}

test "adapter run bridges provider events into redacted trace artifacts" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    var adapter = @import("../providers/fake.zig").initHeadlessAdapter();
    const result = try runAdapter(allocator, tmp.dir, &adapter, .{
        .task = .{ .text = "hello SECRET_TOKEN" },
        .run_id = "01HADAPT",
        .workspace_host_path = ".",
        .redactor = .{ .rules = &.{
            .{ .id = "token", .pattern = "SECRET_TOKEN" },
        } },
    });
    defer result.deinit(allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, result.adapter_status);
    try std.testing.expectEqual(runtime.RunState.completed, result.final_state);

    var run_dir = try tmp.dir.openDir(result.relative_path, .{});
    defer run_dir.close();

    const events = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.events),
        16 * 1024,
    );
    defer allocator.free(events);
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"adapter_output\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"final_output\""));
    try std.testing.expect(!std.mem.containsAtLeast(u8, events, 1, "SECRET_TOKEN"));

    const report_text = try run_dir.readFileAlloc(
        allocator,
        result.report_path,
        16 * 1024,
    );
    defer allocator.free(report_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, report_text, 1, "Final state: completed"));
}

test "adapter run captures real headless process output into trace" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    var adapter: adapters.AgentAdapter = .{
        .id = .{ .value = "local.headless" },
        .provider = .{ .value = "local" },
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
            .supports_streaming_events = true,
        },
        .start = startLocalHeadlessAdapter,
    };
    const result = try runAdapter(allocator, tmp.dir, &adapter, .{
        .task = .{ .text = "ignored" },
        .run_id = "01HLOCALHEADLESS",
        .workspace_host_path = ".",
    });
    defer result.deinit(allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, result.adapter_status);
    try std.testing.expectEqual(runtime.RunState.completed, result.final_state);

    var run_dir = try tmp.dir.openDir(result.relative_path, .{});
    defer run_dir.close();

    const events = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.events),
        16 * 1024,
    );
    defer allocator.free(events);
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"process_spawned\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"process_exited\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"stream\":\"structured\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"type\":\"final_output\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "local done"));

    const final = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.final_output),
        16 * 1024,
    );
    defer allocator.free(final);
    try std.testing.expectEqualStrings("local done", final);
}

test "adapter run records failed adapter status" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    var adapter: adapters.AgentAdapter = .{
        .id = .{ .value = "failing.adapter" },
        .provider = .{ .value = "failing" },
        .capabilities = .{ .supports_headless = true },
        .start = startFailingAdapter,
    };
    const result = try runAdapter(allocator, tmp.dir, &adapter, .{
        .task = .{ .text = "fail" },
        .run_id = "01HADAPTFAIL",
        .workspace_host_path = ".",
    });
    defer result.deinit(allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.failed, result.adapter_status);
    try std.testing.expectEqual(runtime.RunState.failed, result.final_state);

    var run_dir = try tmp.dir.openDir(result.relative_path, .{});
    defer run_dir.close();
    const report_text = try run_dir.readFileAlloc(
        allocator,
        result.report_path,
        16 * 1024,
    );
    defer allocator.free(report_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, report_text, 1, "Final state: failed"));
}

test "runtime drains sandbox event stream into trace sidecar" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const run_id = try runtime.RunId.init("01HSANDBOX");
    var trace = try trace_file.FileTrace.create(allocator, tmp.dir, run_id, .{
        .created_at_ms = 1,
        .provider = "fake",
        .task = "sandbox",
    });
    defer trace.deinit();

    var fanout: EventFanout = .{ .trace = &trace };
    const events = [_]sandbox.SandboxEvent{
        .{
            .timestamp_ms = 2,
            .run_id = run_id,
            .sandbox_id = "sandbox-1",
            .event_class = .lifecycle,
            .event_type = "container_start",
            .detail = "started",
        },
        .{
            .timestamp_ms = 3,
            .run_id = run_id,
            .sandbox_id = "sandbox-1",
            .event_class = .network,
            .event_type = "egress_denied",
            .detail = "blocked tcp example.com:443",
            .is_error = true,
        },
    };
    var fixture: SandboxStreamFixture = .{ .events = &events };
    var stream: sandbox.SandboxEventStream = .{
        .context = &fixture,
        .next = SandboxStreamFixture.next,
    };

    const drained = try drainSandboxEvents(allocator, &fanout, &stream);
    try std.testing.expectEqual(@as(usize, 2), drained);

    const sandbox_text = try trace.run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.sandbox_events),
        16 * 1024,
    );
    defer allocator.free(sandbox_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, sandbox_text, 1, "container_start"));
    try std.testing.expect(std.mem.containsAtLeast(u8, sandbox_text, 1, "egress_denied"));

    var summary = try trace_file.summarizeEvents(allocator, trace.run_dir);
    defer summary.deinit(allocator);
    try std.testing.expectEqual(@as(usize, 2), summary.sandbox_events);
    try std.testing.expectEqual(@as(usize, 1), summary.sandbox_errors);
}

test "adapter run can launch through sandbox backend" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const nucleus = @import("../sandbox/nucleus.zig");
    var transport: RuntimeFakeNucleusTransport = .{};
    var backend = nucleus.NucleusBackend.init(allocator, .{
        .context = &transport,
        .launch = RuntimeFakeNucleusTransport.launch,
        .stop = RuntimeFakeNucleusTransport.stop,
    });
    defer backend.deinit();

    const run_id = try runtime.RunId.init("01HNUC");
    var adapter = @import("../providers/fake.zig").initHeadlessAdapter();
    const result = try runAdapter(allocator, tmp.dir, &adapter, .{
        .task = .{ .text = "hello in nucleus" },
        .run_id = run_id.value,
        .workspace_host_path = "/repo/.agent-worktrees/01HNUC",
        .sandbox_backend = .{
            .backend = &backend.backend,
            .launch = .{
                .run_id = run_id,
                .backend = .nucleus,
                .mode = .strict_agent,
                .runtime = .gvisor,
                .command = .{
                    .argv = &.{ "fake-agent", "--headless" },
                    .cwd = "/workspace",
                },
                .mounts = &.{.{
                    .host_path = "/repo/.agent-worktrees/01HNUC",
                    .container_path = "/workspace",
                    .access = .read_write_execute,
                }},
                .resources = .{ .memory = "4G", .cpus = 4, .pids = 1024 },
                .network = .none,
            },
        },
    });
    defer result.deinit(allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, result.adapter_status);
    try std.testing.expectEqual(@as(usize, 1), transport.launch_count);
    try std.testing.expectEqual(@as(usize, 1), transport.stop_count);

    var run_dir = try tmp.dir.openDir(result.relative_path, .{});
    defer run_dir.close();
    const events = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.events),
        16 * 1024,
    );
    defer allocator.free(events);
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"backend\":\"nucleus\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "container_start"));
}

test "adapter run records sandbox launch failure as failed run" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    var backend = RuntimeFailingSandboxBackend.init();
    const run_id = try runtime.RunId.init("01HSBOXFAIL");
    var adapter = @import("../providers/fake.zig").initHeadlessAdapter();
    const result = try runAdapter(allocator, tmp.dir, &adapter, .{
        .task = .{ .text = "hello in failing sandbox" },
        .run_id = run_id.value,
        .workspace_host_path = "/repo/.agent-worktrees/01HSBOXFAIL",
        .sandbox_backend = .{
            .backend = &backend.backend,
            .launch = .{
                .run_id = run_id,
                .backend = .nucleus,
                .mode = .strict_agent,
                .runtime = .gvisor,
                .command = .{
                    .argv = &.{ "fake-agent", "--headless" },
                    .cwd = "/workspace",
                },
                .mounts = &.{.{
                    .host_path = "/repo/.agent-worktrees/01HSBOXFAIL",
                    .container_path = "/workspace",
                    .access = .read_write_execute,
                }},
                .network = .none,
            },
        },
    });
    defer result.deinit(allocator);

    try std.testing.expectEqual(adapters.AdapterRunStatus.failed, result.adapter_status);
    try std.testing.expectEqual(runtime.RunState.failed, result.final_state);
    try std.testing.expectEqual(@as(usize, 1), backend.launch_count);
    try std.testing.expectEqual(@as(usize, 0), backend.stop_count);

    var run_dir = try tmp.dir.openDir(result.relative_path, .{});
    defer run_dir.close();

    const events = try run_dir.readFileAlloc(
        allocator,
        trace_types.defaultArtifactPath(.events),
        16 * 1024,
    );
    defer allocator.free(events);
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"component\":\"sandbox\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, events, 1, "\"to\":\"failed\""));

    const report_text = try run_dir.readFileAlloc(
        allocator,
        result.report_path,
        16 * 1024,
    );
    defer allocator.free(report_text);
    try std.testing.expect(std.mem.containsAtLeast(u8, report_text, 1, "Final state: failed"));
    try std.testing.expect(std.mem.containsAtLeast(u8, report_text, 1, "Runtime errors: 1"));
}

const SandboxStreamFixture = struct {
    events: []const sandbox.SandboxEvent,
    index: usize = 0,

    fn next(context: *anyopaque, _: std.mem.Allocator) anyerror!?sandbox.SandboxEvent {
        const self: *SandboxStreamFixture = @ptrCast(@alignCast(context));
        if (self.index >= self.events.len) return null;
        defer self.index += 1;
        return self.events[self.index];
    }
};

const RuntimeFakeNucleusTransport = struct {
    launch_count: usize = 0,
    stop_count: usize = 0,

    fn launch(context: *anyopaque, _: []const u8, _: std.mem.Allocator) anyerror!@import("../sandbox/nucleus.zig").NucleusLaunchResult {
        const self: *RuntimeFakeNucleusTransport = @ptrCast(@alignCast(context));
        self.launch_count += 1;
        return .{
            .container_id = "nucleus-runtime-id",
            .process_id = 7,
            .event_jsonl =
            \\{"timestamp":"2026-05-20T18:00:01.000Z","run_id":"01HNUC","container_id":"nucleus-runtime-id","event_type":"container_start","detail":"started","is_error":false}
            \\
            ,
        };
    }

    fn stop(context: *anyopaque, container_id: []const u8) anyerror!void {
        const self: *RuntimeFakeNucleusTransport = @ptrCast(@alignCast(context));
        try std.testing.expectEqualStrings("nucleus-runtime-id", container_id);
        self.stop_count += 1;
    }
};

const RuntimeFailingSandboxBackend = struct {
    backend: sandbox.SandboxBackend,
    launch_count: usize = 0,
    stop_count: usize = 0,

    fn init() RuntimeFailingSandboxBackend {
        return .{
            .backend = .{
                .id = .nucleus,
                .capabilities = .{},
                .launch = launch,
                .stop = stop,
            },
        };
    }

    fn launch(backend: *sandbox.SandboxBackend, _: sandbox.SandboxLaunchConfig) anyerror!sandbox.SandboxHandle {
        const self: *RuntimeFailingSandboxBackend = @alignCast(@fieldParentPtr("backend", backend));
        self.launch_count += 1;
        return error.SandboxLaunchFailed;
    }

    fn stop(backend: *sandbox.SandboxBackend, _: sandbox.SandboxHandle) anyerror!void {
        const self: *RuntimeFailingSandboxBackend = @alignCast(@fieldParentPtr("backend", backend));
        self.stop_count += 1;
    }
};

fn startFailingAdapter(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    _: adapters.Task,
) anyerror!adapters.AdapterRun {
    if (context.event_sink) |sink| {
        try sink.emit(sink.context, .{ .error_message = "adapter failed" });
    }
    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .mode = .headless_json,
        .status = .failed,
        .started_at_ms = context.started_at_ms,
    };
}

fn startLocalHeadlessAdapter(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    _: adapters.Task,
) anyerror!adapters.AdapterRun {
    const provider_headless = @import("../providers/headless.zig");
    const argv = [_][]const u8{
        "sh",
        "-c",
        "printf '{\"final_output\":\"local done\"}\\n'",
    };
    const result = try provider_headless.executeAndEmit(context.allocator, .{
        .argv = &argv,
        .cwd = context.workspace_host_path,
    }, context.event_sink, .{});
    defer result.deinit(context.allocator);

    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .mode = .headless_json,
        .status = result.status(),
        .started_at_ms = context.started_at_ms,
    };
}
