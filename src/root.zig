pub const version = "0.1.0";

pub const adapters = @import("adapters/root.zig");
pub const approvals = @import("approvals/store.zig");
pub const config = @import("config/types.zig");
pub const nucleus = @import("sandbox/nucleus.zig");
pub const policy = @import("policy/mod.zig");
pub const prompt = @import("prompt/root.zig");
pub const providers = @import("providers/root.zig");
pub const pty = @import("pty/root.zig");
pub const redaction = @import("redaction/redactor.zig");
pub const report = @import("report/generator.zig");
pub const runtime = @import("runtime/mod.zig");
pub const sandbox = @import("sandbox/backend.zig");
pub const steer = @import("steer/root.zig");
pub const terminal = @import("terminal/core.zig");
pub const trace = @import("trace/mod.zig");
pub const transport = @import("transport/root.zig");
pub const workspace = @import("workspace/worktree.zig");

pub const terminal_ghostty = @import("terminal/ghostty.zig");

pub const sidecar = struct {
    pub const protocol = @import("sidecar/protocol.zig");
    pub const msgpack = @import("sidecar/msgpack.zig");
    pub const run = @import("sidecar/sidecar.zig");
};

test {
    const std = @import("std");

    std.testing.refAllDecls(@This());
    std.testing.refAllDecls(terminal_ghostty);
    std.testing.refAllDecls(sidecar.protocol);
    std.testing.refAllDecls(sidecar.msgpack);
    std.testing.refAllDecls(sidecar.run);
}
