const std = @import("std");
const config = @import("../config/types.zig");
const runtime = @import("../runtime/types.zig");
const sandbox = @import("backend.zig");
const trace = @import("../trace/types.zig");
const workspace = @import("../workspace/worktree.zig");

pub const supported_contract_version = "0.1";
pub const default_container_prefix = "atr-";
pub const default_workspace_mount = "/workspace";
pub const default_context_mount = "/context";
pub const default_home_mount = "/home/agent";
pub const default_events_format = "jsonl";
pub const default_events_channel = "sidecar";

pub const NucleusError = error{
    InvalidContractVersion,
    UnsupportedContractVersion,
    InvalidTimestamp,
    InvalidLaunchConfig,
    InvalidLaunchResult,
    NucleusUnavailable,
};

pub const ContractVersion = struct {
    major: u16,
    minor: u16,
};

pub const NucleusMount = struct {
    name: []const u8,
    host_path: ?[]const u8 = null,
    container_path: []const u8,
    access: sandbox.MountAccess,
    type: sandbox.MountType = .bind,
    optional: bool = false,
};

pub const NucleusLaunchConfig = struct {
    contract_version: []const u8,
    run_id: runtime.RunId,
    container_name: []const u8,
    mode: sandbox.SandboxMode,
    runtime_backend: sandbox.SandboxRuntime,
    command: sandbox.SandboxCommand,
    mounts: []const NucleusMount,
    environment: []const sandbox.EnvVar,
    resources: ?sandbox.ResourceLimits,
    network: sandbox.NetworkMode,
    toolchain_rootfs: []const u8,
    events_format: []const u8 = default_events_format,
    events_channel: []const u8 = default_events_channel,

    pub fn deinit(self: NucleusLaunchConfig, allocator: std.mem.Allocator) void {
        allocator.free(self.contract_version);
        allocator.free(self.run_id.value);
        allocator.free(self.container_name);
        for (self.command.argv) |arg| allocator.free(arg);
        allocator.free(self.command.argv);
        allocator.free(self.command.cwd);
        for (self.command.environment) |env| {
            allocator.free(env.name);
            allocator.free(env.value);
        }
        if (self.command.environment.len > 0) allocator.free(self.command.environment);
        for (self.mounts) |mount| {
            allocator.free(mount.name);
            if (mount.host_path) |host_path| allocator.free(host_path);
            allocator.free(mount.container_path);
        }
        allocator.free(self.mounts);
        for (self.environment) |env| {
            allocator.free(env.name);
            allocator.free(env.value);
        }
        allocator.free(self.environment);
        if (self.resources) |resources| allocator.free(resources.memory);
        allocator.free(self.toolchain_rootfs);
        allocator.free(self.events_format);
        allocator.free(self.events_channel);
    }
};

pub const NucleusResourceSample = struct {
    cpu_millis: ?u64 = null,
    memory_bytes: ?u64 = null,
    pids: ?u32 = null,
    io_read_bytes: ?u64 = null,
    io_write_bytes: ?u64 = null,
    network_rx_bytes: ?u64 = null,
    network_tx_bytes: ?u64 = null,
};

pub const NucleusEvent = struct {
    timestamp: []const u8,
    run_id: []const u8,
    container_id: []const u8,
    event_type: []const u8,
    detail: ?[]const u8 = null,
    is_error: bool = false,
    resources: ?NucleusResourceSample = null,
};

pub const NormalizedNucleusEvent = struct {
    arena: std.heap.ArenaAllocator,
    raw: NucleusEvent,
    sandbox_event: sandbox.SandboxEvent,
    runtime_event: runtime.RuntimeEvent,

    pub fn deinit(self: *NormalizedNucleusEvent) void {
        self.arena.deinit();
        self.* = undefined;
    }
};

pub const NucleusLaunchResult = struct {
    container_id: []const u8,
    process_id: ?u32 = null,
    event_jsonl: ?[]const u8 = null,
};

pub const NucleusTransport = struct {
    context: *anyopaque,
    launch: *const fn (*anyopaque, []const u8, std.mem.Allocator) anyerror!NucleusLaunchResult,
    stop: *const fn (*anyopaque, []const u8) anyerror!void,
    deinit_result: ?*const fn (*anyopaque, std.mem.Allocator, NucleusLaunchResult) void = null,
};

pub const NucleusCliTransport = struct {
    allocator: std.mem.Allocator = std.heap.page_allocator,
    executable: []const u8 = "nucleus",
    launch_args: []const []const u8 = &.{ "strict-agent", "launch", "--config-json" },
    stop_args: []const []const u8 = &.{ "strict-agent", "stop" },
    max_output_bytes: usize = 16 * 1024 * 1024,

    pub fn transport(self: *NucleusCliTransport) NucleusTransport {
        return .{
            .context = self,
            .launch = launch,
            .stop = stop,
            .deinit_result = deinitResult,
        };
    }

    fn launch(context: *anyopaque, launch_json: []const u8, allocator: std.mem.Allocator) anyerror!NucleusLaunchResult {
        const self: *NucleusCliTransport = @ptrCast(@alignCast(context));
        var argv: std.ArrayList([]const u8) = .empty;
        defer argv.deinit(allocator);

        try argv.append(allocator, self.executable);
        try argv.appendSlice(allocator, self.launch_args);
        try argv.append(allocator, launch_json);

        const result = std.process.Child.run(.{
            .allocator = allocator,
            .argv = argv.items,
            .max_output_bytes = self.max_output_bytes,
        }) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return error.NucleusUnavailable,
        };
        defer allocator.free(result.stdout);
        defer allocator.free(result.stderr);
        try expectCliExitedZero(result.term);

        return parseLaunchResultJson(allocator, result.stdout);
    }

    fn stop(context: *anyopaque, container_id: []const u8) anyerror!void {
        const self: *NucleusCliTransport = @ptrCast(@alignCast(context));
        var argv: std.ArrayList([]const u8) = .empty;
        defer argv.deinit(self.allocator);

        try argv.append(self.allocator, self.executable);
        try argv.appendSlice(self.allocator, self.stop_args);
        try argv.append(self.allocator, container_id);

        const result = std.process.Child.run(.{
            .allocator = self.allocator,
            .argv = argv.items,
            .max_output_bytes = self.max_output_bytes,
        }) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return error.NucleusUnavailable,
        };
        defer self.allocator.free(result.stdout);
        defer self.allocator.free(result.stderr);
        try expectCliExitedZero(result.term);
    }

    fn deinitResult(_: *anyopaque, allocator: std.mem.Allocator, result: NucleusLaunchResult) void {
        deinitNucleusLaunchResult(allocator, result);
    }
};

pub const NucleusBackend = struct {
    allocator: std.mem.Allocator,
    backend: sandbox.SandboxBackend,
    transport: ?NucleusTransport = null,
    last_launch_json: ?[]u8 = null,

    pub fn init(allocator: std.mem.Allocator, transport: ?NucleusTransport) NucleusBackend {
        return .{
            .allocator = allocator,
            .backend = .{
                .id = .nucleus,
                .capabilities = backendCapabilities(),
                .launch = launchBackend,
                .stop = stopBackend,
            },
            .transport = transport,
        };
    }

    pub fn deinit(self: *NucleusBackend) void {
        if (self.last_launch_json) |json| self.allocator.free(json);
        self.* = undefined;
    }
};

pub const JsonlSandboxEventStream = struct {
    bytes: []const u8,
    cursor: usize = 0,

    pub fn init(allocator: std.mem.Allocator, bytes: []const u8) !*JsonlSandboxEventStream {
        const stream = try allocator.create(JsonlSandboxEventStream);
        errdefer allocator.destroy(stream);
        stream.* = .{
            .bytes = try allocator.dupe(u8, bytes),
        };
        return stream;
    }

    pub fn deinit(self: *JsonlSandboxEventStream, allocator: std.mem.Allocator) void {
        allocator.free(self.bytes);
        allocator.destroy(self);
    }

    pub fn eventStream(self: *JsonlSandboxEventStream) sandbox.SandboxEventStream {
        return .{
            .context = self,
            .next = nextSandboxEvent,
            .deinit_event = deinitStreamEvent,
        };
    }
};

pub fn backendCapabilities() sandbox.SandboxCapabilities {
    return .{
        .supports_pty = true,
        .supports_headless = true,
        .supports_resource_limits = true,
        .supports_network_policy = true,
        .supports_secret_mounts = true,
    };
}

pub fn deinitSandboxEvent(allocator: std.mem.Allocator, event: sandbox.SandboxEvent) void {
    allocator.free(event.run_id.value);
    allocator.free(event.sandbox_id);
    allocator.free(event.event_type);
    if (event.detail) |detail| allocator.free(detail);
}

pub fn parseContractVersion(value: []const u8) NucleusError!ContractVersion {
    var trimmed = std.mem.trim(u8, value, " \t\r\n");
    if (trimmed.len == 0) return error.InvalidContractVersion;
    if (std.mem.lastIndexOfScalar(u8, trimmed, '/')) |slash| trimmed = trimmed[slash + 1 ..];
    if (trimmed.len > 0 and trimmed[0] == 'v') trimmed = trimmed[1..];

    const dot = std.mem.indexOfScalar(u8, trimmed, '.') orelse return error.InvalidContractVersion;
    if (dot == 0 or dot == trimmed.len - 1) return error.InvalidContractVersion;
    return .{
        .major = std.fmt.parseInt(u16, trimmed[0..dot], 10) catch return error.InvalidContractVersion,
        .minor = std.fmt.parseInt(u16, trimmed[dot + 1 ..], 10) catch return error.InvalidContractVersion,
    };
}

pub fn checkContractVersion(reported: []const u8) NucleusError!void {
    const supported = try parseContractVersion(supported_contract_version);
    const version = try parseContractVersion(reported);
    if (version.major != supported.major or version.minor != supported.minor) return error.UnsupportedContractVersion;
}

pub fn buildLaunchConfig(
    allocator: std.mem.Allocator,
    project: config.ProjectConfig,
    plan: workspace.WorktreePlan,
    command: sandbox.SandboxCommand,
) !NucleusLaunchConfig {
    try project.validate();
    if (project.sandbox.backend != .nucleus or project.sandbox.mode != .strict_agent) return error.InvalidLaunchConfig;
    if (!std.mem.eql(u8, plan.container_mount, default_workspace_mount)) return error.InvalidLaunchConfig;
    if (!std.mem.eql(u8, command.cwd, default_workspace_mount)) return error.InvalidLaunchConfig;

    const run_id = runtime.RunId{ .value = try allocator.dupe(u8, plan.run_id.value) };
    errdefer allocator.free(run_id.value);

    const container_name = try std.fmt.allocPrint(allocator, "{s}{s}", .{ default_container_prefix, plan.run_id.value });
    errdefer allocator.free(container_name);

    const launch_command = try copyCommand(allocator, command);
    errdefer freeCommand(allocator, launch_command);

    const mounts = try buildMounts(allocator, plan);
    errdefer freeMounts(allocator, mounts);

    const environment = try buildLaunchEnvironment(allocator, project.sandbox.home, plan.container_mount, command.environment);
    errdefer freeEnv(allocator, environment);

    const resources: sandbox.ResourceLimits = .{
        .memory = try allocator.dupe(u8, project.sandbox.resources.memory),
        .cpus = project.sandbox.resources.cpus,
        .pids = project.sandbox.resources.pids,
    };
    errdefer allocator.free(resources.memory);

    return .{
        .contract_version = try allocator.dupe(u8, supported_contract_version),
        .run_id = run_id,
        .container_name = container_name,
        .mode = project.sandbox.mode,
        .runtime_backend = project.sandbox.runtime,
        .command = launch_command,
        .mounts = mounts,
        .environment = environment,
        .resources = resources,
        .network = project.sandbox.network,
        .toolchain_rootfs = try allocator.dupe(u8, project.sandbox.toolchain_rootfs),
        .events_format = try allocator.dupe(u8, default_events_format),
        .events_channel = try allocator.dupe(u8, default_events_channel),
    };
}

pub fn buildLaunchConfigFromSandbox(
    allocator: std.mem.Allocator,
    launch: sandbox.SandboxLaunchConfig,
) !NucleusLaunchConfig {
    if (launch.backend != .nucleus or launch.mode != .strict_agent) return error.InvalidLaunchConfig;
    if (!std.mem.eql(u8, launch.command.cwd, default_workspace_mount)) return error.InvalidLaunchConfig;

    const workspace_mount = findWorkspaceMount(launch.mounts) orelse return error.InvalidLaunchConfig;

    const run_id = runtime.RunId{ .value = try allocator.dupe(u8, launch.run_id.value) };
    errdefer allocator.free(run_id.value);
    const container_name = try std.fmt.allocPrint(allocator, "{s}{s}", .{ default_container_prefix, launch.run_id.value });
    errdefer allocator.free(container_name);
    const command = try copyCommand(allocator, launch.command);
    errdefer freeCommand(allocator, command);
    const mounts = try buildMountsFromSandbox(allocator, launch.mounts);
    errdefer freeMounts(allocator, mounts);
    const environment = try buildLaunchEnvironment(allocator, default_home_mount, workspace_mount.container_path, launch.command.environment);
    errdefer freeEnv(allocator, environment);

    var resources: ?sandbox.ResourceLimits = null;
    if (launch.resources) |limits| {
        resources = .{
            .memory = try allocator.dupe(u8, limits.memory),
            .cpus = limits.cpus,
            .pids = limits.pids,
        };
    }
    errdefer if (resources) |limits| allocator.free(limits.memory);

    return .{
        .contract_version = try allocator.dupe(u8, supported_contract_version),
        .run_id = run_id,
        .container_name = container_name,
        .mode = launch.mode,
        .runtime_backend = launch.runtime,
        .command = command,
        .mounts = mounts,
        .environment = environment,
        .resources = resources,
        .network = launch.network,
        .toolchain_rootfs = try allocator.dupe(u8, "unspecified"),
        .events_format = try allocator.dupe(u8, default_events_format),
        .events_channel = try allocator.dupe(u8, default_events_channel),
    };
}

pub fn renderLaunchJson(allocator: std.mem.Allocator, launch: NucleusLaunchConfig) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();

    var json: std.json.Stringify = .{
        .writer = &out.writer,
        .options = .{ .whitespace = .indent_2 },
    };

    try json.beginObject();
    try json.objectField("contract_version");
    try json.write(launch.contract_version);
    try json.objectField("run_id");
    try json.write(launch.run_id.value);
    try json.objectField("container_name");
    try json.write(launch.container_name);
    try json.objectField("mode");
    try json.write(sandboxModeName(launch.mode));
    try json.objectField("runtime");
    try json.write(sandboxRuntimeName(launch.runtime_backend));

    try json.objectField("command");
    try writeCommand(&json, launch.command);

    try json.objectField("mounts");
    try writeMounts(&json, launch.mounts);

    try json.objectField("environment");
    try writeEnvironment(&json, launch.environment);

    try json.objectField("resources");
    if (launch.resources) |resources| {
        try json.beginObject();
        try json.objectField("memory");
        try json.write(resources.memory);
        try json.objectField("cpus");
        try json.write(resources.cpus);
        try json.objectField("pids");
        try json.write(resources.pids);
        try json.endObject();
    } else {
        try json.write(null);
    }

    try json.objectField("network");
    try json.beginObject();
    try json.objectField("mode");
    try json.write(networkModeName(launch.network));
    try json.endObject();

    try json.objectField("events");
    try json.beginObject();
    try json.objectField("format");
    try json.write(launch.events_format);
    try json.objectField("channel");
    try json.write(launch.events_channel);
    try json.endObject();

    try json.objectField("toolchain_rootfs");
    try json.write(launch.toolchain_rootfs);
    try json.endObject();

    return out.toOwnedSlice();
}

pub fn parseLaunchResultJson(allocator: std.mem.Allocator, stdout: []const u8) !NucleusLaunchResult {
    const trimmed = std.mem.trim(u8, stdout, " \t\r\n");
    if (trimmed.len == 0) return error.InvalidLaunchResult;

    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(), trimmed, .{
        .ignore_unknown_fields = true,
    }) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return error.InvalidLaunchResult,
    };

    const container_id_value = jsonObjectField(parsed, "container_id") orelse return error.InvalidLaunchResult;
    const container_id_text = jsonStringValue(container_id_value) orelse return error.InvalidLaunchResult;
    if (container_id_text.len == 0) return error.InvalidLaunchResult;

    const container_id = try allocator.dupe(u8, container_id_text);
    errdefer allocator.free(container_id);

    var process_id: ?u32 = null;
    if (jsonObjectField(parsed, "process_id")) |value| {
        process_id = switch (value) {
            .null => null,
            .integer => |integer| blk: {
                if (integer < 0 or integer > @as(i64, std.math.maxInt(u32))) return error.InvalidLaunchResult;
                break :blk @intCast(integer);
            },
            else => return error.InvalidLaunchResult,
        };
    }

    var event_jsonl: ?[]u8 = null;
    errdefer if (event_jsonl) |jsonl| allocator.free(jsonl);
    if (jsonObjectField(parsed, "event_jsonl")) |value| {
        event_jsonl = switch (value) {
            .null => null,
            .string => |text| try allocator.dupe(u8, text),
            else => return error.InvalidLaunchResult,
        };
    }

    return .{
        .container_id = container_id,
        .process_id = process_id,
        .event_jsonl = event_jsonl,
    };
}

pub fn deinitNucleusLaunchResult(allocator: std.mem.Allocator, result: NucleusLaunchResult) void {
    allocator.free(result.container_id);
    if (result.event_jsonl) |jsonl| allocator.free(jsonl);
}

pub fn ingestEventLine(
    allocator: std.mem.Allocator,
    writer: *trace.TraceWriter,
    sequence: *runtime.Sequence,
    line: []const u8,
) !void {
    var normalized = try normalizeEventLine(allocator, line);
    defer normalized.deinit();

    try writer.append_jsonl(writer, .sandbox_events, line);

    const record: runtime.RuntimeEventRecord = .{
        .run_id = normalized.sandbox_event.run_id,
        .sequence = sequence.*,
        .timestamp_ms = normalized.sandbox_event.timestamp_ms,
        .event = normalized.runtime_event,
    };
    try writer.append_event(writer, record);
    sequence.* += 1;
}

pub fn ingestEventJsonl(
    allocator: std.mem.Allocator,
    writer: *trace.TraceWriter,
    sequence: *runtime.Sequence,
    jsonl: []const u8,
) !usize {
    var count: usize = 0;
    var lines = std.mem.splitScalar(u8, jsonl, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;
        try ingestEventLine(allocator, writer, sequence, line);
        count += 1;
    }
    return count;
}

pub fn normalizeEventLine(allocator: std.mem.Allocator, line: []const u8) !NormalizedNucleusEvent {
    var normalized: NormalizedNucleusEvent = .{
        .arena = std.heap.ArenaAllocator.init(allocator),
        .raw = undefined,
        .sandbox_event = undefined,
        .runtime_event = undefined,
    };
    errdefer normalized.arena.deinit();

    const arena = normalized.arena.allocator();
    normalized.raw = try std.json.parseFromSliceLeaky(NucleusEvent, arena, line, .{
        .ignore_unknown_fields = true,
    });
    const timestamp_ms = try parseRfc3339Millis(normalized.raw.timestamp);
    const run_id = try runtime.RunId.init(normalized.raw.run_id);
    const detail = if (normalized.raw.detail) |detail| try arena.dupe(u8, detail) else null;

    normalized.sandbox_event = .{
        .timestamp_ms = timestamp_ms,
        .run_id = run_id,
        .sandbox_id = try arena.dupe(u8, normalized.raw.container_id),
        .event_class = classifyEvent(normalized.raw.event_type),
        .event_type = try arena.dupe(u8, normalized.raw.event_type),
        .detail = detail,
        .is_error = normalized.raw.is_error,
    };

    normalized.runtime_event = mapRuntimeEvent(normalized.raw, detail);
    return normalized;
}

fn mapRuntimeEvent(event: NucleusEvent, detail: ?[]const u8) runtime.RuntimeEvent {
    if (std.mem.eql(u8, event.event_type, "container_start")) {
        return .{ .sandbox_prepared = .{
            .backend = "nucleus",
            .mode = "strict-agent",
            .workspace_mount = default_workspace_mount,
        } };
    }
    if (std.mem.eql(u8, event.event_type, "resource_sample")) {
        const resources = event.resources orelse NucleusResourceSample{};
        return .{ .resource_sample = .{
            .memory_bytes = resources.memory_bytes,
            .cpu_millis = resources.cpu_millis,
            .pids = resources.pids,
        } };
    }
    return .{ .sandbox_event = .{
        .event_type = event.event_type,
        .detail = detail,
        .is_error = event.is_error,
    } };
}

fn classifyEvent(event_type: []const u8) sandbox.SandboxEventClass {
    if (std.mem.eql(u8, event_type, "container_start") or std.mem.eql(u8, event_type, "container_stop")) return .lifecycle;
    if (std.mem.eql(u8, event_type, "namespace_created")) return .namespace;
    if (std.mem.eql(u8, event_type, "filesystem_mounted") or std.mem.eql(u8, event_type, "mount_audit_passed")) return .filesystem;
    if (std.mem.eql(u8, event_type, "cgroup_created") or std.mem.eql(u8, event_type, "resource_sample")) return .resource;
    if (std.mem.eql(u8, event_type, "secrets_mounted")) return .credential;
    if (std.mem.eql(u8, event_type, "network_bridge_setup") or std.mem.eql(u8, event_type, "egress_policy_applied") or std.mem.eql(u8, event_type, "egress_denied")) return .network;
    if (std.mem.eql(u8, event_type, "capabilities_dropped") or std.mem.eql(u8, event_type, "seccomp_applied") or std.mem.eql(u8, event_type, "seccomp_profile_loaded") or std.mem.eql(u8, event_type, "landlock_applied") or std.mem.eql(u8, event_type, "no_new_privs_set")) return .security;
    return .audit;
}

fn launchBackend(backend: *sandbox.SandboxBackend, launch: sandbox.SandboxLaunchConfig) anyerror!sandbox.SandboxHandle {
    const self: *NucleusBackend = @alignCast(@fieldParentPtr("backend", backend));
    const transport = self.transport orelse return error.NucleusUnavailable;

    const contract = try buildLaunchConfigFromSandbox(self.allocator, launch);
    defer contract.deinit(self.allocator);

    const json = try renderLaunchJson(self.allocator, contract);
    errdefer self.allocator.free(json);
    if (self.last_launch_json) |previous| self.allocator.free(previous);
    self.last_launch_json = json;

    const result = try transport.launch(transport.context, json, self.allocator);
    defer if (transport.deinit_result) |deinit_result| deinit_result(transport.context, self.allocator, result);
    const sandbox_id = try self.allocator.dupe(u8, result.container_id);
    errdefer self.allocator.free(sandbox_id);
    const workspace_mount = try self.allocator.dupe(u8, default_workspace_mount);
    errdefer self.allocator.free(workspace_mount);

    var event_stream: ?sandbox.SandboxEventStream = null;
    if (result.event_jsonl) |jsonl| {
        const stream = try JsonlSandboxEventStream.init(self.allocator, jsonl);
        event_stream = stream.eventStream();
    }

    return .{
        .run_id = launch.run_id,
        .backend = .nucleus,
        .sandbox_id = sandbox_id,
        .process_id = result.process_id,
        .workspace_mount = workspace_mount,
        .event_stream = event_stream,
    };
}

fn stopBackend(backend: *sandbox.SandboxBackend, handle: sandbox.SandboxHandle) anyerror!void {
    const self: *NucleusBackend = @alignCast(@fieldParentPtr("backend", backend));
    if (self.transport) |transport| try transport.stop(transport.context, handle.sandbox_id);
    if (handle.event_stream) |stream| {
        const event_stream: *JsonlSandboxEventStream = @ptrCast(@alignCast(stream.context));
        event_stream.deinit(self.allocator);
    }
    self.allocator.free(handle.sandbox_id);
    self.allocator.free(handle.workspace_mount);
}

fn nextSandboxEvent(context: *anyopaque, allocator: std.mem.Allocator) anyerror!?sandbox.SandboxEvent {
    const self: *JsonlSandboxEventStream = @ptrCast(@alignCast(context));
    while (self.cursor < self.bytes.len) {
        const start = self.cursor;
        const maybe_end = std.mem.indexOfScalarPos(u8, self.bytes, start, '\n');
        const end = maybe_end orelse self.bytes.len;
        self.cursor = if (maybe_end == null) self.bytes.len else end + 1;

        const line = std.mem.trim(u8, self.bytes[start..end], " \t\r");
        if (line.len == 0) continue;

        var normalized = try normalizeEventLine(allocator, line);
        defer normalized.deinit();
        return try dupeSandboxEvent(allocator, normalized.sandbox_event);
    }
    return null;
}

fn deinitStreamEvent(_: *anyopaque, allocator: std.mem.Allocator, event: sandbox.SandboxEvent) void {
    deinitSandboxEvent(allocator, event);
}

fn dupeSandboxEvent(allocator: std.mem.Allocator, event: sandbox.SandboxEvent) !sandbox.SandboxEvent {
    const run_id_value = try allocator.dupe(u8, event.run_id.value);
    errdefer allocator.free(run_id_value);
    const sandbox_id = try allocator.dupe(u8, event.sandbox_id);
    errdefer allocator.free(sandbox_id);
    const event_type = try allocator.dupe(u8, event.event_type);
    errdefer allocator.free(event_type);
    const detail = if (event.detail) |value| try allocator.dupe(u8, value) else null;
    errdefer if (detail) |value| allocator.free(value);

    return .{
        .timestamp_ms = event.timestamp_ms,
        .run_id = .{ .value = run_id_value },
        .sandbox_id = sandbox_id,
        .event_class = event.event_class,
        .event_type = event_type,
        .detail = detail,
        .is_error = event.is_error,
    };
}

fn findWorkspaceMount(mounts: []const sandbox.MountSpec) ?sandbox.MountSpec {
    for (mounts) |mount| {
        if (std.mem.eql(u8, mount.container_path, default_workspace_mount)) return mount;
    }
    return null;
}

fn buildMountsFromSandbox(allocator: std.mem.Allocator, mounts: []const sandbox.MountSpec) ![]const NucleusMount {
    const result = try allocator.alloc(NucleusMount, mounts.len);
    errdefer allocator.free(result);
    for (mounts, 0..) |mount, index| {
        result[index] = .{
            .name = try mountNameForPath(allocator, mount.container_path, index),
            .host_path = if (mount.host_path) |host_path| try allocator.dupe(u8, host_path) else null,
            .container_path = try allocator.dupe(u8, mount.container_path),
            .access = mount.access,
            .type = mount.type,
            .optional = mount.optional,
        };
    }
    return result;
}

fn mountNameForPath(allocator: std.mem.Allocator, container_path: []const u8, index: usize) ![]const u8 {
    if (std.mem.eql(u8, container_path, default_workspace_mount)) return allocator.dupe(u8, "workspace");
    if (std.mem.eql(u8, container_path, default_home_mount)) return allocator.dupe(u8, "home");
    if (std.mem.eql(u8, container_path, default_context_mount)) return allocator.dupe(u8, "context");
    if (std.mem.startsWith(u8, container_path, "/run/secrets")) return allocator.dupe(u8, "secret");
    return std.fmt.allocPrint(allocator, "mount_{d}", .{index});
}

fn copyCommand(allocator: std.mem.Allocator, command: sandbox.SandboxCommand) !sandbox.SandboxCommand {
    const argv = try allocator.alloc([]const u8, command.argv.len);
    errdefer allocator.free(argv);
    for (command.argv, 0..) |arg, index| {
        argv[index] = try allocator.dupe(u8, arg);
    }

    return .{
        .argv = argv,
        .cwd = try allocator.dupe(u8, command.cwd),
        .environment = try copyEnv(allocator, command.environment),
    };
}

fn freeCommand(allocator: std.mem.Allocator, command: sandbox.SandboxCommand) void {
    for (command.argv) |arg| allocator.free(arg);
    allocator.free(command.argv);
    allocator.free(command.cwd);
    freeEnv(allocator, command.environment);
}

fn buildMounts(allocator: std.mem.Allocator, plan: workspace.WorktreePlan) ![]const NucleusMount {
    const mounts = try allocator.alloc(NucleusMount, 3);
    errdefer allocator.free(mounts);
    mounts[0] = .{
        .name = try allocator.dupe(u8, "workspace"),
        .host_path = try allocator.dupe(u8, plan.host_path),
        .container_path = try allocator.dupe(u8, default_workspace_mount),
        .access = .read_write_execute,
    };
    mounts[1] = .{
        .name = try allocator.dupe(u8, "home"),
        .container_path = try allocator.dupe(u8, default_home_mount),
        .access = .read_write,
        .type = .tmpfs,
    };
    mounts[2] = .{
        .name = try allocator.dupe(u8, "context"),
        .host_path = try allocator.dupe(u8, plan.repo_root),
        .container_path = try allocator.dupe(u8, default_context_mount),
        .access = .read_only,
        .optional = true,
    };
    return mounts;
}

fn freeMounts(allocator: std.mem.Allocator, mounts: []const NucleusMount) void {
    for (mounts) |mount| {
        allocator.free(mount.name);
        if (mount.host_path) |host_path| allocator.free(host_path);
        allocator.free(mount.container_path);
    }
    allocator.free(mounts);
}

fn buildLaunchEnvironment(
    allocator: std.mem.Allocator,
    home: []const u8,
    workspace_mount: []const u8,
    extra: []const sandbox.EnvVar,
) ![]const sandbox.EnvVar {
    const env = try allocator.alloc(sandbox.EnvVar, 4 + extra.len);
    errdefer allocator.free(env);
    env[0] = try dupeEnv(allocator, "HOME", home);
    env[1] = try dupeEnv(allocator, "WORKSPACE", workspace_mount);
    env[2] = try dupeEnv(allocator, "TERM", "xterm-256color");
    env[3] = try dupeEnv(allocator, "LANG", "C.UTF-8");
    for (extra, 0..) |entry, index| {
        env[4 + index] = try dupeEnv(allocator, entry.name, entry.value);
    }
    return env;
}

fn copyEnv(allocator: std.mem.Allocator, source: []const sandbox.EnvVar) ![]const sandbox.EnvVar {
    if (source.len == 0) return &.{};
    const env = try allocator.alloc(sandbox.EnvVar, source.len);
    errdefer allocator.free(env);
    for (source, 0..) |entry, index| {
        env[index] = try dupeEnv(allocator, entry.name, entry.value);
    }
    return env;
}

fn dupeEnv(allocator: std.mem.Allocator, name: []const u8, value: []const u8) !sandbox.EnvVar {
    return .{
        .name = try allocator.dupe(u8, name),
        .value = try allocator.dupe(u8, value),
    };
}

fn freeEnv(allocator: std.mem.Allocator, env: []const sandbox.EnvVar) void {
    for (env) |entry| {
        allocator.free(entry.name);
        allocator.free(entry.value);
    }
    if (env.len > 0) allocator.free(env);
}

fn writeCommand(json: *std.json.Stringify, command: sandbox.SandboxCommand) !void {
    try json.beginObject();
    try json.objectField("argv");
    try json.beginArray();
    for (command.argv) |arg| try json.write(arg);
    try json.endArray();
    try json.objectField("cwd");
    try json.write(command.cwd);
    try json.endObject();
}

fn writeMounts(json: *std.json.Stringify, mounts: []const NucleusMount) !void {
    try json.beginObject();
    for (mounts) |mount| {
        try json.objectField(mount.name);
        try json.beginObject();
        if (mount.host_path) |host_path| {
            try json.objectField("host_path");
            try json.write(host_path);
        }
        try json.objectField("container_path");
        try json.write(mount.container_path);
        try json.objectField("access");
        try json.write(mountAccessName(mount.access));
        try json.objectField("type");
        try json.write(mountTypeName(mount.type));
        if (mount.optional) {
            try json.objectField("optional");
            try json.write(true);
        }
        try json.endObject();
    }
    try json.endObject();
}

fn writeEnvironment(json: *std.json.Stringify, env: []const sandbox.EnvVar) !void {
    try json.beginObject();
    for (env) |entry| {
        try json.objectField(entry.name);
        try json.write(entry.value);
    }
    try json.endObject();
}

fn sandboxModeName(mode: sandbox.SandboxMode) []const u8 {
    return switch (mode) {
        .none => "none",
        .host_process => "host-process",
        .strict_agent => "strict-agent",
    };
}

fn sandboxRuntimeName(runtime_backend: sandbox.SandboxRuntime) []const u8 {
    return switch (runtime_backend) {
        .none => "none",
        .native => "native",
        .gvisor => "gvisor",
    };
}

fn networkModeName(network: sandbox.NetworkMode) []const u8 {
    return switch (network) {
        .none => "none",
        .host => "host",
        .bridge => "bridge",
    };
}

fn mountAccessName(access: sandbox.MountAccess) []const u8 {
    return switch (access) {
        .read_only => "read_only",
        .read_write => "read_write",
        .read_write_execute => "read_write_execute",
    };
}

fn mountTypeName(mount_type: sandbox.MountType) []const u8 {
    return switch (mount_type) {
        .bind => "bind",
        .tmpfs => "tmpfs",
        .secret => "secret",
    };
}

fn jsonObjectField(value: std.json.Value, key: []const u8) ?std.json.Value {
    return switch (value) {
        .object => |object| object.get(key),
        else => null,
    };
}

fn jsonStringValue(value: std.json.Value) ?[]const u8 {
    return switch (value) {
        .string => |text| text,
        else => null,
    };
}

fn expectCliExitedZero(term: std.process.Child.Term) NucleusError!void {
    switch (term) {
        .Exited => |code| if (code == 0) return else return error.NucleusUnavailable,
        else => return error.NucleusUnavailable,
    }
}

fn parseRfc3339Millis(timestamp: []const u8) NucleusError!runtime.TimestampMs {
    if (timestamp.len < "0000-00-00T00:00:00Z".len) return error.InvalidTimestamp;
    if (timestamp[4] != '-' or timestamp[7] != '-' or timestamp[10] != 'T' or timestamp[13] != ':' or timestamp[16] != ':') return error.InvalidTimestamp;

    const year = std.fmt.parseInt(u16, timestamp[0..4], 10) catch return error.InvalidTimestamp;
    const month = std.fmt.parseInt(u8, timestamp[5..7], 10) catch return error.InvalidTimestamp;
    const day = std.fmt.parseInt(u8, timestamp[8..10], 10) catch return error.InvalidTimestamp;
    const hour = std.fmt.parseInt(u8, timestamp[11..13], 10) catch return error.InvalidTimestamp;
    const minute = std.fmt.parseInt(u8, timestamp[14..16], 10) catch return error.InvalidTimestamp;
    const second = std.fmt.parseInt(u8, timestamp[17..19], 10) catch return error.InvalidTimestamp;

    if (year < 1970 or month < 1 or month > 12 or day < 1 or hour > 23 or minute > 59 or second > 59) return error.InvalidTimestamp;

    var index: usize = 19;
    var millis: i64 = 0;
    if (index < timestamp.len and timestamp[index] == '.') {
        index += 1;
        var digits: u8 = 0;
        while (index < timestamp.len and timestamp[index] >= '0' and timestamp[index] <= '9') : (index += 1) {
            if (digits < 3) {
                millis = millis * 10 + @as(i64, timestamp[index] - '0');
                digits += 1;
            }
        }
        while (digits < 3) : (digits += 1) millis *= 10;
    }
    if (index >= timestamp.len or timestamp[index] != 'Z') return error.InvalidTimestamp;

    var days: i64 = 0;
    var year_iter: u16 = 1970;
    while (year_iter < year) : (year_iter += 1) days += daysInYear(year_iter);

    var month_iter: u8 = 1;
    while (month_iter < month) : (month_iter += 1) days += daysInMonth(year, month_iter);
    const days_in_month = daysInMonth(year, month);
    if (day > days_in_month) return error.InvalidTimestamp;
    days += @as(i64, day) - 1;

    const seconds = days * 86_400 + @as(i64, hour) * 3_600 + @as(i64, minute) * 60 + second;
    return seconds * 1_000 + millis;
}

fn daysInYear(year: u16) u16 {
    return if (isLeapYear(year)) 366 else 365;
}

fn daysInMonth(year: u16, month: u8) u8 {
    return switch (month) {
        1, 3, 5, 7, 8, 10, 12 => 31,
        4, 6, 9, 11 => 30,
        2 => if (isLeapYear(year)) 29 else 28,
        else => 0,
    };
}

fn isLeapYear(year: u16) bool {
    if (@mod(year, 4) != 0) return false;
    if (@mod(year, 100) != 0) return true;
    return @mod(year, 400) == 0;
}

test "nucleus contract version check is explicit" {
    try checkContractVersion("0.1");
    try checkContractVersion("mitos-nucleus-contract/v0.1");
    try std.testing.expectError(error.UnsupportedContractVersion, checkContractVersion("0.0"));
    try std.testing.expectError(error.InvalidContractVersion, checkContractVersion("garbage"));
}

test "nucleus launch config renders strict-agent contract json" {
    const allocator = std.testing.allocator;
    const run_id = try runtime.RunId.init("01HXYZ");
    const workspace_cfg: config.WorkspaceConfig = .{ .base_branch = "main" };
    const plan = try workspace.planGitWorktree(allocator, "/repo", run_id, workspace_cfg);
    defer plan.deinit(allocator);

    const project: config.ProjectConfig = .{
        .version = config.supported_version,
        .workspace = workspace_cfg,
        .sandbox = .{},
        .providers = &.{.{ .name = "fake", .adapter_order = &.{.exec_json} }},
        .policy = .{},
        .watcher = .{},
    };
    const command: sandbox.SandboxCommand = .{
        .argv = &.{ "fake-agent", "--run" },
        .cwd = "/workspace",
        .environment = &.{.{ .name = "PROVIDER_CONFIG", .value = "/run/secrets/fake" }},
    };

    const launch = try buildLaunchConfig(allocator, project, plan, command);
    defer launch.deinit(allocator);
    const json = try renderLaunchJson(allocator, launch);
    defer allocator.free(json);

    try std.testing.expect(std.mem.indexOf(u8, json, "\"container_name\": \"atr-01HXYZ\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, json, "\"workspace\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, json, "\"HOME\": \"/home/agent\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, json, "\"PROVIDER_CONFIG\": \"/run/secrets/fake\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, json, "\"toolchain_rootfs\": \"agent-zig-node\"") != null);
}

test "nucleus cli launch result parses owned fields" {
    const allocator = std.testing.allocator;
    const result = try parseLaunchResultJson(allocator,
        \\{"container_id":"cli-id","process_id":99,"event_jsonl":"{\"timestamp\":\"2026-05-20T18:00:01.000Z\",\"run_id\":\"01HXYZ\",\"container_id\":\"cli-id\",\"event_type\":\"container_start\",\"is_error\":false}\n"}
    );
    defer deinitNucleusLaunchResult(allocator, result);

    try std.testing.expectEqualStrings("cli-id", result.container_id);
    try std.testing.expectEqual(@as(?u32, 99), result.process_id);
    try std.testing.expect(result.event_jsonl != null);
}

test "nucleus cli transport invokes executable launch and stop" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const script =
        \\#!/bin/sh
        \\if [ "$1" = "launch" ]; then
        \\  printf '%s\n' '{"container_id":"cli-id","process_id":99}'
        \\  exit 0
        \\fi
        \\if [ "$1" = "stop" ] && [ "$2" = "cli-id" ]; then
        \\  exit 0
        \\fi
        \\exit 9
        \\
    ;
    {
        var file = try tmp.dir.createFile("nucleus-cli", .{});
        defer file.close();
        try file.writeAll(script);
        try file.chmod(0o755);
    }

    const cwd = try std.fs.cwd().realpathAlloc(allocator, ".");
    defer allocator.free(cwd);
    const executable = try std.fs.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "nucleus-cli" });
    defer allocator.free(executable);

    var cli: NucleusCliTransport = .{
        .allocator = allocator,
        .executable = executable,
        .launch_args = &.{"launch"},
        .stop_args = &.{"stop"},
    };
    const transport = cli.transport();

    const result = try transport.launch(transport.context, "{}", allocator);
    defer transport.deinit_result.?(transport.context, allocator, result);
    try std.testing.expectEqualStrings("cli-id", result.container_id);
    try std.testing.expectEqual(@as(?u32, 99), result.process_id);

    try transport.stop(transport.context, result.container_id);
}

test "nucleus jsonl lifecycle events normalize into runtime events" {
    const line =
        \\{"timestamp":"2026-05-20T18:00:01.123Z","run_id":"01HXYZ","container_id":"nucleus-id","event_type":"seccomp_applied","detail":"profile loaded","is_error":false}
    ;

    var normalized = try normalizeEventLine(std.testing.allocator, line);
    defer normalized.deinit();

    try std.testing.expectEqual(sandbox.SandboxEventClass.security, normalized.sandbox_event.event_class);
    try std.testing.expectEqualStrings("seccomp_applied", normalized.sandbox_event.event_type);
    try std.testing.expectEqual(@as(runtime.TimestampMs, 1_779_300_001_123), normalized.sandbox_event.timestamp_ms);
    switch (normalized.runtime_event) {
        .sandbox_event => |event| try std.testing.expectEqualStrings("profile loaded", event.detail.?),
        else => return error.InvalidLaunchConfig,
    }
}

test "nucleus resource samples normalize into runtime resource events" {
    const line =
        \\{"timestamp":"2026-05-20T18:00:02.000Z","run_id":"01HXYZ","container_id":"nucleus-id","event_type":"resource_sample","detail":"periodic sample","is_error":false,"resources":{"cpu_millis":1042,"memory_bytes":134217728,"pids":12}}
    ;

    var normalized = try normalizeEventLine(std.testing.allocator, line);
    defer normalized.deinit();

    try std.testing.expectEqual(sandbox.SandboxEventClass.resource, normalized.sandbox_event.event_class);
    switch (normalized.runtime_event) {
        .resource_sample => |sample| {
            try std.testing.expectEqual(@as(?u64, 1042), sample.cpu_millis);
            try std.testing.expectEqual(@as(?u64, 134217728), sample.memory_bytes);
            try std.testing.expectEqual(@as(?u32, 12), sample.pids);
        },
        else => return error.InvalidLaunchConfig,
    }
}

const FakeTransport = struct {
    launch_count: usize = 0,
    stop_count: usize = 0,
    saw_launch_json: bool = false,

    fn launch(context: *anyopaque, launch_json: []const u8, _: std.mem.Allocator) anyerror!NucleusLaunchResult {
        const self: *FakeTransport = @ptrCast(@alignCast(context));
        self.launch_count += 1;
        self.saw_launch_json = std.mem.indexOf(u8, launch_json, "\"container_name\": \"atr-01HXYZ\"") != null;
        return .{
            .container_id = "nucleus-id",
            .process_id = 42,
            .event_jsonl =
            \\{"timestamp":"2026-05-20T18:00:01.000Z","run_id":"01HXYZ","container_id":"nucleus-id","event_type":"container_start","detail":"started","is_error":false}
            \\{"timestamp":"2026-05-20T18:00:02.000Z","run_id":"01HXYZ","container_id":"nucleus-id","event_type":"container_stop","detail":"stopped","is_error":false}
            \\
            ,
        };
    }

    fn stop(context: *anyopaque, container_id: []const u8) anyerror!void {
        const self: *FakeTransport = @ptrCast(@alignCast(context));
        try std.testing.expectEqualStrings("nucleus-id", container_id);
        self.stop_count += 1;
    }
};

test "nucleus backend launch uses transport and exposes event stream" {
    const allocator = std.testing.allocator;
    var fake_transport: FakeTransport = .{};
    var backend = NucleusBackend.init(allocator, .{
        .context = &fake_transport,
        .launch = FakeTransport.launch,
        .stop = FakeTransport.stop,
    });
    defer backend.deinit();

    const run_id = try runtime.RunId.init("01HXYZ");
    const launch: sandbox.SandboxLaunchConfig = .{
        .run_id = run_id,
        .backend = .nucleus,
        .mode = .strict_agent,
        .runtime = .gvisor,
        .command = .{
            .argv = &.{ "fake-agent", "--headless" },
            .cwd = "/workspace",
        },
        .mounts = &.{.{
            .host_path = "/repo/.agent-worktrees/01HXYZ",
            .container_path = "/workspace",
            .access = .read_write_execute,
        }},
        .resources = .{ .memory = "4G", .cpus = 4, .pids = 1024 },
        .network = .none,
    };

    const handle = try backend.backend.launch(&backend.backend, launch);
    defer backend.backend.stop(&backend.backend, handle) catch unreachable;

    try std.testing.expectEqual(@as(usize, 1), fake_transport.launch_count);
    try std.testing.expect(fake_transport.saw_launch_json);
    try std.testing.expectEqualStrings("nucleus-id", handle.sandbox_id);
    try std.testing.expectEqual(@as(?u32, 42), handle.process_id);

    var event_stream = handle.event_stream.?;
    const first = (try event_stream.next(event_stream.context, allocator)).?;
    defer deinitSandboxEvent(allocator, first);
    try std.testing.expectEqual(sandbox.SandboxEventClass.lifecycle, first.event_class);
    try std.testing.expectEqualStrings("container_start", first.event_type);

    const second = (try event_stream.next(event_stream.context, allocator)).?;
    defer deinitSandboxEvent(allocator, second);
    try std.testing.expectEqualStrings("container_stop", second.event_type);
    try std.testing.expect((try event_stream.next(event_stream.context, allocator)) == null);
}

test "nucleus ingestion writes raw jsonl and normalized runtime records" {
    const allocator = std.testing.allocator;
    var store = @import("../trace/file_store.zig").MemoryTraceStore{};
    var writer = store.writer();
    var sequence: runtime.Sequence = 0;
    const jsonl =
        \\{"timestamp":"2026-05-20T18:00:01.000Z","run_id":"01HXYZ","container_id":"nucleus-id","event_type":"container_start","detail":"started","is_error":false}
        \\{"timestamp":"2026-05-20T18:00:02.000Z","run_id":"01HXYZ","container_id":"nucleus-id","event_type":"resource_sample","detail":"sample","is_error":false,"resources":{"cpu_millis":9,"memory_bytes":10,"pids":1}}
        \\
    ;

    const count = try ingestEventJsonl(allocator, &writer, &sequence, jsonl);
    try std.testing.expectEqual(@as(usize, 2), count);
    try std.testing.expectEqual(@as(runtime.Sequence, 2), sequence);
    try std.testing.expectEqual(@as(usize, 2), store.events_appended);
    try std.testing.expectEqual(@as(usize, 2), store.artifacts_written);
}

test "nucleus fixture event stream ingests raw and normalized records" {
    const allocator = std.testing.allocator;
    const jsonl = try std.fs.cwd().readFileAlloc(
        allocator,
        "test/fixtures/sandbox/nucleus_events.jsonl",
        16 * 1024,
    );
    defer allocator.free(jsonl);

    var store = @import("../trace/file_store.zig").MemoryTraceStore{};
    var writer = store.writer();
    var sequence: runtime.Sequence = 0;

    const count = try ingestEventJsonl(allocator, &writer, &sequence, jsonl);
    try std.testing.expectEqual(@as(usize, 7), count);
    try std.testing.expectEqual(@as(runtime.Sequence, 7), sequence);
    try std.testing.expectEqual(@as(usize, 7), store.events_appended);
    try std.testing.expectEqual(@as(usize, 7), store.artifacts_written);
}
