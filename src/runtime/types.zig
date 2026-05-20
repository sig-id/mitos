const std = @import("std");

pub const TimestampMs = i64;
pub const Sequence = u64;

pub const RunIdError = error{
    EmptyRunId,
};

pub const RunId = struct {
    value: []const u8,

    pub fn init(value: []const u8) RunIdError!RunId {
        if (value.len == 0) return error.EmptyRunId;
        return .{ .value = value };
    }

    pub fn eql(self: RunId, other: RunId) bool {
        return std.mem.eql(u8, self.value, other.value);
    }
};

pub const RunState = enum {
    created,
    preparing_workspace,
    starting_adapter,
    running,
    finalizing,
    completed,
    failed,
    cancelled,

    pub fn isTerminal(self: RunState) bool {
        return switch (self) {
            .completed, .failed, .cancelled => true,
            else => false,
        };
    }
};

pub const RuntimeEventRecord = struct {
    run_id: RunId,
    sequence: Sequence,
    timestamp_ms: TimestampMs,
    event: RuntimeEvent,
};

pub const RuntimeEvent = union(enum) {
    run_created: RunCreated,
    run_state_changed: RunStateChanged,
    adapter_started: AdapterStarted,
    adapter_output: StreamChunk,
    process_spawned: ProcessSpawned,
    process_exited: ProcessExited,
    terminal_effect: TerminalEffectRef,
    screen_snapshot: ArtifactRef,
    prompt_detected: PromptDetected,
    policy_decision: PolicyDecisionRef,
    approval_requested: ApprovalRequestedRef,
    approval_decision: ApprovalDecisionRef,
    sandbox_prepared: SandboxPrepared,
    sandbox_event: SandboxEventRef,
    resource_sample: ResourceSample,
    trace_artifact: ArtifactRef,
    final_output: TextEvent,
    error_reported: ErrorReported,
};

pub const RunCreated = struct {
    task: []const u8,
    provider: []const u8,
};

pub const RunStateChanged = struct {
    from: RunState,
    to: RunState,
    reason: ?[]const u8 = null,
};

pub const AdapterStarted = struct {
    provider: []const u8,
    adapter: []const u8,
    mode: []const u8,
};

pub const StreamName = enum {
    stdout,
    stderr,
    structured,
    terminal,
};

pub const StreamChunk = struct {
    stream: StreamName,
    bytes: []const u8,
};

pub const ProcessSpawned = struct {
    argv: []const []const u8,
    cwd: []const u8,
    pid: ?u32 = null,
};

pub const ProcessExited = struct {
    pid: ?u32 = null,
    exit_code: ?u8 = null,
    signal: ?u8 = null,
};

pub const TerminalEffectRef = struct {
    effect: []const u8,
    artifact_path: ?[]const u8 = null,
};

pub const PromptDetected = struct {
    prompt_kind: []const u8,
    text: []const u8,
    risk: ?[]const u8 = null,
};

pub const PolicyDecisionRef = struct {
    action_kind: []const u8,
    decision: []const u8,
    rule_id: ?[]const u8 = null,
    reason: ?[]const u8 = null,
};

pub const ApprovalRequestedRef = struct {
    approval_id: []const u8,
    action_kind: []const u8,
    risk: []const u8,
};

pub const ApprovalDecisionRef = struct {
    approval_id: []const u8,
    decision: []const u8,
    decided_by: []const u8,
};

pub const SandboxPrepared = struct {
    backend: []const u8,
    mode: []const u8,
    workspace_mount: []const u8,
};

pub const SandboxEventRef = struct {
    event_type: []const u8,
    detail: ?[]const u8 = null,
    is_error: bool = false,
};

pub const ResourceSample = struct {
    memory_bytes: ?u64 = null,
    cpu_millis: ?u64 = null,
    pids: ?u32 = null,
};

pub const ArtifactRef = struct {
    kind: []const u8,
    path: []const u8,
};

pub const TextEvent = struct {
    text: []const u8,
};

pub const ErrorReported = struct {
    message: []const u8,
    component: ?[]const u8 = null,
};

test "run id rejects empty values" {
    try std.testing.expectError(error.EmptyRunId, RunId.init(""));
    const run_id = try RunId.init("01HXYZ");
    try std.testing.expect(run_id.eql(.{ .value = "01HXYZ" }));
}

test "terminal states are classified" {
    try std.testing.expect(!RunState.created.isTerminal());
    try std.testing.expect(!RunState.running.isTerminal());
    try std.testing.expect(RunState.completed.isTerminal());
    try std.testing.expect(RunState.failed.isTerminal());
    try std.testing.expect(RunState.cancelled.isTerminal());
}
