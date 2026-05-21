const std = @import("std");
const builtin = @import("builtin");

pub const unix = @import("unix.zig");
pub const windows = @import("windows.zig");

pub const PtyError = error{
    ChildSetupFailed,
    Closed,
    ExecFailed,
    ForkFailed,
    InvalidSize,
    OpenFailed,
    OutOfMemory,
    ReadFailed,
    ResizeFailed,
    SignalFailed,
    SpawnFailed,
    UnsupportedPlatform,
    WouldBlock,
    WriteFailed,
};

pub const PtySize = struct {
    cols: u16,
    rows: u16,
    width_px: u16 = 0,
    height_px: u16 = 0,

    pub fn validate(self: PtySize) PtyError!void {
        if (self.cols == 0 or self.rows == 0) return error.InvalidSize;
    }
};

pub const Pty = struct {
    context: *anyopaque,
    read: *const fn (*Pty, []u8) PtyError!usize,
    write: *const fn (*Pty, []const u8) PtyError!usize,
    resize: *const fn (*Pty, PtySize) PtyError!void,
    close: *const fn (*Pty) void,
};

pub const ProcessTerm = union(enum) {
    exited: u8,
    signaled: u32,
    stopped: u32,
    unknown: u32,
};

pub const NativePty = switch (builtin.os.tag) {
    .windows => windows.WindowsConPty,
    else => unix.UnixPty,
};

test "pty size rejects zero dimensions" {
    try std.testing.expectError(error.InvalidSize, (PtySize{ .cols = 0, .rows = 24 }).validate());
    try std.testing.expectError(error.InvalidSize, (PtySize{ .cols = 80, .rows = 0 }).validate());
    try (PtySize{ .cols = 80, .rows = 24 }).validate();
}
