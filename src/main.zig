const std = @import("std");
const mitos = @import("mitos");

pub fn main() !void {
    var stdout_buffer: [256]u8 = undefined;
    var stdout_writer = std.fs.File.stdout().writer(&stdout_buffer);
    const stdout = &stdout_writer.interface;

    try stdout.print("mitos {s}\n", .{mitos.version});
    try stdout.flush();
}

test "cli module imports library metadata" {
    try std.testing.expect(mitos.version.len > 0);
}
