const std = @import("std");
const adapter_detection = @import("../adapters/detection.zig");
const adapters = @import("../adapters/types.zig");
const headless = @import("headless.zig");
const pty_provider = @import("pty.zig");
const transport = @import("../transport/root.zig");

pub const provider_id = adapters.ProviderId{ .value = "codex" };
pub const exec_adapter_id = adapters.AdapterId{ .value = "codex.exec" };
pub const pty_adapter_id = adapters.AdapterId{ .value = "codex.pty" };

const supported_transports = [_]transport.TransportName{.stdio_json};
const pty_supported_transports = [_]transport.TransportName{.pty};
const unavailable_warnings = [_][]const u8{"codex binary was not found on PATH"};
const no_json_warnings = [_][]const u8{"codex exec does not advertise --json support"};
const no_pty_warnings = [_][]const u8{"codex does not advertise interactive PTY flags"};

pub fn initExecAdapter() adapters.AgentAdapter {
    return .{
        .id = exec_adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
            .supports_streaming_events = true,
            .supports_model_selection = true,
        },
        .detect = detectExec,
        .start = startExec,
    };
}

pub fn initPtyAdapter() adapters.AgentAdapter {
    return .{
        .id = pty_adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_interactive_pty = true,
            .supports_streaming_events = true,
        },
        .detect = detectPty,
        .start = startPty,
    };
}

pub fn buildExecCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !headless.HeadlessCommand {
    const argv = try allocator.alloc([]const u8, 6);
    argv[0] = "codex";
    argv[1] = "exec";
    argv[2] = "--json";
    argv[3] = "--color";
    argv[4] = "never";
    argv[5] = task.text;

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

pub fn buildPtyCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !headless.HeadlessCommand {
    const argv = try allocator.alloc([]const u8, 5);
    argv[0] = "codex";
    argv[1] = "--no-alt-screen";
    argv[2] = "--cd";
    argv[3] = cwd;
    argv[4] = task.text;

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

fn detectExec(
    _: *adapters.AgentAdapter,
    allocator: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    if (!try adapter_detection.executableAvailable(allocator, "codex")) {
        return .{
            .available = false,
            .auth_status = .unknown,
            .supported_transports = &supported_transports,
            .warnings = &unavailable_warnings,
        };
    }

    if (!try adapter_detection.commandOutputContains(
        allocator,
        &.{ "codex", "exec", "--help" },
        "--json",
    )) {
        return .{
            .available = false,
            .binary = "codex",
            .auth_status = .unknown,
            .supported_transports = &supported_transports,
            .warnings = &no_json_warnings,
        };
    }

    const version = adapter_detection.commandOutputTrimmedAlloc(
        allocator,
        &.{ "codex", "--version" },
    ) catch null;

    return .{
        .available = true,
        .binary = "codex",
        .version = version,
        .owns_version = version != null,
        .auth_status = try adapter_detection.authStatusFromEnv(
            allocator,
            &.{ "OPENAI_API_KEY", "CODEX_API_KEY" },
        ),
        .supported_transports = &supported_transports,
    };
}

fn detectPty(
    _: *adapters.AgentAdapter,
    allocator: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    if (!try adapter_detection.executableAvailable(allocator, "codex")) {
        return .{
            .available = false,
            .supported_transports = &pty_supported_transports,
            .warnings = &unavailable_warnings,
        };
    }

    if (!try adapter_detection.commandOutputContains(
        allocator,
        &.{ "codex", "--help" },
        "--no-alt-screen",
    )) {
        return .{
            .available = false,
            .binary = "codex",
            .auth_status = .unknown,
            .supported_transports = &pty_supported_transports,
            .warnings = &no_pty_warnings,
        };
    }

    const version = adapter_detection.commandOutputTrimmedAlloc(
        allocator,
        &.{ "codex", "--version" },
    ) catch null;

    return .{
        .available = true,
        .binary = "codex",
        .version = version,
        .owns_version = version != null,
        .auth_status = try adapter_detection.authStatusFromEnv(
            allocator,
            &.{ "OPENAI_API_KEY", "CODEX_API_KEY" },
        ),
        .supported_transports = &pty_supported_transports,
    };
}

fn startExec(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    const cwd = context.workspace_container_path orelse context.workspace_host_path;
    var command = try buildExecCommand(context.allocator, task, cwd);
    command.environment = context.env;
    defer command.deinit(context.allocator);

    try headless.emitLaunchPlan(context.event_sink, self.provider.value, self.id.value);
    const result = try headless.executeAndEmit(context.allocator, command, context.event_sink, .{});
    defer result.deinit(context.allocator);
    try headless.emitDetectedPrompts(
        context.allocator,
        context.event_sink,
        self.provider,
        self.id,
        result.stdout,
        context.started_at_ms,
    );
    try headless.emitDetectedPrompts(
        context.allocator,
        context.event_sink,
        self.provider,
        self.id,
        result.stderr,
        context.started_at_ms,
    );

    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .transport_name = .stdio_json,
        .status = result.status(),
        .started_at_ms = context.started_at_ms,
    };
}

fn startPty(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    const cwd = context.workspace_host_path;
    const command = try buildPtyCommand(context.allocator, task, cwd);
    defer command.deinit(context.allocator);

    return try pty_provider.runCommand(self, context, .{
        .argv = command.argv,
        .cwd = command.cwd,
        .env = context.env,
    });
}

test "codex exec command construction is deterministic" {
    const command = try buildExecCommand(
        std.testing.allocator,
        .{ .text = "Explain the failing test" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("codex", command.argv[0]);
    try std.testing.expectEqualStrings("exec", command.argv[1]);
    try std.testing.expectEqualStrings("--json", command.argv[2]);
    try std.testing.expectEqualStrings("--color", command.argv[3]);
    try std.testing.expectEqualStrings("never", command.argv[4]);
    try std.testing.expectEqualStrings("Explain the failing test", command.argv[5]);
    try std.testing.expectEqualStrings("/workspace", command.cwd);
}

test "codex adapter advertises exec json support" {
    var adapter = initExecAdapter();

    try std.testing.expectEqualStrings("codex", adapter.provider.value);
    try std.testing.expect(adapter.capabilities.supportsTransport(.stdio_json));
    try std.testing.expect(!adapter.capabilities.supportsTransport(.pty));
}

test "codex pty command construction is deterministic" {
    const command = try buildPtyCommand(
        std.testing.allocator,
        .{ .text = "Fix failing tests" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("codex", command.argv[0]);
    try std.testing.expectEqualStrings("--no-alt-screen", command.argv[1]);
    try std.testing.expectEqualStrings("--cd", command.argv[2]);
    try std.testing.expectEqualStrings("/workspace", command.argv[3]);
    try std.testing.expectEqualStrings("Fix failing tests", command.argv[4]);
}

test "codex pty adapter advertises interactive support" {
    var adapter = initPtyAdapter();

    try std.testing.expectEqualStrings("codex.pty", adapter.id.value);
    try std.testing.expect(adapter.capabilities.supportsTransport(.pty));
    try std.testing.expect(!adapter.capabilities.supportsTransport(.stdio_json));
}

test "codex exec adapter start runs configured provider binary" {
    var fake_bin = try @import("test_support.zig").FakeBin.init(std.testing.allocator);
    defer fake_bin.deinit();
    try fake_bin.writeExecutable("codex",
        \\#!/bin/sh
        \\if [ "$1" = "exec" ] && [ "$2" = "--json" ] && [ "$3" = "--color" ] && [ "$4" = "never" ]; then
        \\  printf 'Run npm install? [y/N]\n'
        \\  printf '{"final_output":"codex fake done"}\n'
        \\  exit 0
        \\fi
        \\printf 'unexpected codex args\n' >&2
        \\exit 9
        \\
    );

    var adapter = initExecAdapter();
    var recorder: @import("test_support.zig").AdapterEventRecorder = .{
        .expected_final = "codex fake done",
    };
    const env = [_]adapters.EnvVar{fake_bin.pathEnv()};
    const run_id = try @import("../runtime/types.zig").RunId.init("run-codex-start");
    const run = try adapter.start(&adapter, .{
        .allocator = std.testing.allocator,
        .run_id = run_id,
        .workspace_host_path = ".",
        .env = &env,
        .event_sink = recorder.sink(),
    }, .{ .text = "hello" });

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, run.status);
    try std.testing.expect(recorder.saw_process_spawned);
    try std.testing.expect(recorder.saw_process_exited);
    try std.testing.expect(recorder.saw_structured);
    try std.testing.expect(recorder.saw_prompt);
    try std.testing.expect(recorder.saw_final);
    try std.testing.expect(recorder.saw_expected_final);
    try std.testing.expectEqual(@as(?u8, 0), recorder.exit_code);
}
