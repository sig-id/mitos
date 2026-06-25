const std = @import("std");
const adapter_detection = @import("../adapters/detection.zig");
const adapters = @import("../adapters/types.zig");
const headless = @import("headless.zig");
const transport = @import("../transport/root.zig");

pub const provider_id = adapters.ProviderId{ .value = "claude" };
pub const claudecode_provider_id = adapters.ProviderId{ .value = "claudecode" };
pub const print_adapter_id = adapters.AdapterId{ .value = "claude.print" };
pub const claudecode_print_adapter_id = adapters.AdapterId{ .value = "claudecode.print" };
pub const pty_adapter_id = adapters.AdapterId{ .value = "claude.pty" };
pub const claudecode_pty_adapter_id = adapters.AdapterId{ .value = "claudecode.pty" };

const pty_terminal_chrome_markers = [_][]const u8{
    "MCP startup",
    "Working",
    "Thinking",
    "esc to interrupt",
};
pub const pty_terminal_observation = adapters.terminalObservationProviderTui(&pty_terminal_chrome_markers);

const supported_transports = [_]transport.TransportName{.stdio_json};
const unavailable_warnings = [_][]const u8{"claude binary was not found on PATH or common toolchain directories"};
const unsupported_warnings = [_][]const u8{"claude does not advertise --print stream-json support"};

pub fn initPrintAdapter() adapters.AgentAdapter {
    return .{
        .id = print_adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
            .supports_streaming_events = true,
            .supports_model_selection = true,
            .supports_cost_reporting = true,
        },
        .detect = detectPrint,
        .start = startPrint,
    };
}

pub fn initClaudeCodePrintAdapter() adapters.AgentAdapter {
    var adapter = initPrintAdapter();
    adapter.id = claudecode_print_adapter_id;
    adapter.provider = claudecode_provider_id;
    return adapter;
}

pub fn buildPrintCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !headless.HeadlessCommand {
    // `--print --output-format stream-json` requires `--verbose` on current
    // claude CLI releases; without it the binary exits with a usage error.
    const argv = try allocator.alloc([]const u8, 8);
    argv[0] = "claude";
    argv[1] = "--print";
    argv[2] = "--output-format";
    argv[3] = "stream-json";
    argv[4] = "--verbose";
    argv[5] = "--no-session-persistence";
    argv[6] = "--bare";
    argv[7] = task.text;

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
    const argv = try allocator.alloc([]const u8, 3);
    argv[0] = "claude";
    argv[1] = "--";
    argv[2] = task.text;

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

fn detectPrint(
    _: *adapters.AgentAdapter,
    allocator: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    if (!try adapter_detection.executableAvailable(allocator, "claude")) {
        return .{
            .available = false,
            .supported_transports = &supported_transports,
            .warnings = &unavailable_warnings,
        };
    }

    return .{
        .available = true,
        .binary = "claude",
        .version = null,
        .owns_version = false,
        .auth_status = try adapter_detection.authStatusFromEnv(
            allocator,
            &.{"ANTHROPIC_API_KEY"},
        ),
        .supported_transports = &supported_transports,
    };
}

fn startPrint(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    const cwd = context.workspace_container_path orelse context.workspace_host_path;
    var command = try buildPrintCommand(context.allocator, task, cwd);
    command.environment = context.env;
    defer command.deinit(context.allocator);

    try headless.emitLaunchPlan(context.event_sink, self.provider.value, self.id.value);
    const result = try headless.executeAndEmitForProvider(context.allocator, command, context.event_sink, .{}, self.provider);
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

test "claude print command construction is deterministic" {
    const command = try buildPrintCommand(
        std.testing.allocator,
        .{ .text = "Summarize this repository" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("claude", command.argv[0]);
    try std.testing.expectEqualStrings("--print", command.argv[1]);
    try std.testing.expectEqualStrings("--output-format", command.argv[2]);
    try std.testing.expectEqualStrings("stream-json", command.argv[3]);
    try std.testing.expectEqualStrings("--verbose", command.argv[4]);
    try std.testing.expectEqualStrings("--no-session-persistence", command.argv[5]);
    try std.testing.expectEqualStrings("--bare", command.argv[6]);
    try std.testing.expectEqualStrings("Summarize this repository", command.argv[7]);
    try std.testing.expectEqualStrings("/workspace", command.cwd);
}

test "claude adapter advertises headless json support" {
    var adapter = initPrintAdapter();

    try std.testing.expectEqualStrings("claude", adapter.provider.value);
    try std.testing.expect(adapter.capabilities.supportsTransport(.stdio_json));
    try std.testing.expect(!adapter.capabilities.supportsTransport(.pty));
}

test "claude print adapter start runs configured provider binary" {
    var fake_bin = try @import("test_support.zig").FakeBin.init(std.testing.allocator);
    defer fake_bin.deinit();
    try fake_bin.writeExecutable("claude",
        \\#!/bin/sh
        \\if [ "$1" = "--print" ] && [ "$2" = "--output-format" ] && [ "$3" = "stream-json" ] && [ "$4" = "--verbose" ] && [ "$5" = "--no-session-persistence" ] && [ "$6" = "--bare" ]; then
        \\  printf '{"result":"claude fake done"}\n'
        \\  exit 0
        \\fi
        \\printf 'unexpected claude args\n' >&2
        \\exit 9
        \\
    );

    var adapter = initPrintAdapter();
    var recorder: @import("test_support.zig").AdapterEventRecorder = .{
        .expected_final = "claude fake done",
    };
    const env = [_]adapters.EnvVar{fake_bin.pathEnv()};
    const run_id = try @import("../runtime/types.zig").RunId.init("run-claude-start");
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
    try std.testing.expect(recorder.saw_final);
    try std.testing.expect(recorder.saw_expected_final);
    try std.testing.expectEqual(@as(?u8, 0), recorder.exit_code);
}

test "claude pty command construction is deterministic" {
    const command = try buildPtyCommand(
        std.testing.allocator,
        .{ .text = "Explain the code" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("claude", command.argv[0]);
    try std.testing.expectEqualStrings("--", command.argv[1]);
    try std.testing.expectEqualStrings("Explain the code", command.argv[2]);
    try std.testing.expectEqualStrings("/workspace", command.cwd);
}
