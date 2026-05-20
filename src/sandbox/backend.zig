const std = @import("std");
const runtime = @import("../runtime/types.zig");

pub const BackendId = enum {
    none,
    host_process,
    nucleus,
    remote,
    vm,
};

pub const SandboxMode = enum {
    none,
    host_process,
    strict_agent,
};

pub const SandboxRuntime = enum {
    none,
    native,
    gvisor,
};

pub const NetworkMode = enum {
    none,
    host,
    bridge,
};

pub const MountAccess = enum {
    read_only,
    read_write,
    read_write_execute,
};

pub const MountType = enum {
    bind,
    tmpfs,
    secret,
};

pub const MountSpec = struct {
    host_path: ?[]const u8 = null,
    container_path: []const u8,
    access: MountAccess,
    type: MountType = .bind,
    optional: bool = false,
};

pub const ResourceLimits = struct {
    memory: []const u8,
    cpus: u16,
    pids: u32,
};

pub const SecretMount = struct {
    name: []const u8,
    host_path: []const u8,
    container_path: []const u8,
};

pub const EnvVar = struct {
    name: []const u8,
    value: []const u8,
};

pub const SandboxCommand = struct {
    argv: []const []const u8,
    cwd: []const u8,
    environment: []const EnvVar = &.{},
};

pub const SandboxLaunchConfig = struct {
    run_id: runtime.RunId,
    backend: BackendId,
    mode: SandboxMode,
    runtime: SandboxRuntime,
    command: SandboxCommand,
    mounts: []const MountSpec,
    resources: ?ResourceLimits = null,
    network: NetworkMode,
    secrets: []const SecretMount = &.{},
};

pub const SandboxCapabilities = struct {
    supports_pty: bool = false,
    supports_headless: bool = false,
    supports_resource_limits: bool = false,
    supports_network_policy: bool = false,
    supports_secret_mounts: bool = false,
};

pub const SandboxHandle = struct {
    run_id: runtime.RunId,
    backend: BackendId,
    sandbox_id: []const u8,
    process_id: ?u32 = null,
    workspace_mount: []const u8,
    event_stream: ?SandboxEventStream = null,
    io: SandboxIo = .{},
};

pub const SandboxEventClass = enum {
    lifecycle,
    filesystem,
    namespace,
    resource,
    credential,
    network,
    security,
    audit,
};

pub const SandboxEvent = struct {
    timestamp_ms: runtime.TimestampMs,
    run_id: runtime.RunId,
    sandbox_id: []const u8,
    event_class: SandboxEventClass,
    event_type: []const u8,
    detail: ?[]const u8 = null,
    is_error: bool = false,
};

pub const SandboxEventStream = struct {
    context: *anyopaque,
    next: *const fn (*anyopaque, std.mem.Allocator) anyerror!?SandboxEvent,
};

pub const ByteReader = struct {
    context: *anyopaque,
    read: *const fn (*anyopaque, []u8) anyerror!usize,
};

pub const ByteWriter = struct {
    context: *anyopaque,
    write: *const fn (*anyopaque, []const u8) anyerror!usize,
};

pub const PtyIo = struct {
    reader: ByteReader,
    writer: ByteWriter,
    resize: *const fn (*anyopaque, u16, u16) anyerror!void,
    context: *anyopaque,
};

pub const SandboxIo = struct {
    stdin: ?ByteWriter = null,
    stdout: ?ByteReader = null,
    stderr: ?ByteReader = null,
    pty: ?PtyIo = null,
};

pub const SandboxBackend = struct {
    id: BackendId,
    capabilities: SandboxCapabilities,
    launch: *const fn (*SandboxBackend, SandboxLaunchConfig) anyerror!SandboxHandle,
    stop: *const fn (*SandboxBackend, SandboxHandle) anyerror!void,
};

test "strict agent nucleus launch config keeps command contract generic" {
    const run_id = try runtime.RunId.init("01HXYZ");
    const command: SandboxCommand = .{
        .argv = &.{ "agent", "--headless" },
        .cwd = "/workspace",
    };
    const mount: MountSpec = .{
        .host_path = "/repo/.agent-worktrees/01HXYZ",
        .container_path = "/workspace",
        .access = .read_write_execute,
    };
    const launch: SandboxLaunchConfig = .{
        .run_id = run_id,
        .backend = .nucleus,
        .mode = .strict_agent,
        .runtime = .gvisor,
        .command = command,
        .mounts = &.{mount},
        .resources = .{ .memory = "4G", .cpus = 4, .pids = 1024 },
        .network = .none,
    };

    try std.testing.expectEqual(BackendId.nucleus, launch.backend);
    try std.testing.expectEqualStrings("/workspace", launch.command.cwd);
    try std.testing.expectEqual(MountAccess.read_write_execute, launch.mounts[0].access);
}
