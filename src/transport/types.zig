const std = @import("std");

pub const protocol_version: u32 = 1;

pub const TransportKind = enum {
    stdio,
    pty,
    socket_json_rpc,
    http_stream,
    cloud_job,

    pub fn name(self: TransportKind) []const u8 {
        return switch (self) {
            .stdio => "stdio",
            .pty => "pty",
            .socket_json_rpc => "socket(json-rpc)",
            .http_stream => "http-stream",
            .cloud_job => "cloud-job",
        };
    }
};

pub const CodecKind = enum {
    text,
    json_stream,
    acp,
    mcp,
    vt_screen,

    pub fn name(self: CodecKind) []const u8 {
        return switch (self) {
            .text => "text",
            .json_stream => "json-stream",
            .acp => "acp",
            .mcp => "mcp",
            .vt_screen => "vt-screen",
        };
    }
};

pub const LifecycleKind = enum {
    one_shot,
    durable_session,

    pub fn name(self: LifecycleKind) []const u8 {
        return switch (self) {
            .one_shot => "one-shot",
            .durable_session => "durable-session",
        };
    }
};

pub const TransportName = enum {
    pty,
    stdio_json,
    acp,
    mcp,
    cloud_job,
    http_stream,

    pub fn fromName(wire_name: []const u8) ?TransportName {
        const map = std.StaticStringMap(TransportName).initComptime(.{
            .{ "pty", .pty },
            .{ "stdio-json", .stdio_json },
            .{ "stdio_json", .stdio_json },
            .{ "headless_json", .stdio_json },
            .{ "exec_json", .stdio_json },
            .{ "acp", .acp },
            .{ "mcp", .mcp },
            .{ "cloud-job", .cloud_job },
            .{ "cloud_job", .cloud_job },
            .{ "http-stream", .http_stream },
            .{ "http_stream", .http_stream },
        });
        return map.get(wire_name);
    }

    pub fn name(self: TransportName) []const u8 {
        return switch (self) {
            .pty => "pty",
            .stdio_json => "stdio-json",
            .acp => "acp",
            .mcp => "mcp",
            .cloud_job => "cloud-job",
            .http_stream => "http-stream",
        };
    }

    pub fn axes(self: TransportName) TransportSpec {
        return switch (self) {
            .pty => .{
                .transport = .pty,
                .codec = .vt_screen,
                .lifecycle = .durable_session,
            },
            .stdio_json => .{
                .transport = .stdio,
                .codec = .json_stream,
                .lifecycle = .one_shot,
            },
            .acp => .{
                .transport = .socket_json_rpc,
                .codec = .acp,
                .lifecycle = .durable_session,
            },
            .mcp => .{
                .transport = .socket_json_rpc,
                .codec = .mcp,
                .lifecycle = .durable_session,
            },
            .cloud_job => .{
                .transport = .cloud_job,
                .codec = .json_stream,
                .lifecycle = .durable_session,
            },
            .http_stream => .{
                .transport = .http_stream,
                .codec = .json_stream,
                .lifecycle = .durable_session,
            },
        };
    }
};

pub const TransportSpec = struct {
    transport: TransportKind,
    codec: CodecKind,
    lifecycle: LifecycleKind,
};

pub const Capabilities = struct {
    transport: TransportKind,
    codecs: []const CodecKind = &.{},
    lifecycle: LifecycleKind,
    can_resize: bool = false,
    can_cancel: bool = true,
    can_reattach: bool = false,
};

pub const Input = union(enum) {
    bytes: []const u8,
    structured: []const u8,
};

pub const Frame = union(enum) {
    raw: []const u8,
    structured: []const u8,
};

pub const OpenSpec = struct {
    launch: LaunchSpec,
    selected_transport: TransportName,
};

pub const TransportSession = struct {
    context: *anyopaque,
    send_fn: *const fn (*anyopaque, Input) anyerror!void,
    resize_fn: *const fn (*anyopaque, u16, u16) anyerror!void,
    cancel_fn: *const fn (*anyopaque, []const u8) anyerror!void,
    poll_fn: *const fn (*anyopaque, std.mem.Allocator) anyerror![]Frame,
    close_fn: ?*const fn (*anyopaque) void = null,

    pub fn send(self: TransportSession, input: Input) !void {
        return self.send_fn(self.context, input);
    }

    pub fn resize(self: TransportSession, cols: u16, rows: u16) !void {
        return self.resize_fn(self.context, cols, rows);
    }

    pub fn cancel(self: TransportSession, reason: []const u8) !void {
        return self.cancel_fn(self.context, reason);
    }

    pub fn poll(self: TransportSession, allocator: std.mem.Allocator) ![]Frame {
        return self.poll_fn(self.context, allocator);
    }

    pub fn close(self: TransportSession) void {
        if (self.close_fn) |close_fn| close_fn(self.context);
    }
};

pub const Transport = struct {
    name: TransportName,
    context: *anyopaque,
    detect_fn: *const fn (*anyopaque, []const u8, std.mem.Allocator) anyerror!Capabilities,
    open_fn: *const fn (*anyopaque, OpenSpec, std.mem.Allocator) anyerror!TransportSession,

    pub fn detect(self: Transport, provider: []const u8, allocator: std.mem.Allocator) !Capabilities {
        return self.detect_fn(self.context, provider, allocator);
    }

    pub fn open(self: Transport, spec: OpenSpec, allocator: std.mem.Allocator) !TransportSession {
        return self.open_fn(self.context, spec, allocator);
    }
};

pub const EnvVar = struct {
    name: []const u8,
    value: []const u8,
};

pub const AuthorityPlane = struct {
    socket_path: ?[]const u8 = null,
    token: ?[]const u8 = null,
    project_id: ?[]const u8 = null,
    run_id: ?[]const u8 = null,
};

pub const LaunchPolicy = struct {
    tier1_launch_config: ?[]const u8 = null,
    tier2_fast_path_rules: []const []const u8 = &.{},
    redaction_patterns: []const []const u8 = &.{},
};

pub const LaunchSpec = struct {
    version: u32 = protocol_version,
    provider: []const u8,
    task: []const u8,
    transport_order: []const TransportName,
    cwd: ?[]const u8 = null,
    env: []const EnvVar = &.{},
    cols: u16 = 80,
    rows: u16 = 24,
    idle_ms: i64 = 250,
    argv: []const []const u8 = &.{},
    policy: LaunchPolicy = .{},
    authority: AuthorityPlane = .{},
};

pub const TypedEventName = enum {
    run_started,
    process_spawned,
    raw_ref,
    snapshot,
    prompt_detected,
    action_requested,
    structured_output,
    final_output,
    terminal_output,
    sandbox_event,
    resource_sample,
    process_exited,
    error_reported,

    pub fn wireName(self: TypedEventName) []const u8 {
        return switch (self) {
            .run_started => "RUN_STARTED",
            .process_spawned => "PROCESS_SPAWNED",
            .raw_ref => "RAW_REF",
            .snapshot => "SNAPSHOT",
            .prompt_detected => "PROMPT_DETECTED",
            .action_requested => "ACTION_REQUESTED",
            .structured_output => "STRUCTURED_OUTPUT",
            .final_output => "FINAL_OUTPUT",
            .terminal_output => "TERMINAL_OUTPUT",
            .sandbox_event => "SANDBOX_EVENT",
            .resource_sample => "RESOURCE_SAMPLE",
            .process_exited => "PROCESS_EXITED",
            .error_reported => "ERROR",
        };
    }
};

test "named transports decompose into independent axes" {
    const pty_axes = TransportName.pty.axes();
    try std.testing.expectEqual(TransportKind.pty, pty_axes.transport);
    try std.testing.expectEqual(CodecKind.vt_screen, pty_axes.codec);
    try std.testing.expectEqual(LifecycleKind.durable_session, pty_axes.lifecycle);

    const stdio_axes = TransportName.stdio_json.axes();
    try std.testing.expectEqual(TransportKind.stdio, stdio_axes.transport);
    try std.testing.expectEqual(CodecKind.json_stream, stdio_axes.codec);
    try std.testing.expectEqual(LifecycleKind.one_shot, stdio_axes.lifecycle);
}

test "transport names parse stable wire aliases" {
    try std.testing.expectEqual(TransportName.stdio_json, TransportName.fromName("stdio-json").?);
    try std.testing.expectEqual(TransportName.stdio_json, TransportName.fromName("exec_json").?);
    try std.testing.expectEqual(TransportName.pty, TransportName.fromName("pty").?);
    try std.testing.expect(TransportName.fromName("native_api") == null);
}
