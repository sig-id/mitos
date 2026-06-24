const std = @import("std");
const adapter_detection = @import("../adapters/detection.zig");
const adapters = @import("../adapters/types.zig");
const headless = @import("headless.zig");
const transport = @import("../transport/root.zig");

pub const provider_id = adapters.ProviderId{ .value = "gemini" };
pub const headless_adapter_id = adapters.AdapterId{ .value = "gemini.headless" };
pub const pty_adapter_id = adapters.AdapterId{ .value = "gemini.pty" };

const supported_transports = [_]transport.TransportName{.stdio_json};
const unavailable_warnings = [_][]const u8{"gemini binary was not found on PATH"};
const unsupported_warnings = [_][]const u8{"gemini does not advertise --prompt stream-json support"};

pub fn initHeadlessAdapter() adapters.AgentAdapter {
    return .{
        .id = headless_adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
            .supports_streaming_events = true,
            .supports_model_selection = true,
        },
        .detect = detectHeadless,
        .start = startHeadless,
    };
}

pub fn buildHeadlessCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !headless.HeadlessCommand {
    const argv = try allocator.alloc([]const u8, 6);
    argv[0] = "gemini";
    argv[1] = "--prompt";
    argv[2] = task.text;
    argv[3] = "--output-format";
    argv[4] = "stream-json";
    argv[5] = "--approval-mode=default";

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

pub const PtyCommand = struct {
    argv: []const []const u8,
    cwd: []const u8,
    initial_input: []const u8,

    pub fn deinit(self: PtyCommand, allocator: std.mem.Allocator) void {
        if (self.argv.len > 0) allocator.free(self.argv);
        if (self.initial_input.len > 0) allocator.free(self.initial_input);
    }
};

pub fn buildPtyCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !PtyCommand {
    const argv = try allocator.alloc([]const u8, 1);
    errdefer allocator.free(argv);
    argv[0] = "gemini";

    const initial_input = try std.fmt.allocPrint(allocator, "{s}\r", .{task.text});
    return .{
        .argv = argv,
        .cwd = cwd,
        .initial_input = initial_input,
    };
}

fn detectHeadless(
    _: *adapters.AgentAdapter,
    allocator: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    if (!try adapter_detection.executableAvailable(allocator, "gemini")) {
        return .{
            .available = false,
            .supported_transports = &supported_transports,
            .warnings = &unavailable_warnings,
        };
    }

    const supports_headless = try adapter_detection.commandOutputContains(
        allocator,
        &.{ "gemini", "--help" },
        "--prompt",
    ) and try adapter_detection.commandOutputContains(
        allocator,
        &.{ "gemini", "--help" },
        "--output-format",
    );

    if (!supports_headless) {
        return .{
            .available = false,
            .binary = "gemini",
            .auth_status = .unknown,
            .supported_transports = &supported_transports,
            .warnings = &unsupported_warnings,
        };
    }

    const version = adapter_detection.commandOutputTrimmedAlloc(
        allocator,
        &.{ "gemini", "--version" },
    ) catch null;

    return .{
        .available = true,
        .binary = "gemini",
        .version = version,
        .owns_version = version != null,
        .auth_status = try adapter_detection.authStatusFromEnv(
            allocator,
            &.{ "GEMINI_API_KEY", "GOOGLE_API_KEY" },
        ),
        .supported_transports = &supported_transports,
    };
}

fn startHeadless(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    const cwd = context.workspace_container_path orelse context.workspace_host_path;
    var command = try buildHeadlessCommand(context.allocator, task, cwd);
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

test "gemini headless command construction is deterministic" {
    const command = try buildHeadlessCommand(
        std.testing.allocator,
        .{ .text = "Summarize this repository" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("gemini", command.argv[0]);
    try std.testing.expectEqualStrings("--prompt", command.argv[1]);
    try std.testing.expectEqualStrings("Summarize this repository", command.argv[2]);
    try std.testing.expectEqualStrings("--output-format", command.argv[3]);
    try std.testing.expectEqualStrings("stream-json", command.argv[4]);
    try std.testing.expectEqualStrings("--approval-mode=default", command.argv[5]);
    try std.testing.expectEqualStrings("/workspace", command.cwd);
}

test "gemini pty command construction is deterministic" {
    const command = try buildPtyCommand(
        std.testing.allocator,
        .{ .text = "Summarize this repository" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("gemini", command.argv[0]);
    try std.testing.expectEqualStrings("/workspace", command.cwd);
    try std.testing.expectEqualStrings("Summarize this repository\r", command.initial_input);
}

test "gemini adapter advertises headless json support" {
    var adapter = initHeadlessAdapter();

    try std.testing.expectEqualStrings("gemini", adapter.provider.value);
    try std.testing.expect(adapter.capabilities.supportsTransport(.stdio_json));
    try std.testing.expect(!adapter.capabilities.supportsTransport(.pty));
}

test "gemini headless adapter start runs configured provider binary" {
    var fake_bin = try @import("test_support.zig").FakeBin.init(std.testing.allocator);
    defer fake_bin.deinit();
    try fake_bin.writeExecutable("gemini",
        \\#!/bin/sh
        \\if [ "$1" = "--prompt" ] && [ "$3" = "--output-format" ] && [ "$4" = "stream-json" ]; then
        \\  printf '{"text":"gemini fake done"}\n'
        \\  exit 0
        \\fi
        \\printf 'unexpected gemini args\n' >&2
        \\exit 9
        \\
    );

    var adapter = initHeadlessAdapter();
    var recorder: @import("test_support.zig").AdapterEventRecorder = .{
        .expected_final = "gemini fake done",
    };
    const env = [_]adapters.EnvVar{fake_bin.pathEnv()};
    const run_id = try @import("../runtime/types.zig").RunId.init("run-gemini-start");
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
