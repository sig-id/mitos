//! Entry point for the standalone `mitos-term` binary. The BEAM orchestrator
//! launches one of these per session via `erlang:open_port({spawn_executable,
//! "mitos-term"}, [{packet,4}, binary, exit_status])`.
//!
//! The binary owns nothing but stdin/stdout and the single PTY it spawns;
//! crash isolation is the whole point — the sidecar dying takes down only one
//! session, which the supervising `Session` GenServer then restarts.
const std = @import("std");
const mitos = @import("mitos");

pub fn main() !void {
    var gpa = std.heap.GeneralPurposeAllocator(.{}){};
    defer _ = gpa.deinit();

    const code = mitos.sidecar.run.run(gpa.allocator()) catch |err| {
        std.debug.print("mitos-term: {s}\n", .{@errorName(err)});
        std.process.exit(2);
    };
    if (code != 0) std.process.exit(code);
}
