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
const adapters = @import("../adapters/types.zig");
const prompt = @import("../prompt/root.zig");
const headless = @import("../providers/headless.zig");
const provider_launch = @import("../providers/launch.zig");
const terminal = @import("../terminal/core.zig");
const ghostty = @import("../terminal/ghostty.zig");
const transport = @import("../transport/root.zig");
const protocol = @import("protocol.zig");
const msgpack = @import("msgpack.zig");

/// PTY read buffer. Sized to amortize syscalls without holding a frame too
/// long; the BEAM channel is fast so we want to flush often.
const pty_read_buffer_bytes: usize = 32 * 1024;

/// Default idle threshold (ms). After this many milliseconds of PTY silence,
/// we emit one `idle` EVENT. Configurable via the `open` command.
const default_idle_ms: i64 = 250;

const protocol_version: u32 = transport.protocol_version;

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
    provider: adapters.ProviderId = .{ .value = "generic-pty" },
    adapter: adapters.AdapterId = .{ .value = "generic-pty.terminal" },
    selected_transport: transport.TransportName = .pty,
    tier2_fast_path_rules: []const []const u8 = &.{},
    redaction_patterns: []const []const u8 = &.{},
    next_action_id: u64 = 1,
    raw_spool_file: ?std.fs.File = null,
    raw_spool_path: ?[]u8 = null,
    raw_spool_offset: u64 = 0,

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
        if (self.raw_spool_file) |file| file.close();
        self.raw_spool_file = null;
        if (self.raw_spool_path) |path| self.allocator.free(path);
        self.raw_spool_path = null;
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
        try self.detectPromptsOnIdle();
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
            const boundary_bytes = redactPatternsAlloc(self.allocator, bytes, self.redaction_patterns) catch return error.OutOfMemory;
            defer boundary_bytes.deinit(self.allocator);
            self.writer.writeFrame(.raw, boundary_bytes.bytes) catch return error.IoError;
            try self.appendRawSpool(bytes);
            // Feed the libghostty tap so snapshots are coherent. Any
            // terminal-response bytes (e.g. cursor-position report) go back to
            // the PTY synchronously.
            self.feedTerminal(bytes);
            self.output_seq +%= 1;
            self.last_output_ms = std.time.milliTimestamp();
            self.idle_pending = true;
        }
    }

    fn appendRawSpool(self: *State, bytes: []const u8) LoopError!void {
        if (bytes.len == 0) return;
        try self.ensureRawSpool();

        const offset = self.raw_spool_offset;
        const len: u64 = @intCast(bytes.len);
        var file = self.raw_spool_file.?;
        file.writeAll(bytes) catch |err| {
            self.emitError("raw_spool", @errorName(err)) catch {};
            return error.IoError;
        };
        self.raw_spool_offset += len;

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = transport.TypedEventName.raw_ref.wireName() } },
            .{ "offset", .{ .uint = offset } },
            .{ "len", .{ .uint = len } },
            .{ "path", .{ .str = self.raw_spool_path.? } },
        });
    }

    fn ensureRawSpool(self: *State) LoopError!void {
        if (self.raw_spool_file != null) return;

        const path = std.fmt.allocPrint(
            self.allocator,
            "/tmp/mitos-terminal-{d}.raw",
            .{std.time.nanoTimestamp()},
        ) catch return error.OutOfMemory;
        errdefer self.allocator.free(path);

        const file = std.fs.createFileAbsolute(path, .{
            .truncate = true,
            .exclusive = true,
        }) catch |err| {
            self.emitError("raw_spool", @errorName(err)) catch {};
            return error.IoError;
        };

        self.raw_spool_file = file;
        self.raw_spool_path = path;
        self.raw_spool_offset = 0;
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

        if (std.ascii.eqlIgnoreCase(cmd, "hello")) return self.handleHello();
        if (std.ascii.eqlIgnoreCase(cmd, "open")) return self.handleOpen(value, arena);
        if (std.ascii.eqlIgnoreCase(cmd, "input") or std.mem.eql(u8, cmd, "write_stdin")) return self.handleWriteStdin(value);
        if (std.ascii.eqlIgnoreCase(cmd, "resize")) return self.handleResize(value);
        if (std.ascii.eqlIgnoreCase(cmd, "signal")) return self.handleSignal(value);
        if (std.ascii.eqlIgnoreCase(cmd, "authorize")) return self.handleAuthorize(value);
        if (std.ascii.eqlIgnoreCase(cmd, "deny")) return self.handleDeny(value);
        if (std.mem.eql(u8, cmd, "SNAPSHOT_REQUEST") or std.mem.eql(u8, cmd, "snapshot")) return self.handleSnapshot(value);
        if (std.ascii.eqlIgnoreCase(cmd, "cancel")) return self.handleCancel();
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

        const launch_value = value.mapGet("launch_spec") orelse value.mapGet("launch") orelse value.mapGet("spec");
        const control = launch_value orelse value;

        const cols: u16 = if (control.mapGet("cols")) |v| @intCast(v.asUint() orelse 80) else 80;
        const rows: u16 = if (control.mapGet("rows")) |v| @intCast(v.asUint() orelse 24) else 24;
        if (control.mapGet("idle_ms")) |v| {
            if (v.asUint()) |ms| self.idle_ms = @intCast(ms);
        }

        var resolved_launch: ?provider_launch.ResolvedLaunch = null;
        defer if (resolved_launch) |resolved| resolved.deinit(self.allocator);

        var argv: []const []const u8 = &.{};
        var cwd_opt: ?[]const u8 = null;
        var initial_input: ?[]const u8 = null;
        var authority: transport.AuthorityPlane = .{};

        if (launch_value) |launch| {
            const spec = parseLaunchSpec(arena, launch) catch |err| {
                try self.emitError("protocol", @errorName(err));
                return;
            };
            const resolved = provider_launch.resolve(self.allocator, spec) catch |err| {
                try self.emitError("launch", @errorName(err));
                return;
            };
            self.provider = resolved.provider;
            self.adapter = resolved.adapter;
            self.selected_transport = resolved.transport_name;
            self.tier2_fast_path_rules = spec.policy.tier2_fast_path_rules;
            self.redaction_patterns = spec.policy.redaction_patterns;
            authority = spec.authority;
            argv = resolved.argv;
            cwd_opt = resolved.cwd;
            initial_input = resolved.initial_input;
            resolved_launch = resolved;
        } else {
            const argv_value = value.mapGet("argv") orelse {
                try self.emitError("protocol", "open: missing argv");
                return;
            };
            if (argv_value != .array or argv_value.array.len == 0) {
                try self.emitError("protocol", "open: argv must be non-empty array");
                return;
            }
            argv = collectStringArray(arena, argv_value.array) catch {
                try self.emitError("protocol", "open: argv must be array of strings");
                return;
            };

            cwd_opt = if (value.mapGet("cwd")) |v|
                (if (v == .str) v.str else null)
            else
                null;
            self.provider = .{ .value = "generic-pty" };
            self.adapter = .{ .value = "generic-pty.terminal" };
            self.selected_transport = .pty;
        }

        const env_list = collectEnvWithAuthority(arena, control.mapGet("env"), authority) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };
        const adapter_env = collectAdapterEnvVarsWithAuthority(arena, control.mapGet("env"), authority) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };

        if (self.selected_transport == .stdio_json) {
            return self.runStdioJson(resolved_launch.?, adapter_env);
        }

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

        const axes = self.selected_transport.axes();
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "RUN_STARTED" } },
            .{ "proto", .{ .uint = protocol_version } },
            .{ "provider", .{ .str = self.provider.value } },
            .{ "adapter", .{ .str = self.adapter.value } },
            .{ "transport", .{ .str = self.selected_transport.name() } },
            .{ "codec", .{ .str = axes.codec.name() } },
            .{ "lifecycle", .{ .str = axes.lifecycle.name() } },
        });

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "opened" } },
            .{ "pid", .{ .int = session.pid } },
            .{ "cols", .{ .uint = cols } },
            .{ "rows", .{ .uint = rows } },
        });
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "PROCESS_SPAWNED" } },
            .{ "pid", .{ .int = session.pid } },
            .{ "provider", .{ .str = self.provider.value } },
            .{ "adapter", .{ .str = self.adapter.value } },
            .{ "transport", .{ .str = self.selected_transport.name() } },
            .{ "cwd", .{ .str = cwd_opt orelse "." } },
        });

        if (initial_input) |input| {
            if (input.len > 0) {
                _ = self.session.?.pty.write(input) catch |err| {
                    try self.emitError("pty_write", @errorName(err));
                    return;
                };
            }
        }
    }

    fn runStdioJson(
        self: *State,
        resolved: provider_launch.ResolvedLaunch,
        env: []const adapters.EnvVar,
    ) LoopError!void {
        const axes = self.selected_transport.axes();
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "RUN_STARTED" } },
            .{ "proto", .{ .uint = protocol_version } },
            .{ "provider", .{ .str = self.provider.value } },
            .{ "adapter", .{ .str = self.adapter.value } },
            .{ "transport", .{ .str = self.selected_transport.name() } },
            .{ "codec", .{ .str = axes.codec.name() } },
            .{ "lifecycle", .{ .str = axes.lifecycle.name() } },
        });
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "PROCESS_SPAWNED" } },
            .{ "provider", .{ .str = self.provider.value } },
            .{ "adapter", .{ .str = self.adapter.value } },
            .{ "transport", .{ .str = self.selected_transport.name() } },
            .{ "cwd", .{ .str = resolved.cwd orelse "." } },
        });

        const result = headless.execute(self.allocator, .{
            .argv = resolved.argv,
            .cwd = resolved.cwd orelse ".",
            .environment = env,
        }, .{}) catch |err| {
            try self.emitError("spawn", @errorName(err));
            return error.OrderlyShutdown;
        };
        defer result.deinit(self.allocator);

        if (result.stdout.len > 0) {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
                .{ "stream", .{ .str = "stdout" } },
                .{ "bytes", .{ .bin = result.stdout } },
            });
        }
        if (result.stderr.len > 0) {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
                .{ "stream", .{ .str = "stderr" } },
                .{ "bytes", .{ .bin = result.stderr } },
            });
        }

        const final_output = headless.extractFinalOutputAlloc(self.allocator, result.stdout) catch |err| {
            try self.emitError("codec", @errorName(err));
            return error.OrderlyShutdown;
        };
        defer self.allocator.free(final_output);
        if (final_output.len > 0) {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "FINAL_OUTPUT" } },
                .{ "text", .{ .str = final_output } },
            });
            try self.emitPromptsFromText(final_output);
        }

        if (result.status() != .completed) {
            try self.emitError("process", if (result.stderr.len > 0) result.stderr else "stdio-json provider command failed");
        }

        if (result.exit_code) |code| {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "PROCESS_EXITED" } },
                .{ "code", .{ .uint = code } },
            });
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "exit" } },
                .{ "code", .{ .uint = code } },
            });
        } else if (result.signal) |signal| {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "PROCESS_EXITED" } },
                .{ "signal", .{ .uint = signal } },
            });
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "exit" } },
                .{ "signal", .{ .uint = signal } },
            });
        } else {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "PROCESS_EXITED" } },
            });
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "exit" } },
                .{ "code", .{ .int = -1 } },
            });
        }

        return error.OrderlyShutdown;
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

    fn handleCancel(self: *State) LoopError!void {
        var session = &(self.session orelse {
            try self.emitError("state", "no session");
            return;
        });
        session.kill(15) catch |err| {
            try self.emitError("cancel", @errorName(err));
        };
    }

    fn handleAuthorize(self: *State, value: msgpack.Value) LoopError!void {
        const action_id = stringField(value, "action_id") orelse {
            try self.emitError("protocol", "authorize: missing action_id");
            return;
        };
        const token = stringField(value, "token") orelse {
            try self.emitError("protocol", "authorize: missing token");
            return;
        };

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
            .{ "kind", .{ .str = "authorization" } },
            .{ "action_id", .{ .str = action_id } },
            .{ "decision", .{ .str = "allow" } },
            .{ "token", .{ .str = token } },
        });
    }

    fn handleDeny(self: *State, value: msgpack.Value) LoopError!void {
        const action_id = stringField(value, "action_id") orelse {
            try self.emitError("protocol", "deny: missing action_id");
            return;
        };
        const reason = stringField(value, "reason") orelse {
            try self.emitError("protocol", "deny: missing reason");
            return;
        };

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
            .{ "kind", .{ .str = "authorization" } },
            .{ "action_id", .{ .str = action_id } },
            .{ "decision", .{ .str = "deny" } },
            .{ "reason", .{ .str = reason } },
        });
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
                .{ "type", .{ .str = "PROCESS_EXITED" } },
                .{ "code", .{ .uint = code } },
            }),
            .signaled => |sig| try self.emitMap(.event, &.{
                .{ "type", .{ .str = "PROCESS_EXITED" } },
                .{ "signal", .{ .uint = sig } },
            }),
            else => try self.emitMap(.event, &.{
                .{ "type", .{ .str = "PROCESS_EXITED" } },
            }),
        }
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

    fn detectPromptsOnIdle(self: *State) LoopError!void {
        var t = &(self.terminal orelse return);
        var core = t.asCore();
        const visible = core.visible_text(&core, self.allocator) catch |err| {
            try self.emitError("prompt", @errorName(err));
            return;
        };
        defer self.allocator.free(visible);
        const scrollback = core.scrollback_text(&core, self.allocator, 4096) catch |err| {
            try self.emitError("prompt", @errorName(err));
            return;
        };
        defer self.allocator.free(scrollback);

        const events = prompt.providers.detect(.{
            .visible_text = visible,
            .scrollback_text = scrollback,
        }, .{
            .provider = self.provider,
            .adapter = self.adapter,
            .timestamp_ms = std.time.milliTimestamp(),
        }, self.allocator) catch |err| {
            try self.emitError("prompt", @errorName(err));
            return;
        };
        defer prompt.freeEvents(self.allocator, events);

        for (events) |event| {
            try self.emitPromptEvent(event);
        }
    }

    fn emitPromptsFromText(self: *State, text: []const u8) LoopError!void {
        const trimmed = std.mem.trim(u8, text, " \t\r\n");
        if (trimmed.len == 0) return;

        const events = prompt.providers.detect(.{
            .visible_text = trimmed,
            .scrollback_text = trimmed,
        }, .{
            .provider = self.provider,
            .adapter = self.adapter,
            .timestamp_ms = std.time.milliTimestamp(),
        }, self.allocator) catch |err| {
            try self.emitError("prompt", @errorName(err));
            return;
        };
        defer prompt.freeEvents(self.allocator, events);

        for (events) |event| {
            try self.emitPromptEvent(event);
        }
    }

    fn emitPromptEvent(self: *State, event: prompt.PromptEvent) LoopError!void {
        switch (event) {
            .approval_requested => |approval| {
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "approval_requested" } },
                    .{ "text", .{ .str = approval.text } },
                    .{ "risk", .{ .str = @tagName(approval.risk) } },
                });
                try self.emitActionRequestUnlessFastPath(approval.text, @tagName(approval.risk));
            },
            .question_asked => |question| {
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "question_asked" } },
                    .{ "text", .{ .str = question.text } },
                });
            },
            .final_answer_candidate => |text| {
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "final_answer_candidate" } },
                    .{ "text", .{ .str = text } },
                });
            },
            .idle_detected => {
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "idle_detected" } },
                    .{ "text", .{ .str = "" } },
                });
            },
            .error_detected => |text| {
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "error_detected" } },
                    .{ "text", .{ .str = text } },
                    .{ "risk", .{ .str = "high" } },
                });
            },
        }
    }

    fn emitActionRequestUnlessFastPath(
        self: *State,
        prompt_text: []const u8,
        risk: []const u8,
    ) LoopError!void {
        if (matchesAnyPattern(prompt_text, self.tier2_fast_path_rules)) return;

        var id_buf: [64]u8 = undefined;
        const action_id = std.fmt.bufPrint(
            &id_buf,
            "action-{d}",
            .{self.next_action_id},
        ) catch "action";
        self.next_action_id += 1;

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "ACTION_REQUESTED" } },
            .{ "action_id", .{ .str = action_id } },
            .{ "action", .{ .str = "terminal_input" } },
            .{ "risk", .{ .str = risk } },
            .{ "context", .{ .str = prompt_text } },
        });
    }

    fn emitError(self: *State, kind: []const u8, detail: []const u8) LoopError!void {
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = transport.TypedEventName.error_reported.wireName() } },
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

const BoundaryBytes = struct {
    bytes: []const u8,
    owned: ?[]u8 = null,

    fn deinit(self: BoundaryBytes, allocator: std.mem.Allocator) void {
        if (self.owned) |owned| allocator.free(owned);
    }
};

fn redactPatternsAlloc(
    allocator: std.mem.Allocator,
    bytes: []const u8,
    patterns: []const []const u8,
) !BoundaryBytes {
    var current = bytes;
    var owned: ?[]u8 = null;
    errdefer if (owned) |old| allocator.free(old);

    for (patterns) |pattern| {
        if (pattern.len == 0) continue;
        if (std.mem.indexOf(u8, current, pattern) == null) continue;

        const replaced = try std.mem.replaceOwned(u8, allocator, current, pattern, "[REDACTED]");
        if (owned) |old| allocator.free(old);
        current = replaced;
        owned = replaced;
    }

    return .{ .bytes = current, .owned = owned };
}

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

fn parseLaunchSpec(arena: std.mem.Allocator, value: msgpack.Value) !transport.LaunchSpec {
    if (value != .map) return error.InvalidArgument;

    if (value.mapGet("version")) |version_value| {
        const version = version_value.asUint() orelse return error.InvalidArgument;
        if (version != transport.protocol_version) return error.UnsupportedProtocolVersion;
    }

    const provider_value = value.mapGet("provider") orelse return error.MissingProvider;
    const task_value = value.mapGet("task") orelse return error.MissingTask;
    if (provider_value != .str or task_value != .str) return error.InvalidArgument;

    const order_value = value.mapGet("transport_order") orelse value.mapGet("adapter_order");
    const order = try collectTransportOrder(arena, order_value);
    const env = try collectEnvVars(arena, value.mapGet("env"));
    const launch_policy = try parseLaunchPolicy(arena, value.mapGet("policy"));
    const authority = try parseAuthorityPlane(value.mapGet("authority"));
    const argv = if (value.mapGet("argv")) |argv_value| argv: {
        if (argv_value != .array) return error.InvalidArgument;
        break :argv try collectStringArray(arena, argv_value.array);
    } else &.{};

    const cols: u16 = if (value.mapGet("cols")) |v| @intCast(v.asUint() orelse 80) else 80;
    const rows: u16 = if (value.mapGet("rows")) |v| @intCast(v.asUint() orelse 24) else 24;
    const idle_ms: i64 = if (value.mapGet("idle_ms")) |v| @intCast(v.asUint() orelse default_idle_ms) else default_idle_ms;
    const cwd: ?[]const u8 = if (value.mapGet("cwd")) |v|
        (if (v == .str) v.str else null)
    else
        null;

    return .{
        .version = transport.protocol_version,
        .provider = provider_value.str,
        .task = task_value.str,
        .transport_order = order,
        .cwd = cwd,
        .env = env,
        .cols = cols,
        .rows = rows,
        .idle_ms = idle_ms,
        .argv = argv,
        .policy = launch_policy,
        .authority = authority,
    };
}

fn parseAuthorityPlane(value: ?msgpack.Value) !transport.AuthorityPlane {
    const v = value orelse return .{};
    if (v == .nil) return .{};
    if (v != .map) return error.InvalidArgument;

    return .{
        .socket_path = stringField(v, "socket_path"),
        .token = stringField(v, "token"),
        .project_id = stringField(v, "project_id"),
        .run_id = stringField(v, "run_id"),
    };
}

fn parseLaunchPolicy(
    arena: std.mem.Allocator,
    value: ?msgpack.Value,
) !transport.LaunchPolicy {
    const v = value orelse return .{};
    if (v == .nil) return .{};
    if (v != .map) return error.InvalidArgument;

    return .{
        .tier1_launch_config = stringField(v, "tier1_launch_config"),
        .tier2_fast_path_rules = try collectStringList(arena, v.mapGet("tier2_fast_path_rules")),
        .redaction_patterns = try collectStringList(arena, v.mapGet("redaction_patterns")),
    };
}

fn collectTransportOrder(
    arena: std.mem.Allocator,
    value: ?msgpack.Value,
) ![]const transport.TransportName {
    const v = value orelse return &.{.pty};
    if (v == .nil) return &.{.pty};
    if (v != .array) return error.InvalidArgument;

    var list: std.ArrayList(transport.TransportName) = .empty;
    for (v.array) |item| {
        if (item != .str) return error.InvalidArgument;
        if (transport.TransportName.fromName(item.str)) |name| {
            try list.append(arena, name);
        }
    }
    if (list.items.len == 0) return &.{.pty};
    return list.toOwnedSlice(arena);
}

fn collectStringList(arena: std.mem.Allocator, value: ?msgpack.Value) ![]const []const u8 {
    const v = value orelse return &.{};
    if (v == .nil) return &.{};
    if (v != .array) return error.InvalidArgument;

    const out = try arena.alloc([]const u8, v.array.len);
    for (v.array, 0..) |item, i| {
        if (item != .str) return error.InvalidArgument;
        out[i] = item.str;
    }
    return out;
}

fn collectEnvVars(arena: std.mem.Allocator, value: ?msgpack.Value) ![]const transport.EnvVar {
    const v = value orelse return &.{};
    if (v == .nil) return &.{};

    var list: std.ArrayList(transport.EnvVar) = .empty;
    if (v == .map) {
        try list.ensureTotalCapacity(arena, v.map.len);
        for (v.map) |pair| {
            if (pair.key != .str or pair.value != .str) return error.InvalidArgument;
            list.appendAssumeCapacity(.{ .name = pair.key.str, .value = pair.value.str });
        }
    } else if (v == .array) {
        try list.ensureTotalCapacity(arena, v.array.len);
        for (v.array) |item| {
            if (item != .map) return error.InvalidArgument;
            const name = item.mapGet("name") orelse return error.InvalidArgument;
            const val = item.mapGet("value") orelse return error.InvalidArgument;
            if (name != .str or val != .str) return error.InvalidArgument;
            list.appendAssumeCapacity(.{ .name = name.str, .value = val.str });
        }
    } else return error.InvalidArgument;

    return list.toOwnedSlice(arena);
}

fn collectEnvWithAuthority(
    arena: std.mem.Allocator,
    value: ?msgpack.Value,
    authority: transport.AuthorityPlane,
) ![][]const u8 {
    const base = try collectEnv(arena, value);
    var list: std.ArrayList([]const u8) = .empty;
    try list.ensureTotalCapacity(arena, base.len + authorityEnvCount(authority));
    for (base) |entry| list.appendAssumeCapacity(entry);
    try appendAuthorityEnvStrings(arena, &list, authority);
    return list.toOwnedSlice(arena);
}

fn collectAdapterEnvVars(arena: std.mem.Allocator, value: ?msgpack.Value) ![]const adapters.EnvVar {
    const env = try collectEnvVars(arena, value);
    if (env.len == 0) return &.{};

    const out = try arena.alloc(adapters.EnvVar, env.len);
    for (env, 0..) |entry, i| {
        out[i] = .{ .name = entry.name, .value = entry.value };
    }
    return out;
}

fn collectAdapterEnvVarsWithAuthority(
    arena: std.mem.Allocator,
    value: ?msgpack.Value,
    authority: transport.AuthorityPlane,
) ![]const adapters.EnvVar {
    const base = try collectEnvVars(arena, value);
    var list: std.ArrayList(adapters.EnvVar) = .empty;
    try list.ensureTotalCapacity(arena, base.len + authorityEnvCount(authority));
    for (base) |entry| {
        list.appendAssumeCapacity(.{ .name = entry.name, .value = entry.value });
    }
    appendAuthorityAdapterEnv(&list, authority);
    return list.toOwnedSlice(arena);
}

fn authorityEnvCount(authority: transport.AuthorityPlane) usize {
    var count: usize = 0;
    if (authority.socket_path != null) count += 1;
    if (authority.project_id != null) count += 1;
    if (authority.run_id != null) count += 1;
    if (authority.token != null) count += 1;
    return count;
}

fn appendAuthorityEnvStrings(
    arena: std.mem.Allocator,
    list: *std.ArrayList([]const u8),
    authority: transport.AuthorityPlane,
) !void {
    if (authority.socket_path) |socket_path| {
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_DAEMON_URL=unix://{s}", .{socket_path}));
    }
    if (authority.project_id) |project_id| {
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_PROJECT_ID={s}", .{project_id}));
    }
    if (authority.run_id) |run_id| {
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_RUN_ID={s}", .{run_id}));
    }
    if (authority.token) |token| {
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_TOKEN={s}", .{token}));
    }
}

fn appendAuthorityAdapterEnv(
    list: *std.ArrayList(adapters.EnvVar),
    authority: transport.AuthorityPlane,
) void {
    if (authority.socket_path) |socket_path| {
        list.appendAssumeCapacity(.{ .name = "OD_DAEMON_URL", .value = socket_path });
    }
    if (authority.project_id) |project_id| {
        list.appendAssumeCapacity(.{ .name = "OD_PROJECT_ID", .value = project_id });
    }
    if (authority.run_id) |run_id| {
        list.appendAssumeCapacity(.{ .name = "OD_RUN_ID", .value = run_id });
    }
    if (authority.token) |token| {
        list.appendAssumeCapacity(.{ .name = "OD_TOKEN", .value = token });
    }
}

fn stringField(value: msgpack.Value, key: []const u8) ?[]const u8 {
    const field = value.mapGet(key) orelse return null;
    return if (field == .str) field.str else null;
}

fn matchesAnyPattern(text: []const u8, patterns: []const []const u8) bool {
    for (patterns) |pattern| {
        if (pattern.len == 0) continue;
        if (std.mem.indexOf(u8, text, pattern) != null) return true;
    }
    return false;
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

test "redactPatternsAlloc replaces configured exact patterns" {
    const redacted = try redactPatternsAlloc(
        std.testing.allocator,
        "token=secret and secret again",
        &.{"secret"},
    );
    defer redacted.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("token=[REDACTED] and [REDACTED] again", redacted.bytes);

    const unchanged = try redactPatternsAlloc(
        std.testing.allocator,
        "nothing sensitive",
        &.{"secret"},
    );
    defer unchanged.deinit(std.testing.allocator);

    try std.testing.expect(unchanged.owned == null);
    try std.testing.expectEqualStrings("nothing sensitive", unchanged.bytes);
}

test "parseLaunchSpec accepts provider task and transport order" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    const order = try a.alloc(msgpack.Value, 2);
    order[0] = .{ .str = "stdio-json" };
    order[1] = .{ .str = "pty" };

    const fast_rules = try a.alloc(msgpack.Value, 1);
    fast_rules[0] = .{ .str = "Run tests?" };

    const policy_pairs = try a.alloc(msgpack.Value.Pair, 1);
    policy_pairs[0] = .{
        .key = .{ .str = "tier2_fast_path_rules" },
        .value = .{ .array = fast_rules },
    };

    const authority_pairs = try a.alloc(msgpack.Value.Pair, 4);
    authority_pairs[0] = .{ .key = .{ .str = "socket_path" }, .value = .{ .str = "/tmp/authority.sock" } };
    authority_pairs[1] = .{ .key = .{ .str = "project_id" }, .value = .{ .str = "proj" } };
    authority_pairs[2] = .{ .key = .{ .str = "run_id" }, .value = .{ .str = "run" } };
    authority_pairs[3] = .{ .key = .{ .str = "token" }, .value = .{ .str = "tok" } };

    const pairs = try a.alloc(msgpack.Value.Pair, 6);
    pairs[0] = .{ .key = .{ .str = "version" }, .value = .{ .uint = transport.protocol_version } };
    pairs[1] = .{ .key = .{ .str = "provider" }, .value = .{ .str = "fake" } };
    pairs[2] = .{ .key = .{ .str = "task" }, .value = .{ .str = "hello" } };
    pairs[3] = .{ .key = .{ .str = "transport_order" }, .value = .{ .array = order } };
    pairs[4] = .{ .key = .{ .str = "policy" }, .value = .{ .map = policy_pairs } };
    pairs[5] = .{ .key = .{ .str = "authority" }, .value = .{ .map = authority_pairs } };

    const spec = try parseLaunchSpec(a, .{ .map = pairs });

    try std.testing.expectEqualStrings("fake", spec.provider);
    try std.testing.expectEqualStrings("hello", spec.task);
    try std.testing.expectEqual(transport.TransportName.stdio_json, spec.transport_order[0]);
    try std.testing.expectEqual(transport.TransportName.pty, spec.transport_order[1]);
    try std.testing.expectEqualStrings("Run tests?", spec.policy.tier2_fast_path_rules[0]);
    try std.testing.expectEqualStrings("/tmp/authority.sock", spec.authority.socket_path.?);
    try std.testing.expectEqualStrings("tok", spec.authority.token.?);
    try std.testing.expect(matchesAnyPattern("Run tests? [y/N]", spec.policy.tier2_fast_path_rules));
}

test "parseSpecialKey resolves arrow keys" {
    try std.testing.expectEqual(terminal.SpecialKey.up, parseSpecialKey("up").?);
    try std.testing.expectEqual(terminal.SpecialKey.page_down, parseSpecialKey("page_down").?);
    try std.testing.expect(parseSpecialKey("nope") == null);
}
