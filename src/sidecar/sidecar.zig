//! mitos-term: the per-session sidecar.
//!
//! Owns one PTY + one libghostty-vt parser, communicates with the BEAM
//! orchestrator over stdin/stdout using the framing in `protocol.zig`. The
//! display path (RAW frames) is opaque byte forwarding; the structured paths
//! (CONTROL/EVENT/SNAPSHOT) use MessagePack so the brain can reason about them.
//!
//! Process model: one OS process per session. A crash in libghostty or the PTY
//! takes down only this process; its supervising `Session` GenServer on the
//! BEAM restarts it. "Let it crash" carried across the FFI boundary — exactly
//! what a NIF would forfeit.
const std = @import("std");
const builtin = @import("builtin");
const pty_core = @import("../pty/core.zig");
const pty_unix = @import("../pty/unix.zig");
const terminal = @import("../terminal/core.zig");
const ghostty = @import("../terminal/ghostty.zig");
const protocol = @import("protocol.zig");
const msgpack = @import("msgpack.zig");

/// PTY read buffer. Sized to amortize syscalls without holding a frame too
/// long; the BEAM channel is fast so we want to flush often.
const pty_read_buffer_bytes: usize = 32 * 1024;

/// Default idle threshold (ms). After this many milliseconds of PTY silence,
/// we emit one `idle` EVENT. Configurable via the `open` command.
const default_idle_ms: i64 = 250;

const protocol_version: u32 = 1;

pub const RunError = error{
    UnsupportedPlatform,
    Bootstrap,
};

const Backend = enum { native, compat };

/// Top-level entry point: take stdin/stdout, drive the protocol loop until
/// shutdown or stdin EOF. Returns the exit code we want the process to use.
pub fn run(allocator: std.mem.Allocator) RunError!u8 {
    if (builtin.os.tag != .linux) return RunError.UnsupportedPlatform;

    const stdin_fd = std.posix.STDIN_FILENO;
    const stdout_fd = std.posix.STDOUT_FILENO;

    var state = State.init(allocator, stdin_fd, stdout_fd);
    defer state.deinit();

    state.runLoop() catch |err| switch (err) {
        error.OrderlyShutdown, error.Closed => return 0,
        else => {
            state.emitError("fatal", @errorName(err)) catch {};
            return 1;
        },
    };
    return 0;
}

const LoopError = error{
    OrderlyShutdown,
    Closed,
    OutOfMemory,
    IoError,
    Truncated,
    FrameTooLarge,
    EmptyFrame,
    UnknownStream,
    PollFailed,
    UnsupportedPlatform,
};

const State = struct {
    allocator: std.mem.Allocator,
    reader: protocol.FrameReader,
    writer: protocol.FrameWriter,

    // Set after a successful `open`. Owned by State; deinit closes both.
    session: ?pty_unix.UnixPtySession = null,
    terminal: ?ghostty.ManagedTerminal = null,
    backend: Backend = .compat,
    cols: u16 = 80,
    rows: u16 = 24,

    // Idle detection bookkeeping.
    idle_ms: i64 = default_idle_ms,
    last_output_ms: i64 = 0,
    idle_pending: bool = false, // false once an idle event has been emitted for the current quiet stretch

    // Monotonic seq for output_seq events; lets the brain correlate snapshot
    // requests with the RAW stream position they applied to.
    output_seq: u64 = 0,

    fn init(allocator: std.mem.Allocator, stdin_fd: std.posix.fd_t, stdout_fd: std.posix.fd_t) State {
        return .{
            .allocator = allocator,
            .reader = protocol.FrameReader.init(allocator, stdin_fd),
            .writer = protocol.FrameWriter.init(stdout_fd),
        };
    }

    fn deinit(self: *State) void {
        if (self.session) |*session| {
            _ = session.kill(15) catch {};
            _ = session.wait();
            session.deinit();
            self.session = null;
        }
        if (self.terminal) |*t| t.deinit();
        self.terminal = null;
    }

    fn runLoop(self: *State) LoopError!void {
        // First handshake: the brain sends `hello`, we reply with backend info
        // before any session is opened. We don't *require* it (the brain may
        // send `open` directly), but we keep the loop uniform.
        while (true) {
            const child_alive = self.session != null;
            // poll: [0] stdin, [1] pty (if open)
            var pollfds_buf: [2]std.posix.pollfd = undefined;
            pollfds_buf[0] = .{ .fd = self.reader.fd, .events = std.posix.POLL.IN, .revents = 0 };
            var nfds: usize = 1;
            if (self.session) |session| {
                pollfds_buf[1] = .{ .fd = session.pty.master_fd, .events = std.posix.POLL.IN, .revents = 0 };
                nfds = 2;
            }
            const pollfds = pollfds_buf[0..nfds];

            const timeout_ms: i32 = self.computePollTimeoutMs();
            _ = std.posix.poll(pollfds, timeout_ms) catch return error.PollFailed;

            // PTY first (drain it greedily before serving more control msgs so
            // RAW frames don't lag behind the actual screen state).
            if (child_alive and pollfds[1].revents & std.posix.POLL.IN != 0) {
                try self.pumpPty();
            }
            // Watch for child death via POLLHUP on the master.
            if (child_alive and pollfds[1].revents & (std.posix.POLL.HUP | std.posix.POLL.ERR) != 0) {
                // Drain anything still readable before reaping.
                self.pumpPty() catch {};
                try self.reapChild();
            }

            if (pollfds[0].revents & std.posix.POLL.IN != 0) {
                try self.handleStdin();
            }
            if (pollfds[0].revents & (std.posix.POLL.HUP | std.posix.POLL.ERR) != 0) {
                return error.Closed;
            }

            try self.maybeEmitIdle();
        }
    }

    fn computePollTimeoutMs(self: *State) i32 {
        if (self.session == null) return -1; // block until stdin speaks
        if (self.idle_pending) {
            const now = std.time.milliTimestamp();
            const elapsed = now - self.last_output_ms;
            const remaining = self.idle_ms - elapsed;
            if (remaining <= 0) return 0;
            return @intCast(@min(remaining, std.math.maxInt(i32)));
        }
        return -1;
    }

    fn maybeEmitIdle(self: *State) LoopError!void {
        if (!self.idle_pending) return;
        const now = std.time.milliTimestamp();
        if (now - self.last_output_ms < self.idle_ms) return;
        self.idle_pending = false;
        try self.emitIdleEvent(now - self.last_output_ms);
    }

    fn pumpPty(self: *State) LoopError!void {
        var session = &(self.session orelse return);
        var buf: [pty_read_buffer_bytes]u8 = undefined;
        // Drain everything available; PTY master is O_NONBLOCK.
        while (true) {
            const n = session.pty.read(&buf) catch |err| switch (err) {
                error.WouldBlock => return,
                error.Closed => {
                    try self.reapChild();
                    return;
                },
                else => {
                    self.emitError("pty_read", @errorName(err)) catch {};
                    return;
                },
            };
            if (n == 0) return;
            const bytes = buf[0..n];
            // Forward to the BEAM as opaque RAW — display path.
            self.writer.writeFrame(.raw, bytes) catch return error.IoError;
            // Feed the libghostty tap so snapshots are coherent. Any
            // terminal-response bytes (e.g. cursor-position report) go back to
            // the PTY synchronously.
            self.feedTerminal(bytes);
            self.output_seq +%= 1;
            self.last_output_ms = std.time.milliTimestamp();
            self.idle_pending = true;
        }
    }

    fn feedTerminal(self: *State, bytes: []const u8) void {
        if (self.terminal == null) return;
        var core = self.terminal.?.asCore();
        const effects = core.write_from_pty(&core, bytes, self.allocator) catch return;
        defer effects.deinit(self.allocator);
        for (effects.items) |effect| switch (effect) {
            .write_to_pty => |reply| {
                if (self.session) |*s| {
                    _ = s.pty.write(reply) catch {};
                }
            },
            .title_changed => |title| self.emitTitleEvent(title) catch {},
            .bell => self.emitSimpleEvent("bell") catch {},
            else => {},
        };
    }

    fn handleStdin(self: *State) LoopError!void {
        const frame = self.reader.readFrame() catch |err| switch (err) {
            error.Closed => return error.Closed,
            else => return err,
        };
        defer frame.deinit(self.allocator);

        if (frame.stream != .control) {
            try self.emitError("protocol", "non-control frame on stdin");
            return;
        }

        // Decode the control payload. We use a per-frame arena: every value
        // (strings, arrays, maps) lives until the arena is freed, which keeps
        // the dispatch code simple.
        var arena = std.heap.ArenaAllocator.init(self.allocator);
        defer arena.deinit();
        var dec = msgpack.Decoder.init(frame.body);
        const value = dec.decode(arena.allocator()) catch |err| {
            try self.emitError("decode", @errorName(err));
            return;
        };

        try self.dispatchCommand(value, arena.allocator());
    }

    fn dispatchCommand(self: *State, value: msgpack.Value, arena: std.mem.Allocator) LoopError!void {
        const cmd_value = value.mapGet("cmd") orelse {
            try self.emitError("protocol", "missing cmd");
            return;
        };
        if (cmd_value != .str) {
            try self.emitError("protocol", "cmd must be string");
            return;
        }
        const cmd = cmd_value.str;

        if (std.mem.eql(u8, cmd, "hello")) return self.handleHello();
        if (std.mem.eql(u8, cmd, "open")) return self.handleOpen(value, arena);
        if (std.mem.eql(u8, cmd, "write_stdin")) return self.handleWriteStdin(value);
        if (std.mem.eql(u8, cmd, "resize")) return self.handleResize(value);
        if (std.mem.eql(u8, cmd, "signal")) return self.handleSignal(value);
        if (std.mem.eql(u8, cmd, "snapshot")) return self.handleSnapshot(value);
        if (std.mem.eql(u8, cmd, "encode_key")) return self.handleEncodeKey(value, arena);
        if (std.mem.eql(u8, cmd, "shutdown")) {
            const grace_ms = if (value.mapGet("grace_ms")) |g| (g.asUint() orelse 100) else 100;
            try self.shutdownChild(@intCast(grace_ms));
            return error.OrderlyShutdown;
        }

        var buf: [128]u8 = undefined;
        const detail = std.fmt.bufPrint(&buf, "unknown cmd '{s}'", .{cmd}) catch "unknown cmd";
        try self.emitError("protocol", detail);
    }

    // -- command handlers --------------------------------------------------

    fn handleHello(self: *State) LoopError!void {
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "hello" } },
            .{ "proto", .{ .uint = protocol_version } },
            .{ "backend", .{ .str = backendName(self.backend) } },
        });
    }

    fn handleOpen(self: *State, value: msgpack.Value, arena: std.mem.Allocator) LoopError!void {
        if (self.session != null) {
            try self.emitError("state", "session already open");
            return;
        }

        const cols: u16 = if (value.mapGet("cols")) |v| @intCast(v.asUint() orelse 80) else 80;
        const rows: u16 = if (value.mapGet("rows")) |v| @intCast(v.asUint() orelse 24) else 24;
        if (value.mapGet("idle_ms")) |v| {
            if (v.asUint()) |ms| self.idle_ms = @intCast(ms);
        }

        const argv_value = value.mapGet("argv") orelse {
            try self.emitError("protocol", "open: missing argv");
            return;
        };
        if (argv_value != .array or argv_value.array.len == 0) {
            try self.emitError("protocol", "open: argv must be non-empty array");
            return;
        }
        const argv = collectStringArray(arena, argv_value.array) catch {
            try self.emitError("protocol", "open: argv must be array of strings");
            return;
        };

        const cwd_opt: ?[]const u8 = if (value.mapGet("cwd")) |v|
            (if (v == .str) v.str else null)
        else
            null;

        const env_list = collectEnv(arena, value.mapGet("env")) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };

        // Spawn under a libghostty-vt-managed terminal so snapshots are
        // available right away.
        var managed = ghostty.ManagedTerminal.init(self.allocator, .{
            .terminal = .{ .cols = cols, .rows = rows },
        }) catch |err| {
            try self.emitError("terminal", @errorName(err));
            return;
        };
        errdefer managed.deinit();
        self.backend = switch (managed) {
            .native => .native,
            .compat => .compat,
        };

        const session = pty_unix.spawn(self.allocator, .{
            .argv = argv,
            .cwd = cwd_opt,
            .env = env_list,
            .size = .{ .cols = cols, .rows = rows },
        }) catch |err| {
            try self.emitError("spawn", @errorName(err));
            managed.deinit();
            return;
        };

        self.terminal = managed;
        self.session = session;
        self.cols = cols;
        self.rows = rows;
        self.last_output_ms = std.time.milliTimestamp();
        self.idle_pending = false;
        self.output_seq = 0;

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "opened" } },
            .{ "pid", .{ .int = session.pid } },
            .{ "cols", .{ .uint = cols } },
            .{ "rows", .{ .uint = rows } },
        });
    }

    fn handleWriteStdin(self: *State, value: msgpack.Value) LoopError!void {
        var session = &(self.session orelse {
            try self.emitError("state", "no session");
            return;
        });
        const bytes_value = value.mapGet("bytes") orelse {
            try self.emitError("protocol", "write_stdin: missing bytes");
            return;
        };
        const bytes = switch (bytes_value) {
            .bin => |b| b,
            .str => |s| s,
            else => {
                try self.emitError("protocol", "write_stdin: bytes must be bin/str");
                return;
            },
        };
        _ = session.pty.write(bytes) catch |err| {
            try self.emitError("pty_write", @errorName(err));
        };
    }

    fn handleResize(self: *State, value: msgpack.Value) LoopError!void {
        var session = &(self.session orelse {
            try self.emitError("state", "no session");
            return;
        });
        const cols: u16 = if (value.mapGet("cols")) |v| @intCast(v.asUint() orelse self.cols) else self.cols;
        const rows: u16 = if (value.mapGet("rows")) |v| @intCast(v.asUint() orelse self.rows) else self.rows;
        if (cols == 0 or rows == 0) return;
        session.pty.resize(.{ .cols = cols, .rows = rows }) catch |err| {
            try self.emitError("pty_resize", @errorName(err));
            return;
        };
        self.cols = cols;
        self.rows = rows;
        if (self.terminal) |*t| {
            var core = t.asCore();
            core.resize(&core, cols, rows, .{ .width_px = 8, .height_px = 16 }) catch {};
        }
    }

    fn handleSignal(self: *State, value: msgpack.Value) LoopError!void {
        var session = &(self.session orelse {
            try self.emitError("state", "no session");
            return;
        });
        const sig: u8 = if (value.mapGet("signum")) |v| @intCast(v.asUint() orelse 15) else 15;
        session.kill(sig) catch |err| {
            try self.emitError("signal", @errorName(err));
        };
    }

    fn handleSnapshot(self: *State, value: msgpack.Value) LoopError!void {
        _ = value; // mode/lines reserved for scrollback support; visible-only for v1
        var t = &(self.terminal orelse {
            try self.emitError("state", "no terminal");
            return;
        });
        var core = t.asCore();
        const snap = core.snapshot(&core, self.allocator) catch |err| {
            try self.emitError("snapshot", @errorName(err));
            return;
        };
        defer snap.deinit(self.allocator);

        try self.emitSnapshotFrame(snap);
    }

    fn handleEncodeKey(self: *State, value: msgpack.Value, arena: std.mem.Allocator) LoopError!void {
        _ = arena;
        var t = &(self.terminal orelse {
            try self.emitError("state", "no terminal");
            return;
        });
        var core = t.asCore();

        const input: terminal.KeyInput = blk: {
            if (value.mapGet("text")) |text_v| {
                if (text_v == .str) break :blk .{ .text = text_v.str };
            }
            const key_v = value.mapGet("key") orelse {
                try self.emitError("protocol", "encode_key: missing key/text");
                return;
            };
            if (key_v != .str) {
                try self.emitError("protocol", "encode_key: key must be string");
                return;
            }
            const special = parseSpecialKey(key_v.str) orelse {
                try self.emitError("protocol", "encode_key: unknown key");
                return;
            };
            var mods: terminal.KeyModifiers = .{};
            if (value.mapGet("mods")) |m_v| {
                if (m_v == .map) {
                    if (m_v.mapGet("shift")) |b| if (b == .boolean) {
                        mods.shift = b.boolean;
                    };
                    if (m_v.mapGet("alt")) |b| if (b == .boolean) {
                        mods.alt = b.boolean;
                    };
                    if (m_v.mapGet("ctrl")) |b| if (b == .boolean) {
                        mods.ctrl = b.boolean;
                    };
                    if (m_v.mapGet("super")) |b| if (b == .boolean) {
                        mods.super = b.boolean;
                    };
                }
            }
            break :blk .{ .special = .{ .key = special, .modifiers = mods } };
        };

        const encoded = core.encode_key(&core, input, self.allocator) catch |err| {
            try self.emitError("encode_key", @errorName(err));
            return;
        };
        defer self.allocator.free(encoded);

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "key_encoded" } },
            .{ "bytes", .{ .bin = encoded } },
        });
    }

    fn shutdownChild(self: *State, grace_ms: u32) LoopError!void {
        if (self.session) |*session| {
            _ = session.kill(15) catch {};
            const deadline = std.time.milliTimestamp() + @as(i64, grace_ms);
            while (std.time.milliTimestamp() < deadline) {
                const result = std.posix.waitpid(session.pid, std.os.linux.W.NOHANG);
                if (result.pid != 0) {
                    // Child exited within the grace window and we just reaped
                    // it here. Mark it so the blocking wait() below is skipped
                    // — a second waitpid would hit ECHILD (unreachable).
                    session.waited = true;
                    break;
                }
                std.Thread.sleep(5 * std.time.ns_per_ms);
            }
            if (!session.waited) {
                _ = session.kill(9) catch {};
                _ = session.wait();
            }
        }
    }

    fn reapChild(self: *State) LoopError!void {
        var session = self.session orelse return;
        const term_status = session.wait();
        self.session = null;
        switch (term_status) {
            .exited => |code| try self.emitMap(.event, &.{
                .{ "type", .{ .str = "exit" } },
                .{ "code", .{ .uint = code } },
            }),
            .signaled => |sig| try self.emitMap(.event, &.{
                .{ "type", .{ .str = "exit" } },
                .{ "signal", .{ .uint = sig } },
            }),
            else => try self.emitMap(.event, &.{
                .{ "type", .{ .str = "exit" } },
                .{ "code", .{ .int = -1 } },
            }),
        }
        session.deinit();
        return error.OrderlyShutdown;
    }

    // -- event emitters ----------------------------------------------------

    fn emitSimpleEvent(self: *State, type_name: []const u8) LoopError!void {
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = type_name } },
        });
    }

    fn emitTitleEvent(self: *State, title: []const u8) LoopError!void {
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "title" } },
            .{ "text", .{ .str = title } },
        });
    }

    fn emitIdleEvent(self: *State, elapsed_ms: i64) LoopError!void {
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "idle" } },
            .{ "ms", .{ .int = elapsed_ms } },
            .{ "seq", .{ .uint = self.output_seq } },
        });
    }

    fn emitError(self: *State, kind: []const u8, detail: []const u8) LoopError!void {
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "error" } },
            .{ "kind", .{ .str = kind } },
            .{ "detail", .{ .str = detail } },
        });
    }

    /// Convenience: write a small flat map ({"type": ..., ...}) as a single
    /// frame. Caller passes a literal slice of (key, value) tuples.
    fn emitMap(
        self: *State,
        stream: protocol.Stream,
        entries: []const struct { []const u8, msgpack.Value },
    ) LoopError!void {
        var buf: std.ArrayList(u8) = .empty;
        defer buf.deinit(self.allocator);
        var w = msgpack.Writer.init(self.allocator, &buf);
        w.beginMap(entries.len) catch return error.OutOfMemory;
        for (entries) |entry| {
            w.writeStr(entry[0]) catch return error.OutOfMemory;
            w.writeValue(entry[1]) catch return error.OutOfMemory;
        }
        self.writer.writeFrame(stream, buf.items) catch return error.IoError;
    }

    fn emitSnapshotFrame(self: *State, snap: terminal.ScreenSnapshot) LoopError!void {
        var buf: std.ArrayList(u8) = .empty;
        defer buf.deinit(self.allocator);
        var w = msgpack.Writer.init(self.allocator, &buf);

        // Top-level: { seq, cols, rows, cursor: {x,y,visible}, cells: [Cell...] }
        // Cells are emitted as a flat sequence keyed by their grid coordinates
        // — the BEAM reassembles into rows. Span/RLE encoding is a v1.1 add
        // (the on-the-wire shape stays additive: extra keys are allowed).
        const n_keys: usize = if (snap.title != null) 6 else 5;
        w.beginMap(n_keys) catch return error.OutOfMemory;

        w.writeStr("seq") catch return error.OutOfMemory;
        w.writeUint(self.output_seq) catch return error.OutOfMemory;
        w.writeStr("cols") catch return error.OutOfMemory;
        w.writeUint(snap.cols) catch return error.OutOfMemory;
        w.writeStr("rows") catch return error.OutOfMemory;
        w.writeUint(snap.rows) catch return error.OutOfMemory;

        w.writeStr("cursor") catch return error.OutOfMemory;
        w.beginMap(3) catch return error.OutOfMemory;
        w.writeStr("x") catch return error.OutOfMemory;
        w.writeUint(snap.cursor.x) catch return error.OutOfMemory;
        w.writeStr("y") catch return error.OutOfMemory;
        w.writeUint(snap.cursor.y) catch return error.OutOfMemory;
        w.writeStr("visible") catch return error.OutOfMemory;
        w.writeBool(snap.cursor.visible) catch return error.OutOfMemory;

        if (snap.title) |title| {
            w.writeStr("title") catch return error.OutOfMemory;
            w.writeStr(title) catch return error.OutOfMemory;
        }

        w.writeStr("cells") catch return error.OutOfMemory;
        w.beginArray(snap.cells.len) catch return error.OutOfMemory;
        for (snap.cells) |cell| {
            // Each cell: {x, y, t (text), s (style bitfield), fg, bg}
            // fg/bg omitted when null to keep the wire compact.
            var keys: usize = 4;
            if (cell.style.fg != null) keys += 1;
            if (cell.style.bg != null) keys += 1;
            w.beginMap(keys) catch return error.OutOfMemory;

            w.writeStr("x") catch return error.OutOfMemory;
            w.writeUint(cell.x) catch return error.OutOfMemory;
            w.writeStr("y") catch return error.OutOfMemory;
            w.writeUint(cell.y) catch return error.OutOfMemory;
            w.writeStr("t") catch return error.OutOfMemory;
            w.writeStr(cell.text) catch return error.OutOfMemory;
            w.writeStr("s") catch return error.OutOfMemory;
            w.writeUint(encodeStyleAttrs(cell.style)) catch return error.OutOfMemory;
            if (cell.style.fg) |fg| {
                w.writeStr("fg") catch return error.OutOfMemory;
                writeColor(&w, fg) catch return error.OutOfMemory;
            }
            if (cell.style.bg) |bg| {
                w.writeStr("bg") catch return error.OutOfMemory;
                writeColor(&w, bg) catch return error.OutOfMemory;
            }
        }

        self.writer.writeFrame(.snapshot, buf.items) catch return error.IoError;
    }
};

// -- helpers ----------------------------------------------------------------

fn backendName(b: Backend) []const u8 {
    return switch (b) {
        .native => "native",
        .compat => "compat",
    };
}

/// Style bitfield used by Span/Cell. Matches the diagram in the design doc.
fn encodeStyleAttrs(style: terminal.CellStyle) u64 {
    var bits: u64 = 0;
    if (style.bold) bits |= 1 << 0;
    if (style.italic) bits |= 1 << 2;
    if (style.underline) bits |= 1 << 3;
    if (style.inverse) bits |= 1 << 5;
    return bits;
}

fn writeColor(w: *msgpack.Writer, color: terminal.Color) !void {
    switch (color) {
        .indexed => |idx| {
            try w.beginMap(2);
            try w.writeStr("kind");
            try w.writeStr("palette");
            try w.writeStr("v");
            try w.writeUint(idx);
        },
        .rgb => |rgb| {
            try w.beginMap(2);
            try w.writeStr("kind");
            try w.writeStr("rgb");
            try w.writeStr("v");
            try w.beginArray(3);
            try w.writeUint(rgb.r);
            try w.writeUint(rgb.g);
            try w.writeUint(rgb.b);
        },
    }
}

fn collectStringArray(arena: std.mem.Allocator, items: []const msgpack.Value) ![][]const u8 {
    const out = try arena.alloc([]const u8, items.len);
    for (items, 0..) |item, i| {
        if (item != .str) return error.InvalidArgument;
        out[i] = item.str;
    }
    return out;
}

/// env may be encoded two ways: a map {"KEY": "VAL"} or an array of {name,
/// value} maps. We accept both for ergonomics and normalize to KEY=VAL.
fn collectEnv(arena: std.mem.Allocator, value: ?msgpack.Value) ![][]const u8 {
    const v = value orelse return arena.alloc([]const u8, 0);
    if (v == .nil) return arena.alloc([]const u8, 0);

    var list: std.ArrayList([]const u8) = .empty;
    if (v == .map) {
        try list.ensureTotalCapacity(arena, v.map.len);
        for (v.map) |pair| {
            if (pair.key != .str or pair.value != .str) return error.InvalidArgument;
            const joined = try std.fmt.allocPrint(arena, "{s}={s}", .{ pair.key.str, pair.value.str });
            try list.append(arena, joined);
        }
    } else if (v == .array) {
        try list.ensureTotalCapacity(arena, v.array.len);
        for (v.array) |item| {
            if (item != .map) return error.InvalidArgument;
            const name = item.mapGet("name") orelse return error.InvalidArgument;
            const val = item.mapGet("value") orelse return error.InvalidArgument;
            if (name != .str or val != .str) return error.InvalidArgument;
            const joined = try std.fmt.allocPrint(arena, "{s}={s}", .{ name.str, val.str });
            try list.append(arena, joined);
        }
    } else return error.InvalidArgument;

    return list.toOwnedSlice(arena);
}

fn parseSpecialKey(name: []const u8) ?terminal.SpecialKey {
    const map = std.StaticStringMap(terminal.SpecialKey).initComptime(.{
        .{ "enter", .enter },
        .{ "escape", .escape },
        .{ "backspace", .backspace },
        .{ "tab", .tab },
        .{ "up", .up },
        .{ "down", .down },
        .{ "left", .left },
        .{ "right", .right },
        .{ "home", .home },
        .{ "end", .end },
        .{ "page_up", .page_up },
        .{ "page_down", .page_down },
        .{ "delete", .delete },
        .{ "insert", .insert },
    });
    return map.get(name);
}

// -- tests ------------------------------------------------------------------

test "encodeStyleAttrs sets bold and underline bits" {
    const style: terminal.CellStyle = .{ .bold = true, .underline = true };
    const bits = encodeStyleAttrs(style);
    try std.testing.expect(bits & (1 << 0) != 0);
    try std.testing.expect(bits & (1 << 3) != 0);
    try std.testing.expect(bits & (1 << 2) == 0);
}

test "collectEnv handles map form" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const pairs = try a.alloc(msgpack.Value.Pair, 2);
    pairs[0] = .{ .key = .{ .str = "PATH" }, .value = .{ .str = "/usr/bin" } };
    pairs[1] = .{ .key = .{ .str = "TERM" }, .value = .{ .str = "xterm-256color" } };
    const env = try collectEnv(a, .{ .map = pairs });
    try std.testing.expectEqual(@as(usize, 2), env.len);
    try std.testing.expectEqualStrings("PATH=/usr/bin", env[0]);
    try std.testing.expectEqualStrings("TERM=xterm-256color", env[1]);
}

test "parseSpecialKey resolves arrow keys" {
    try std.testing.expectEqual(terminal.SpecialKey.up, parseSpecialKey("up").?);
    try std.testing.expectEqual(terminal.SpecialKey.page_down, parseSpecialKey("page_down").?);
    try std.testing.expect(parseSpecialKey("nope") == null);
}
