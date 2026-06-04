const core = @import("core.zig");

pub const NativePty = core.NativePty;
pub const ProcessTerm = core.ProcessTerm;
pub const Pty = core.Pty;
pub const PtyError = core.PtyError;
pub const PtySize = core.PtySize;
pub const unix = @import("unix.zig");
pub const windows = @import("windows.zig");

test {
    const std = @import("std");

    std.testing.refAllDecls(@This());
}
