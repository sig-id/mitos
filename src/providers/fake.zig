const std = @import("std");
const adapters = @import("../adapters/types.zig");
const transport = @import("../transport/root.zig");

pub const provider_id = adapters.ProviderId{ .value = "fake" };
pub const headless_adapter_id = adapters.AdapterId{ .value = "fake.headless" };
pub const pty_adapter_id = adapters.AdapterId{ .value = "fake.pty" };

const supported_transports = [_]transport.TransportName{.stdio_json};

pub fn initHeadlessAdapter() adapters.AgentAdapter {
    return .{
        .id = headless_adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
            .supports_streaming_events = true,
        },
        .detect = detect,
        .start = start,
    };
}

pub fn buildPtyCommand(
    allocator: std.mem.Allocator,
    task: adapters.Task,
    cwd: []const u8,
) !@import("headless.zig").HeadlessCommand {
    const argv = try allocator.alloc([]const u8, 5);
    argv[0] = "/bin/sh";
    argv[1] = "-c";
    argv[2] = "printf 'FakeAgent\\r\\n'; printf 'FAKE_FINAL: %s\\r\\n' \"$1\"; sleep 0.3";
    argv[3] = "mitos-fake-provider";
    argv[4] = task.text;

    return .{
        .argv = argv,
        .cwd = cwd,
    };
}

fn detect(
    _: *adapters.AgentAdapter,
    _: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    return .{
        .available = true,
        .binary = "mitos-fake-provider",
        .auth_status = .not_required,
        .supported_transports = &supported_transports,
    };
}

fn start(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    if (context.event_sink) |sink| {
        try sink.emit(sink.context, .{ .structured = "{\"type\":\"fake_run_started\"}" });
        try sink.emit(sink.context, .{ .stdout = task.text });
        try sink.emit(sink.context, .{ .final_output = "FAKE_FINAL: completed" });
    }

    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .transport_name = .stdio_json,
        .status = .completed,
        .started_at_ms = context.started_at_ms,
    };
}

const SinkRecorder = struct {
    count: usize = 0,
    saw_final: bool = false,
};

fn recordSink(context: *anyopaque, event: adapters.AdapterEvent) anyerror!void {
    const recorder: *SinkRecorder = @ptrCast(@alignCast(context));
    recorder.count += 1;
    if (event == .final_output) recorder.saw_final = true;
}

test "fake provider emits deterministic integration events" {
    var adapter = initHeadlessAdapter();
    var recorder: SinkRecorder = .{};
    const run_id = try @import("../runtime/types.zig").RunId.init("run-fake");

    const run = try adapter.start(&adapter, .{
        .run_id = run_id,
        .workspace_host_path = ".",
        .event_sink = .{
            .context = &recorder,
            .emit = recordSink,
        },
    }, .{ .text = "hello" });

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, run.status);
    try std.testing.expectEqual(@as(usize, 3), recorder.count);
    try std.testing.expect(recorder.saw_final);
}

test "fake provider advertises headless modes only" {
    var adapter = initHeadlessAdapter();
    const detection = try adapter.detect.?(&adapter, std.testing.allocator);

    try std.testing.expect(adapter.capabilities.supportsTransport(.stdio_json));
    try std.testing.expect(!adapter.capabilities.supportsTransport(.pty));
    try std.testing.expect(detection.supportsTransport(.stdio_json));
    try std.testing.expect(!detection.supportsTransport(.pty));
}

test "fake pty command passes task as a shell argument" {
    const command = try buildPtyCommand(
        std.testing.allocator,
        .{ .text = "hello" },
        "/workspace",
    );
    defer command.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("/bin/sh", command.argv[0]);
    try std.testing.expectEqualStrings("-c", command.argv[1]);
    try std.testing.expectEqualStrings("mitos-fake-provider", command.argv[3]);
    try std.testing.expectEqualStrings("hello", command.argv[4]);
}
