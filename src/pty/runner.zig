const std = @import("std");
const pty = @import("core.zig");
const ghostty = @import("../terminal/ghostty.zig");
const terminal = @import("../terminal/core.zig");
const unix = @import("unix.zig");

pub const DrainResult = struct {
    bytes_read: usize = 0,
    terminal_effects: usize = 0,
};

pub const RunnerError = pty.PtyError || terminal.TerminalError;

pub fn drainToTerminal(
    session: *unix.UnixPtySession,
    terminal_core: *terminal.TerminalCore,
    allocator: std.mem.Allocator,
) RunnerError!DrainResult {
    var result: DrainResult = .{};
    var buffer: [4096]u8 = undefined;
    var raw_pty = session.asPty();

    while (true) {
        const n = raw_pty.read(&raw_pty, &buffer) catch |err| switch (err) {
            error.WouldBlock, error.Closed => break,
            else => return err,
        };
        if (n == 0) break;

        result.bytes_read += n;
        const effects = try terminal_core.write_from_pty(terminal_core, buffer[0..n], allocator);
        result.terminal_effects += effects.items.len;
        effects.deinit(allocator);
    }

    return result;
}

test "pty runner spawns process and drains bytes into terminal core" {
    var session = try unix.spawn(std.testing.allocator, .{
        .argv = &.{ "/bin/sh", "-c", "printf 'hello'; printf '\\033[31mred\\033[0m'" },
        .size = .{ .cols = 32, .rows = 8 },
    });
    defer session.deinit();

    const term = session.wait();
    switch (term) {
        .exited => |code| try std.testing.expectEqual(@as(u8, 0), code),
        else => return error.TestUnexpectedResult,
    }

    var vt = try ghostty.GhosttyTerminal.init(std.testing.allocator, .{ .cols = 32, .rows = 8 });
    defer vt.deinit();
    var terminal_core = vt.asCore();

    const drained = try drainToTerminal(&session, &terminal_core, std.testing.allocator);
    try std.testing.expect(drained.bytes_read > 0);

    const text = try terminal_core.visible_text(&terminal_core, std.testing.allocator);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("hellored", text);

    const shot = try terminal_core.snapshot(&terminal_core, std.testing.allocator);
    defer shot.deinit(std.testing.allocator);
    var saw_red = false;
    for (shot.cells) |cell| {
        if (std.mem.eql(u8, cell.text, "r")) {
            if (cell.style.fg) |fg| switch (fg) {
                .indexed => |idx| {
                    if (idx == 1) saw_red = true;
                },
                else => {},
            };
        }
    }
    try std.testing.expect(saw_red);
}
