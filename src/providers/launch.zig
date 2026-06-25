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
    terminal_observation: adapters.TerminalObservationProfile = adapters.terminal_observation_generic_pty,
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

const PtyCommand = struct {
    argv: []const []const u8,
    cwd: []const u8,
    initial_input: ?[]const u8 = null,
    owns_initial_input: bool = false,
};

const PtyBuildFn = *const fn (std.mem.Allocator, adapters.Task, []const u8) anyerror!PtyCommand;
const StdioJsonBuildFn = *const fn (std.mem.Allocator, adapters.Task, []const u8) anyerror!headless.HeadlessCommand;

const PtyLaunchAdapter = struct {
    aliases: []const []const u8,
    provider: adapters.ProviderId,
    adapter: adapters.AdapterId,
    terminal_observation: adapters.TerminalObservationProfile,
    build: PtyBuildFn,
};

const StdioJsonLaunchAdapter = struct {
    aliases: []const []const u8,
    provider: adapters.ProviderId,
    adapter: adapters.AdapterId,
    build: StdioJsonBuildFn,
};

const pty_launch_adapters = [_]PtyLaunchAdapter{
    .{
        .aliases = &.{"fake"},
        .provider = fake.provider_id,
        .adapter = fake.pty_adapter_id,
        .terminal_observation = fake.pty_terminal_observation,
        .build = buildFakePty,
    },
    .{
        .aliases = &.{"claude"},
        .provider = claude.provider_id,
        .adapter = claude.pty_adapter_id,
        .terminal_observation = claude.pty_terminal_observation,
        .build = buildClaudePty,
    },
    .{
        .aliases = &.{ "claudecode", "claude-code" },
        .provider = claude.claudecode_provider_id,
        .adapter = claude.claudecode_pty_adapter_id,
        .terminal_observation = claude.pty_terminal_observation,
        .build = buildClaudePty,
    },
    .{
        .aliases = &.{"codex"},
        .provider = codex.provider_id,
        .adapter = codex.pty_adapter_id,
        .terminal_observation = codex.pty_terminal_observation,
        .build = buildCodexPty,
    },
    .{
        .aliases = &.{"gemini"},
        .provider = gemini.provider_id,
        .adapter = gemini.pty_adapter_id,
        .terminal_observation = gemini.pty_terminal_observation,
        .build = buildGeminiPty,
    },
    .{
        .aliases = &.{"opencode"},
        .provider = opencode.provider_id,
        .adapter = opencode.pty_adapter_id,
        .terminal_observation = opencode.pty_terminal_observation,
        .build = buildOpencodePty,
    },
    .{
        .aliases = &.{"pi"},
        .provider = pi.provider_id,
        .adapter = pi.pty_adapter_id,
        .terminal_observation = pi.pty_terminal_observation,
        .build = buildPiPty,
    },
};

const stdio_json_launch_adapters = [_]StdioJsonLaunchAdapter{
    .{
        .aliases = &.{"fake"},
        .provider = fake.provider_id,
        .adapter = fake.headless_adapter_id,
        .build = fake.buildHeadlessCommand,
    },
    .{
        .aliases = &.{"claude"},
        .provider = claude.provider_id,
        .adapter = claude.print_adapter_id,
        .build = claude.buildPrintCommand,
    },
    .{
        .aliases = &.{ "claudecode", "claude-code" },
        .provider = claude.claudecode_provider_id,
        .adapter = claude.claudecode_print_adapter_id,
        .build = claude.buildPrintCommand,
    },
    .{
        .aliases = &.{"codex"},
        .provider = codex.provider_id,
        .adapter = codex.exec_adapter_id,
        .build = codex.buildExecCommand,
    },
    .{
        .aliases = &.{"gemini"},
        .provider = gemini.provider_id,
        .adapter = gemini.headless_adapter_id,
        .build = gemini.buildHeadlessCommand,
    },
    .{
        .aliases = &.{"opencode"},
        .provider = opencode.provider_id,
        .adapter = opencode.run_adapter_id,
        .build = opencode.buildRunCommand,
    },
    .{
        .aliases = &.{"pi"},
        .provider = pi.provider_id,
        .adapter = pi.json_adapter_id,
        .build = pi.buildJsonCommand,
    },
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

    if (findPtyLaunchAdapter(spec.provider)) |entry| {
        const command = try entry.build(allocator, task, cwd);
        return .{
            .provider = entry.provider,
            .adapter = entry.adapter,
            .transport_name = .pty,
            .terminal_observation = entry.terminal_observation,
            .argv = command.argv,
            .cwd = command.cwd,
            .initial_input = command.initial_input,
            .owns_initial_input = command.owns_initial_input,
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
            .terminal_observation = adapters.terminal_observation_generic_pty,
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
    if (findStdioJsonLaunchAdapter(spec.provider)) |entry| {
        const cwd = spec.cwd orelse ".";
        const task: adapters.Task = .{ .text = spec.task };
        const command = try entry.build(allocator, task, cwd);
        return .{
            .provider = entry.provider,
            .adapter = entry.adapter,
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
    const entry = findStdioJsonLaunchAdapter(spec.provider) orelse return error.TransportUnavailable;
    const cwd = spec.cwd orelse ".";
    const task: adapters.Task = .{ .text = spec.task };

    return try entry.build(allocator, task, cwd);
}

fn findPtyLaunchAdapter(provider: []const u8) ?*const PtyLaunchAdapter {
    for (&pty_launch_adapters) |*entry| {
        if (matchesAnyAlias(provider, entry.aliases)) return entry;
    }
    return null;
}

fn findStdioJsonLaunchAdapter(provider: []const u8) ?*const StdioJsonLaunchAdapter {
    for (&stdio_json_launch_adapters) |*entry| {
        if (matchesAnyAlias(provider, entry.aliases)) return entry;
    }
    return null;
}

fn matchesAnyAlias(provider: []const u8, aliases: []const []const u8) bool {
    for (aliases) |alias| {
        if (std.mem.eql(u8, provider, alias)) return true;
    }
    return false;
}

fn commandFromHeadless(command: headless.HeadlessCommand) PtyCommand {
    return .{
        .argv = command.argv,
        .cwd = command.cwd,
    };
}

fn buildFakePty(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) anyerror!PtyCommand {
    return commandFromHeadless(try fake.buildPtyCommand(allocator, task, cwd));
}

fn buildClaudePty(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) anyerror!PtyCommand {
    return commandFromHeadless(try claude.buildPtyCommand(allocator, task, cwd));
}

fn buildCodexPty(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) anyerror!PtyCommand {
    return commandFromHeadless(try codex.buildPtyCommand(allocator, task, cwd));
}

fn buildGeminiPty(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) anyerror!PtyCommand {
    const command = try gemini.buildPtyCommand(allocator, task, cwd);
    return .{
        .argv = command.argv,
        .cwd = command.cwd,
        .initial_input = command.initial_input,
        .owns_initial_input = true,
    };
}

fn buildOpencodePty(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) anyerror!PtyCommand {
    return commandFromHeadless(try opencode.buildPtyCommand(allocator, task, cwd));
}

fn buildPiPty(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) anyerror!PtyCommand {
    return commandFromHeadless(try pi.buildPtyCommand(allocator, task, cwd));
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
    try std.testing.expect(resolved.terminal_observation.emit_raw_refs);
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
    try std.testing.expect(!resolved.terminal_observation.emit_raw_refs);
    try std.testing.expect(resolved.terminal_observation.extract_visible_text);
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
