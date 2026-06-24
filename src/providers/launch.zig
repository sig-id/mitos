const std = @import("std");
const adapters = @import("../adapters/types.zig");
const transport = @import("../transport/root.zig");
const claude = @import("claude.zig");
const codex = @import("codex.zig");
const fake = @import("fake.zig");
const gemini = @import("gemini.zig");
const headless = @import("headless.zig");
const opencode = @import("opencode.zig");
const pi = @import("pi.zig");

pub const ResolveError = error{
    ProviderNotRegistered,
    TransportUnavailable,
    MissingArgv,
};

pub const ResolvedLaunch = struct {
    provider: adapters.ProviderId,
    adapter: adapters.AdapterId,
    transport_name: transport.TransportName,
    argv: []const []const u8,
    cwd: ?[]const u8 = null,
    initial_input: ?[]const u8 = null,
    owns_initial_input: bool = false,

    pub fn deinit(self: ResolvedLaunch, allocator: std.mem.Allocator) void {
        if (self.argv.len > 0) allocator.free(self.argv);
        if (self.owns_initial_input) {
            if (self.initial_input) |input| allocator.free(input);
        }
    }
};

pub fn resolve(
    allocator: std.mem.Allocator,
    spec: transport.LaunchSpec,
) !ResolvedLaunch {
    const order = if (spec.transport_order.len > 0)
        spec.transport_order
    else
        &[_]transport.TransportName{.pty};

    var saw_supported_transport = false;
    var last_provider_error: ?anyerror = null;

    for (order) |transport_name| {
        switch (transport_name) {
            .stdio_json => {
                saw_supported_transport = true;
                return resolveProviderStdioJson(allocator, spec) catch |err| {
                    last_provider_error = err;
                    continue;
                };
            },
            .pty => {
                saw_supported_transport = true;
                return resolveProviderPty(allocator, spec) catch |err| {
                    last_provider_error = err;
                    continue;
                };
            },
            else => {},
        }
    }

    if (last_provider_error) |err| return err;
    if (saw_supported_transport) return error.TransportUnavailable;
    return error.TransportUnavailable;
}

pub fn resolvePty(
    allocator: std.mem.Allocator,
    spec: transport.LaunchSpec,
) !ResolvedLaunch {
    const order = if (spec.transport_order.len > 0)
        spec.transport_order
    else
        &[_]transport.TransportName{.pty};

    for (order) |transport_name| {
        if (transport_name != .pty) continue;
        return try resolveProviderPty(allocator, spec);
    }

    return error.TransportUnavailable;
}

fn resolveProviderPty(
    allocator: std.mem.Allocator,
    spec: transport.LaunchSpec,
) !ResolvedLaunch {
    const cwd = spec.cwd orelse ".";
    const task: adapters.Task = .{ .text = spec.task };

    if (std.mem.eql(u8, spec.provider, "fake")) {
        const command = try fake.buildPtyCommand(allocator, task, cwd);
        return .{
            .provider = fake.provider_id,
            .adapter = fake.pty_adapter_id,
            .transport_name = .pty,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }

    if (isClaudeProvider(spec.provider)) {
        const command = try claude.buildPtyCommand(allocator, task, cwd);
        return .{
            .provider = if (std.mem.eql(u8, spec.provider, "claude")) claude.provider_id else claude.claudecode_provider_id,
            .adapter = if (std.mem.eql(u8, spec.provider, "claude")) claude.pty_adapter_id else claude.claudecode_pty_adapter_id,
            .transport_name = .pty,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }

    if (std.mem.eql(u8, spec.provider, "codex")) {
        const command = try codex.buildPtyCommand(allocator, task, cwd);
        return .{
            .provider = codex.provider_id,
            .adapter = codex.pty_adapter_id,
            .transport_name = .pty,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }

    if (std.mem.eql(u8, spec.provider, "gemini")) {
        const command = try gemini.buildPtyCommand(allocator, task, cwd);
        return .{
            .provider = gemini.provider_id,
            .adapter = gemini.pty_adapter_id,
            .transport_name = .pty,
            .argv = command.argv,
            .cwd = command.cwd,
            .initial_input = command.initial_input,
            .owns_initial_input = true,
        };
    }

    if (std.mem.eql(u8, spec.provider, "opencode")) {
        const command = try opencode.buildPtyCommand(allocator, task, cwd);
        return .{
            .provider = opencode.provider_id,
            .adapter = opencode.pty_adapter_id,
            .transport_name = .pty,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }

    if (std.mem.eql(u8, spec.provider, "pi")) {
        const command = try pi.buildPtyCommand(allocator, task, cwd);
        return .{
            .provider = pi.provider_id,
            .adapter = pi.pty_adapter_id,
            .transport_name = .pty,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }

    if (std.mem.eql(u8, spec.provider, "generic") or
        std.mem.eql(u8, spec.provider, "generic-pty"))
    {
        if (spec.argv.len == 0) return error.MissingArgv;
        const argv = try allocator.alloc([]const u8, spec.argv.len);
        @memcpy(argv, spec.argv);
        return .{
            .provider = .{ .value = "generic-pty" },
            .adapter = .{ .value = "generic-pty.terminal" },
            .transport_name = .pty,
            .argv = argv,
            .cwd = spec.cwd,
            .initial_input = if (spec.task.len == 0) null else spec.task,
        };
    }

    return error.ProviderNotRegistered;
}

fn resolveProviderStdioJson(
    allocator: std.mem.Allocator,
    spec: transport.LaunchSpec,
) !ResolvedLaunch {
    const command = try resolveStdioJson(allocator, spec);

    if (std.mem.eql(u8, spec.provider, "fake")) {
        return .{
            .provider = fake.provider_id,
            .adapter = fake.headless_adapter_id,
            .transport_name = .stdio_json,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }
    if (isClaudeProvider(spec.provider)) {
        return .{
            .provider = if (std.mem.eql(u8, spec.provider, "claude")) claude.provider_id else claude.claudecode_provider_id,
            .adapter = if (std.mem.eql(u8, spec.provider, "claude")) claude.print_adapter_id else claude.claudecode_print_adapter_id,
            .transport_name = .stdio_json,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }
    if (std.mem.eql(u8, spec.provider, "codex")) {
        return .{
            .provider = codex.provider_id,
            .adapter = codex.exec_adapter_id,
            .transport_name = .stdio_json,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }
    if (std.mem.eql(u8, spec.provider, "gemini")) {
        return .{
            .provider = gemini.provider_id,
            .adapter = gemini.headless_adapter_id,
            .transport_name = .stdio_json,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }
    if (std.mem.eql(u8, spec.provider, "opencode")) {
        return .{
            .provider = opencode.provider_id,
            .adapter = opencode.run_adapter_id,
            .transport_name = .stdio_json,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }
    if (std.mem.eql(u8, spec.provider, "pi")) {
        return .{
            .provider = pi.provider_id,
            .adapter = pi.json_adapter_id,
            .transport_name = .stdio_json,
            .argv = command.argv,
            .cwd = command.cwd,
        };
    }

    return error.ProviderNotRegistered;
}

pub fn resolveStdioJson(
    allocator: std.mem.Allocator,
    spec: transport.LaunchSpec,
) !headless.HeadlessCommand {
    const cwd = spec.cwd orelse ".";
    const task: adapters.Task = .{ .text = spec.task };

    if (std.mem.eql(u8, spec.provider, "fake")) {
        const argv = try allocator.alloc([]const u8, 5);
        argv[0] = "/bin/sh";
        argv[1] = "-c";
        argv[2] = "case \"$1\" in *\"token usage\"*) printf '%s\\n' '{\"type\":\"token_usage\",\"provider\":\"fake\",\"model\":\"fake-local\",\"input_tokens\":7,\"output_tokens\":3,\"usd\":0.11,\"wall_ms\":125}';; esac; printf 'FAKE_FINAL: %s\\n' \"$1\"";
        argv[3] = "mitos-fake-provider";
        argv[4] = spec.task;
        return .{ .argv = argv, .cwd = cwd };
    }

    if (isClaudeProvider(spec.provider)) {
        return try claude.buildPrintCommand(allocator, task, cwd);
    }

    if (std.mem.eql(u8, spec.provider, "codex")) {
        return try codex.buildExecCommand(allocator, task, cwd);
    }

    if (std.mem.eql(u8, spec.provider, "gemini")) {
        return try gemini.buildHeadlessCommand(allocator, task, cwd);
    }

    if (std.mem.eql(u8, spec.provider, "opencode")) {
        return try opencode.buildRunCommand(allocator, task, cwd);
    }

    if (std.mem.eql(u8, spec.provider, "pi")) {
        return try pi.buildJsonCommand(allocator, task, cwd);
    }

    return error.TransportUnavailable;
}

fn isClaudeProvider(provider: []const u8) bool {
    return std.mem.eql(u8, provider, "claude") or
        std.mem.eql(u8, provider, "claudecode") or
        std.mem.eql(u8, provider, "claude-code");
}

test "resolves fake pty launch without BEAM provider knowledge" {
    const spec: transport.LaunchSpec = .{
        .provider = "fake",
        .task = "hello",
        .transport_order = &.{.pty},
        .cwd = ".",
    };
    const resolved = try resolvePty(std.testing.allocator, spec);
    defer resolved.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("fake", resolved.provider.value);
    try std.testing.expectEqualStrings("fake.pty", resolved.adapter.value);
    try std.testing.expectEqual(transport.TransportName.pty, resolved.transport_name);
    try std.testing.expectEqualStrings("/bin/sh", resolved.argv[0]);
}

test "resolves codex pty launch through provider registry" {
    const spec: transport.LaunchSpec = .{
        .provider = "codex",
        .task = "Fix tests",
        .transport_order = &.{ .stdio_json, .pty },
        .cwd = "/workspace",
    };
    const resolved = try resolvePty(std.testing.allocator, spec);
    defer resolved.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("codex.pty", resolved.adapter.value);
    try std.testing.expectEqualStrings("codex", resolved.argv[0]);
    try std.testing.expectEqualStrings("--cd", resolved.argv[2]);
}

test "resolve honors stdio-json before pty" {
    const spec: transport.LaunchSpec = .{
        .provider = "fake",
        .task = "hello",
        .transport_order = &.{ .stdio_json, .pty },
        .cwd = ".",
    };
    const resolved = try resolve(std.testing.allocator, spec);
    defer resolved.deinit(std.testing.allocator);

    try std.testing.expectEqual(transport.TransportName.stdio_json, resolved.transport_name);
    try std.testing.expectEqualStrings("fake.headless", resolved.adapter.value);
}

test "resolves claudecode alias to claude print compatibility layer" {
    const spec: transport.LaunchSpec = .{
        .provider = "claudecode",
        .task = "hello",
        .transport_order = &.{.stdio_json},
        .cwd = "/workspace",
    };
    const resolved = try resolve(std.testing.allocator, spec);
    defer resolved.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("claudecode", resolved.provider.value);
    try std.testing.expectEqualStrings("claudecode.print", resolved.adapter.value);
    try std.testing.expectEqualStrings("claude", resolved.argv[0]);
    try std.testing.expectEqualStrings("--bare", resolved.argv[6]);
}

test "resolves opencode and pi stdio-json compatibility layers" {
    const opencode_resolved = try resolve(std.testing.allocator, .{
        .provider = "opencode",
        .task = "hello",
        .transport_order = &.{.stdio_json},
        .cwd = "/workspace",
    });
    defer opencode_resolved.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("opencode.run", opencode_resolved.adapter.value);
    try std.testing.expectEqualStrings("opencode", opencode_resolved.argv[0]);
    try std.testing.expectEqualStrings("run", opencode_resolved.argv[2]);

    const pi_resolved = try resolve(std.testing.allocator, .{
        .provider = "pi",
        .task = "hello",
        .transport_order = &.{.stdio_json},
        .cwd = "/workspace",
    });
    defer pi_resolved.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("pi.json", pi_resolved.adapter.value);
    try std.testing.expectEqualStrings("pi", pi_resolved.argv[0]);
    try std.testing.expectEqualStrings("json", pi_resolved.argv[2]);
}
