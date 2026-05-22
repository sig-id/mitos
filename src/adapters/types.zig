const std = @import("std");
const runtime = @import("../runtime/types.zig");
const terminal = @import("../terminal/core.zig");

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

pub const AdapterMode = enum {
    native_api,
    headless_json,
    exec_json,
    pty,
    raw_shell,

    pub fn fromName(mode_name: []const u8) ?AdapterMode {
        inline for (std.meta.fields(AdapterMode)) |field| {
            if (std.mem.eql(u8, mode_name, field.name)) {
                return @field(AdapterMode, field.name);
            }
        }
        return null;
    }

    pub fn name(self: AdapterMode) []const u8 {
        return @tagName(self);
    }
};

pub const AdapterCapabilities = struct {
    supports_headless: bool = false,
    supports_json_output: bool = false,
    supports_streaming_events: bool = false,
    supports_interactive_pty: bool = false,
    supports_cost_reporting: bool = false,
    supports_model_selection: bool = false,

    pub fn supportsMode(self: AdapterCapabilities, mode: AdapterMode) bool {
        return switch (mode) {
            .native_api => self.supports_streaming_events,
            .headless_json => self.supports_headless and self.supports_json_output,
            .exec_json => self.supports_headless and self.supports_json_output,
            .pty => self.supports_interactive_pty,
            .raw_shell => self.supports_headless,
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
    supported_modes: []const AdapterMode = &.{},
    warnings: []const []const u8 = &.{},

    pub fn supportsMode(self: AdapterDetection, mode: AdapterMode) bool {
        if (!self.available) return false;
        if (self.supported_modes.len == 0) return true;

        for (self.supported_modes) |supported_mode| {
            if (supported_mode == mode) return true;
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
    mode: AdapterMode,
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

    try std.testing.expect(capabilities.supportsMode(.headless_json));
    try std.testing.expect(capabilities.supportsMode(.exec_json));
    try std.testing.expect(!capabilities.supportsMode(.pty));
}

test "adapter run terminal states are classified" {
    try std.testing.expect(!AdapterRunStatus.running.isTerminal());
    try std.testing.expect(AdapterRunStatus.completed.isTerminal());
}

test "adapter detection can narrow supported modes" {
    const detection: AdapterDetection = .{
        .available = true,
        .supported_modes = &.{.exec_json},
    };

    try std.testing.expect(detection.supportsMode(.exec_json));
    try std.testing.expect(!detection.supportsMode(.pty));
}

test "adapter modes parse from config names" {
    try std.testing.expectEqual(AdapterMode.headless_json, AdapterMode.fromName("headless_json").?);
    try std.testing.expectEqual(AdapterMode.exec_json, AdapterMode.fromName("exec_json").?);
    try std.testing.expectEqual(AdapterMode.pty, AdapterMode.fromName("pty").?);
    try std.testing.expect(AdapterMode.fromName("missing") == null);
    try std.testing.expectEqualStrings("pty", AdapterMode.pty.name());
}
