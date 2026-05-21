const std = @import("std");
const runtime = @import("../runtime/types.zig");
const trace = @import("../trace/types.zig");
const types = @import("types.zig");

pub fn toRuntimeEvent(event: types.AdapterEvent) runtime.RuntimeEvent {
    return switch (event) {
        .process_spawned => |spawned| .{ .process_spawned = spawned },
        .process_exited => |exited| .{ .process_exited = exited },
        .prompt_detected => |prompt| .{ .prompt_detected = prompt },
        .stdout => |bytes| .{ .adapter_output = .{ .stream = .stdout, .bytes = bytes } },
        .stderr => |bytes| .{ .adapter_output = .{ .stream = .stderr, .bytes = bytes } },
        .structured => |bytes| .{ .adapter_output = .{ .stream = .structured, .bytes = bytes } },
        .terminal_bytes => |bytes| .{ .adapter_output = .{ .stream = .terminal, .bytes = bytes } },
        .terminal_snapshot => |_| .{ .screen_snapshot = .{
            .kind = @tagName(trace.ArtifactKind.terminal_snapshots),
            .path = trace.defaultArtifactPath(.terminal_snapshots),
        } },
        .final_output => |text| .{ .final_output = .{ .text = text } },
        .error_message => |message| .{ .error_reported = .{ .message = message, .component = "adapter" } },
    };
}

test "headless and pty output map to the same runtime event family" {
    const headless = toRuntimeEvent(.{ .stdout = "ok\n" });
    const pty = toRuntimeEvent(.{ .terminal_bytes = "ok\r\n" });

    try std.testing.expectEqual(runtime.StreamName.stdout, headless.adapter_output.stream);
    try std.testing.expectEqual(runtime.StreamName.terminal, pty.adapter_output.stream);
}

test "adapter final output maps to runtime final output" {
    const event = toRuntimeEvent(.{ .final_output = "done" });

    try std.testing.expectEqualStrings("done", event.final_output.text);
}

test "adapter process events map to runtime process events" {
    const spawned = toRuntimeEvent(.{ .process_spawned = .{
        .argv = &.{ "codex", "exec" },
        .cwd = "/workspace",
        .pid = null,
    } });
    const exited = toRuntimeEvent(.{ .process_exited = .{ .exit_code = 0 } });

    try std.testing.expectEqualStrings("codex", spawned.process_spawned.argv[0]);
    try std.testing.expectEqual(@as(u8, 0), exited.process_exited.exit_code.?);
}

test "adapter prompt events map to runtime prompt events" {
    const event = toRuntimeEvent(.{ .prompt_detected = .{
        .prompt_kind = "approval_requested",
        .text = "Run tests? [y/N]",
        .risk = "low",
    } });

    try std.testing.expectEqualStrings("approval_requested", event.prompt_detected.prompt_kind);
}
