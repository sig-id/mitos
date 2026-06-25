const std = @import("std");
const adapter_detection = @import("../adapters/detection.zig");
const adapters = @import("../adapters/types.zig");
const headless = @import("headless.zig");
const transport = @import("../transport/root.zig");

pub const provider_id = adapters.ProviderId{ .value = "pi" };
pub const json_adapter_id = adapters.AdapterId{ .value = "pi.json" };
pub const pty_adapter_id = adapters.AdapterId{ .value = "pi.pty" };

const pty_terminal_chrome_markers = [_][]const u8{
    "Pi",
    "Working",
    "Thinking",
    "steering",
    "follow-up",
};
pub const pty_terminal_observation = adapters.terminalObservationProviderTui(&pty_terminal_chrome_markers);

const supported_transports = [_]transport.TransportName{.stdio_json};
const unavailable_warnings = [_][]const u8{"pi binary was not found on PATH or common toolchain directories"};
const unsupported_warnings = [_][]const u8{"pi does not advertise --mode json support"};

pub fn initJsonAdapter() adapters.AgentAdapter {
    return .{
        .id = json_adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
            .supports_streaming_events = true,
            .supports_model_selection = true,
        },
        .detect = detectJson,
        .start = startJson,
    };
}

pub fn buildJsonCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !headless.HeadlessCommand {
    const argv = try allocator.alloc([]const u8, 11);
    argv[0] = "pi";
    argv[1] = "--mode";
    argv[2] = "json";
    argv[3] = "--print";
    argv[4] = "--no-session";
    argv[5] = "--no-context-files";
    argv[6] = "--no-extensions";
    argv[7] = "--no-skills";
    argv[8] = "--no-prompt-templates";
    argv[9] = "--no-themes";
    argv[10] = task.text;

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
    const argv = try allocator.alloc([]const u8, 2);
    argv[0] = "pi";
    argv[1] = task.text;

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

fn detectJson(
    _: *adapters.AgentAdapter,
    allocator: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    if (!try adapter_detection.executableAvailable(allocator, "pi")) {
        return .{
            .available = false,
            .supported_transports = &supported_transports,
            .warnings = &unavailable_warnings,
        };
    }

    if (!try adapter_detection.commandOutputContains(
        allocator,
        &.{ "pi", "--help" },
        "--mode",
    )) {
        return .{
            .available = false,
            .binary = "pi",
            .supported_transports = &supported_transports,
            .warnings = &unsupported_warnings,
        };
    }

    const version = adapter_detection.commandOutputTrimmedAlloc(
        allocator,
        &.{ "pi", "--version" },
    ) catch null;

    return .{
        .available = true,
        .binary = "pi",
        .version = version,
        .owns_version = version != null,
        .auth_status = try adapter_detection.authStatusFromEnv(
            allocator,
            &.{ "ZAI_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "OPENCODE_API_KEY" },
        ),
        .supported_transports = &supported_transports,
    };
}

fn startJson(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    const cwd = context.workspace_container_path orelse context.workspace_host_path;
    var command = try buildJsonCommand(context.allocator, task, cwd);
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

test "pi json command construction is deterministic and hermetic" {
    const command = try buildJsonCommand(
        std.testing.allocator,
        .{ .text = "Summarize this repository" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("pi", command.argv[0]);
    try std.testing.expectEqualStrings("--mode", command.argv[1]);
    try std.testing.expectEqualStrings("json", command.argv[2]);
    try std.testing.expectEqualStrings("--print", command.argv[3]);
    try std.testing.expectEqualStrings("--no-session", command.argv[4]);
    try std.testing.expectEqualStrings("--no-context-files", command.argv[5]);
    try std.testing.expectEqualStrings("Summarize this repository", command.argv[10]);
    try std.testing.expectEqualStrings("/workspace", command.cwd);
}

test "pi json adapter start emits normalized provider events" {
    var fake_bin = try @import("test_support.zig").FakeBin.init(std.testing.allocator);
    defer fake_bin.deinit();
    try fake_bin.writeExecutable("pi",
        \\#!/bin/sh
        \\if [ "$1" = "--mode" ] && [ "$2" = "json" ] && [ "$3" = "--print" ]; then
        \\  printf '{"type":"message_update","message":{"role":"assistant","content":[]},"assistantMessageEvent":{"type":"text_delta","delta":"hi"}}\n'
        \\  printf '{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"pi fake done"}]}]}\n'
        \\  exit 0
        \\fi
        \\printf 'unexpected pi args\n' >&2
        \\exit 9
        \\
    );

    var adapter = initJsonAdapter();
    var recorder: @import("test_support.zig").AdapterEventRecorder = .{
        .expected_final = "pi fake done",
    };
    const env = [_]adapters.EnvVar{fake_bin.pathEnv()};
    const run_id = try @import("../runtime/types.zig").RunId.init("run-pi-start");
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
}
