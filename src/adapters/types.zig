const std = @import("std");
const runtime = @import("../runtime/types.zig");
const terminal = @import("../terminal/core.zig");
const transport = @import("../transport/root.zig");

pub const IdentifierError = error{
    EmptyIdentifier,
};

pub const ProviderId = struct {
    value: []const u8,

    pub fn init(value: []const u8) IdentifierError!ProviderId {
        if (value.len == 0) return error.EmptyIdentifier;
        return .{ .value = value };
    }
};

pub const AdapterId = struct {
    value: []const u8,

    pub fn init(value: []const u8) IdentifierError!AdapterId {
        if (value.len == 0) return error.EmptyIdentifier;
        return .{ .value = value };
    }
};

pub const AdapterCapabilities = struct {
    supports_headless: bool = false,
    supports_json_output: bool = false,
    supports_streaming_events: bool = false,
    supports_interactive_pty: bool = false,
    supports_cost_reporting: bool = false,
    supports_model_selection: bool = false,

    pub fn supportsTransport(self: AdapterCapabilities, transport_name: transport.TransportName) bool {
        return switch (transport_name) {
            .stdio_json => self.supports_headless and self.supports_json_output,
            .pty => self.supports_interactive_pty,
            else => false,
        };
    }
};

pub const AuthStatus = enum {
    unknown,
    authenticated,
    unauthenticated,
    not_required,
};

pub const AdapterDetection = struct {
    available: bool,
    binary: ?[]const u8 = null,
    version: ?[]const u8 = null,
    owns_version: bool = false,
    auth_status: AuthStatus = .unknown,
    supported_transports: []const transport.TransportName = &.{},
    warnings: []const []const u8 = &.{},

    pub fn supportsTransport(self: AdapterDetection, transport_name: transport.TransportName) bool {
        if (!self.available) return false;
        if (self.supported_transports.len == 0) return true;

        for (self.supported_transports) |supported_transport| {
            if (supported_transport == transport_name) return true;
        }
        return false;
    }

    pub fn deinit(self: AdapterDetection, allocator: std.mem.Allocator) void {
        if (self.owns_version) {
            if (self.version) |version| allocator.free(version);
        }
    }
};

pub const EnvVar = struct {
    name: []const u8,
    value: []const u8,
};

pub const Task = struct {
    text: []const u8,
    files: []const []const u8 = &.{},
};

pub const AdapterContext = struct {
    allocator: std.mem.Allocator = std.heap.page_allocator,
    run_id: runtime.RunId,
    workspace_host_path: []const u8,
    workspace_container_path: ?[]const u8 = null,
    env: []const EnvVar = &.{},
    event_sink: ?AdapterEventSink = null,
    terminal_core: ?*terminal.TerminalCore = null,
    started_at_ms: runtime.TimestampMs = 0,
    get_pending_response: ?*const fn (*anyopaque) ?[]const u8 = null,
    response_context: ?*anyopaque = null,
};

pub const AdapterRunStatus = enum {
    starting,
    running,
    waiting_for_approval,
    completed,
    failed,
    cancelled,

    pub fn isTerminal(self: AdapterRunStatus) bool {
        return switch (self) {
            .completed, .failed, .cancelled => true,
            else => false,
        };
    }
};

pub const AdapterRun = struct {
    run_id: runtime.RunId,
    adapter_id: AdapterId,
    transport_name: transport.TransportName,
    status: AdapterRunStatus,
    process_id: ?u32 = null,
    started_at_ms: runtime.TimestampMs,
};

pub const AdapterEvent = union(enum) {
    process_spawned: runtime.ProcessSpawned,
    process_exited: runtime.ProcessExited,
    prompt_detected: runtime.PromptDetected,
    stdout: []const u8,
    stderr: []const u8,
    structured: []const u8,
    terminal_bytes: []const u8,
    terminal_snapshot: []const u8,
    final_output: []const u8,
    error_message: []const u8,
};

pub const AdapterEventSink = struct {
    context: *anyopaque,
    emit: *const fn (*anyopaque, AdapterEvent) anyerror!void,
};

pub const AgentAdapter = struct {
    id: AdapterId,
    provider: ProviderId,
    capabilities: AdapterCapabilities,
    detect: ?*const fn (*AgentAdapter, std.mem.Allocator) anyerror!AdapterDetection = null,
    start: *const fn (*AgentAdapter, AdapterContext, Task) anyerror!AdapterRun,
    cancel: ?*const fn (*AgentAdapter, AdapterRun) anyerror!void = null,
};

test "adapter capability matching follows configured modes" {
    const capabilities: AdapterCapabilities = .{
        .supports_headless = true,
        .supports_json_output = true,
        .supports_interactive_pty = false,
    };

    try std.testing.expect(capabilities.supportsTransport(.stdio_json));
    try std.testing.expect(!capabilities.supportsTransport(.pty));
}

test "adapter run terminal states are classified" {
    try std.testing.expect(!AdapterRunStatus.running.isTerminal());
    try std.testing.expect(AdapterRunStatus.completed.isTerminal());
}

test "adapter detection can narrow supported modes" {
    const detection: AdapterDetection = .{
        .available = true,
        .supported_transports = &.{.stdio_json},
    };

    try std.testing.expect(detection.supportsTransport(.stdio_json));
    try std.testing.expect(!detection.supportsTransport(.pty));
}
