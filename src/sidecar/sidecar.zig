//! mitos-term: the per-session sidecar.
//!
//! Owns one PTY + one libghostty-vt parser, communicates with the BEAM
//! control plane over stdin/stdout using the framing in `protocol.zig`. The
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
const redaction = @import("../redaction/redactor.zig");
const steer = @import("../steer/root.zig");
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
const max_terminal_cols: u16 = 512;
const max_terminal_rows: u16 = 512;
const max_signal_number: u8 = 64;
const max_shutdown_grace_ms: u32 = 60_000;
const max_authority_id_bytes: usize = 4 * 1024;
const max_authority_origin_bytes: usize = 4 * 1024;
const max_authority_response_bytes: usize = 1024 * 1024;
const max_stdin_bytes: usize = protocol.max_frame_bytes - 1024;
const max_steer_text_bytes: usize = 1024 * 1024;
const max_encode_key_text_bytes: usize = 1024 * 1024;

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
    terminal_observation: adapters.TerminalObservationProfile = adapters.terminal_observation_generic_pty,
    tier2_fast_path_rules: []const []const u8 = &.{},
    redaction_patterns: []const []const u8 = &.{},
    next_producer_request_id: u64 = 1,
    raw_spool_file: ?std.fs.File = null,
    raw_spool_path: ?[]u8 = null,
    raw_spool_offset: u64 = 0,
    raw_ref_filter: PtyRawRefFilter = .{},
    prompt_filter: PromptEventFilter = .{},
    title_filter: TitleEventFilter = .{},
    terminal_output_filter: TerminalOutputFilter = .{},

    // Steering: semantic interactions (approve/deny/answer/prompt/...) are
    // translated to provider-specific key sequences and written to the PTY.
    steering: steer.SteeringAdapter = steer.keymaps.generic,

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
        if (self.raw_spool_path) |path| {
            std.fs.deleteFileAbsolute(path) catch {};
            self.allocator.free(path);
        }
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
        const now = std.time.milliTimestamp();

        var timeout: i64 = -1;
        if (self.idle_pending) {
            timeout = @max(0, self.idle_ms - (now - self.last_output_ms));
        }
        if (timeout < 0) return -1;
        return @intCast(@min(timeout, std.math.maxInt(i32)));
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
        const session = if (self.session) |*s| s else return;
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
            const redacted_bytes = try self.redactForScope(.terminal_text, bytes);
            defer redacted_bytes.deinit(self.allocator);
            self.writer.writeFrame(.raw, redacted_bytes.bytes) catch return error.IoError;
            try self.appendRawSpool(redacted_bytes.bytes);
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
        const emit_raw_ref = self.raw_ref_filter.shouldEmit(self.terminal_observation, bytes);
        var file = self.raw_spool_file.?;
        file.writeAll(bytes) catch |err| {
            self.emitError("raw_spool", @errorName(err)) catch {};
            return error.IoError;
        };
        self.raw_spool_offset += len;

        if (!emit_raw_ref) return;

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
            .mode = 0o600,
        }) catch |err| {
            self.emitError("raw_spool", @errorName(err)) catch {};
            return error.IoError;
        };

        self.raw_spool_file = file;
        self.raw_spool_path = path;
        self.raw_spool_offset = 0;
    }

    fn feedTerminal(self: *State, bytes: []const u8) void {
        const managed_terminal = if (self.terminal) |*t| t else return;
        var core = managed_terminal.asCore();
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
        if (!dec.finished()) {
            try self.emitError("decode", "TrailingBytes");
            return;
        }

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
        if (std.ascii.eqlIgnoreCase(cmd, "steer")) return self.handleSteer(value);
        if (std.mem.eql(u8, cmd, "SNAPSHOT_REQUEST") or std.mem.eql(u8, cmd, "snapshot")) return self.handleSnapshot(value);
        if (std.ascii.eqlIgnoreCase(cmd, "cancel")) return self.handleCancel();
        if (std.mem.eql(u8, cmd, "encode_key")) return self.handleEncodeKey(value, arena);
        if (std.mem.eql(u8, cmd, "shutdown")) {
            const grace_ms = shutdownGraceMilliseconds(value.mapGet("grace_ms"), 100) catch |err| {
                try self.emitError("protocol", @errorName(err));
                return;
            };
            try self.shutdownChild(grace_ms);
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

        const cols = terminalDimension(control.mapGet("cols"), 80, max_terminal_cols) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };
        const rows = terminalDimension(control.mapGet("rows"), 24, max_terminal_rows) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };
        self.idle_ms = idleMilliseconds(control.mapGet("idle_ms"), self.idle_ms) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };

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
            self.terminal_observation = resolved.terminal_observation;
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

            cwd_opt = if (value.mapGet("cwd")) |v| cwd: {
                if (v != .str) break :cwd null;
                validateCString(v.str) catch |err| {
                    try self.emitError("protocol", @errorName(err));
                    return;
                };
                break :cwd v.str;
            } else null;
            self.provider = .{ .value = "generic-pty" };
            self.adapter = .{ .value = "generic-pty.terminal" };
            self.selected_transport = .pty;
            self.terminal_observation = adapters.terminal_observation_generic_pty;
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
        var managed_owned = true;
        errdefer if (managed_owned) managed.deinit();
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
        managed_owned = false;
        self.session = session;
        self.cols = cols;
        self.rows = rows;
        self.last_output_ms = std.time.milliTimestamp();
        self.idle_pending = false;
        self.output_seq = 0;
        self.raw_ref_filter = .{};
        self.prompt_filter = .{};
        self.title_filter = .{};
        self.terminal_output_filter = .{};
        self.steering = steer.forProvider(self.provider);

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
                const session_ref = if (self.session) |*s| s else {
                    try self.emitError("state", "no session");
                    return;
                };

                session_ref.pty.writeAll(input) catch |err| {
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
            const stdout = try self.redactForScope(.provider_event, result.stdout);
            defer stdout.deinit(self.allocator);
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
                .{ "stream", .{ .str = "stdout" } },
                .{ "bytes", .{ .bin = stdout.bytes } },
            });
            var sink_context = SidecarAdapterSink{
                .state = self,
                .skip_token_usage = stdoutHasCanonicalTokenUsage(result.stdout),
            };
            headless.emitNormalizedJsonl(
                self.allocator,
                sink_context.sink(),
                self.provider,
                result.stdout,
            ) catch |err| {
                try self.emitError("codec", @errorName(err));
            };
        }
        if (result.stderr.len > 0) {
            const stderr = try self.redactForScope(.provider_event, result.stderr);
            defer stderr.deinit(self.allocator);
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
                .{ "stream", .{ .str = "stderr" } },
                .{ "bytes", .{ .bin = stderr.bytes } },
            });
        }

        const final_output = headless.extractFinalOutputAlloc(self.allocator, result.stdout) catch |err| {
            try self.emitError("codec", @errorName(err));
            return error.OrderlyShutdown;
        };
        defer self.allocator.free(final_output);
        if (final_output.len > 0) {
            const redacted_final = try self.redactForScope(.provider_event, final_output);
            defer redacted_final.deinit(self.allocator);
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "FINAL_OUTPUT" } },
                .{ "text", .{ .str = redacted_final.bytes } },
            });
            try self.emitPromptsFromText(final_output);
        }

        if (result.status() != .completed) {
            const detail = if (result.stderr.len > 0) result.stderr else "stdio-json provider command failed";
            const redacted_detail = try self.redactForScope(.provider_event, detail);
            defer redacted_detail.deinit(self.allocator);
            try self.emitError("process", redacted_detail.bytes);
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
        if (bytes.len > max_stdin_bytes) {
            try self.emitError("protocol", "write_stdin: bytes too large");
            return;
        }

        const session = if (self.session) |*s| s else {
            try self.emitError("state", "no session");
            return;
        };
        session.pty.writeAll(bytes) catch |err| {
            try self.emitError("pty_write", @errorName(err));
        };
    }

    fn handleResize(self: *State, value: msgpack.Value) LoopError!void {
        const session = if (self.session) |*s| s else {
            try self.emitError("state", "no session");
            return;
        };
        const cols = terminalDimension(value.mapGet("cols"), self.cols, max_terminal_cols) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };
        const rows = terminalDimension(value.mapGet("rows"), self.rows, max_terminal_rows) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };
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
        const session = if (self.session) |*s| s else {
            try self.emitError("state", "no session");
            return;
        };
        const sig = signalNumber(value.mapGet("signum"), 15) catch |err| {
            try self.emitError("protocol", @errorName(err));
            return;
        };
        session.kill(sig) catch |err| {
            try self.emitError("signal", @errorName(err));
        };
    }

    fn handleCancel(self: *State) LoopError!void {
        const session = if (self.session) |*s| s else {
            try self.emitError("state", "no session");
            return;
        };
        session.kill(15) catch |err| {
            try self.emitError("cancel", @errorName(err));
        };
    }

    fn handleAuthorize(self: *State, value: msgpack.Value) LoopError!void {
        const request_id_value = value.mapGet("request_id") orelse value.mapGet("action_id") orelse {
            try self.emitError("protocol", "authorize: missing request_id");
            return;
        };
        if (request_id_value != .str) {
            try self.emitError("protocol", "authorize: request_id must be string");
            return;
        }
        const request_id = request_id_value.str;
        if (controlTextError(request_id, max_authority_id_bytes, true)) |err| {
            try self.emitControlTextError("authorize", "request_id", err);
            return;
        }

        const token_value = value.mapGet("token") orelse {
            try self.emitError("protocol", "authorize: missing token");
            return;
        };
        if (token_value != .str) {
            try self.emitError("protocol", "authorize: token must be string");
            return;
        }
        if (controlTextError(token_value.str, max_authority_response_bytes, false)) |err| {
            try self.emitControlTextError("authorize", "token", err);
            return;
        }

        const origin = if (value.mapGet("origin")) |origin_value| origin: {
            if (origin_value != .str) {
                try self.emitError("protocol", "authorize: origin must be string");
                return;
            }
            if (controlTextError(origin_value.str, max_authority_origin_bytes, false)) |err| {
                try self.emitControlTextError("authorize", "origin", err);
                return;
            }
            break :origin origin_value.str;
        } else "terminal_scrape";

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
            .{ "kind", .{ .str = "authorization" } },
            .{ "request_id", .{ .str = request_id } },
            .{ "action_id", .{ .str = request_id } },
            .{ "origin", .{ .str = origin } },
            .{ "decision", .{ .str = "allow" } },
            .{ "token", .{ .str = "[REDACTED]" } },
        });
    }

    fn handleDeny(self: *State, value: msgpack.Value) LoopError!void {
        const request_id_value = value.mapGet("request_id") orelse value.mapGet("action_id") orelse {
            try self.emitError("protocol", "deny: missing request_id");
            return;
        };
        if (request_id_value != .str) {
            try self.emitError("protocol", "deny: request_id must be string");
            return;
        }
        const request_id = request_id_value.str;
        if (controlTextError(request_id, max_authority_id_bytes, true)) |err| {
            try self.emitControlTextError("deny", "request_id", err);
            return;
        }

        const reason_value = value.mapGet("reason") orelse {
            try self.emitError("protocol", "deny: missing reason");
            return;
        };
        if (reason_value != .str) {
            try self.emitError("protocol", "deny: reason must be string");
            return;
        }
        const reason = reason_value.str;
        if (controlTextError(reason, max_authority_response_bytes, false)) |err| {
            try self.emitControlTextError("deny", "reason", err);
            return;
        }

        const origin = if (value.mapGet("origin")) |origin_value| origin: {
            if (origin_value != .str) {
                try self.emitError("protocol", "deny: origin must be string");
                return;
            }
            if (controlTextError(origin_value.str, max_authority_origin_bytes, false)) |err| {
                try self.emitControlTextError("deny", "origin", err);
                return;
            }
            break :origin origin_value.str;
        } else "terminal_scrape";
        const redacted_reason = try self.redactForScope(.provider_event, reason);
        defer redacted_reason.deinit(self.allocator);

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "STRUCTURED_OUTPUT" } },
            .{ "kind", .{ .str = "authorization" } },
            .{ "request_id", .{ .str = request_id } },
            .{ "action_id", .{ .str = request_id } },
            .{ "origin", .{ .str = origin } },
            .{ "decision", .{ .str = "deny" } },
            .{ "reason", .{ .str = redacted_reason.bytes } },
        });
    }

    /// `steer` realizes a semantic interaction against the agent's TUI:
    /// {cmd: "steer", interaction: "approve"|"deny"|"answer"|"prompt"|
    ///  "interrupt"|"select_option", text?: str, option?: uint}
    /// The provider keymap chosen at `open` decides the byte sequence, so the
    /// brain never has to know which keys a given provider's dialogs expect.
    fn handleSteer(self: *State, value: msgpack.Value) LoopError!void {
        const name_value = value.mapGet("interaction") orelse {
            try self.emitError("protocol", "steer: missing interaction");
            return;
        };
        if (name_value != .str) {
            try self.emitError("protocol", "steer: interaction must be string");
            return;
        }
        const name = name_value.str;
        const requires_text = steerInteractionRequiresText(name);
        const text = if (value.mapGet("text")) |text_v| text: {
            if (text_v != .str) {
                try self.emitError("protocol", "steer: text must be string");
                return;
            }
            if (steerTextError(text_v.str, requires_text)) |detail| {
                try self.emitError("protocol", detail);
                return;
            }
            break :text text_v.str;
        } else text: {
            if (requires_text) {
                try self.emitError("protocol", "steer: text required");
                return;
            }
            break :text "";
        };
        const option: ?u64 = if (value.mapGet("option")) |v| option: {
            const raw = v.asUint() orelse {
                try self.emitError("protocol", "steer: option must be integer");
                return;
            };
            if (raw == 0 or raw > 9) {
                try self.emitError("protocol", "steer: option out of range");
                return;
            }
            break :option raw;
        } else null;

        const interaction = steer.parseInteraction(name, text, option) orelse {
            try self.emitError("steer", "unknown interaction");
            return;
        };
        const session = if (self.session) |*s| s else {
            try self.emitError("state", "no session");
            return;
        };
        const bytes = self.steering.encode(interaction, self.allocator) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {
                try self.emitError("steer", @errorName(err));
                return;
            },
        };
        defer self.allocator.free(bytes);

        session.pty.writeAll(bytes) catch |err| {
            try self.emitError("pty_write", @errorName(err));
            return;
        };

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "STEER_SENT" } },
            .{ "interaction", .{ .str = name } },
            .{ "provider", .{ .str = self.provider.value } },
            .{ "bytes", .{ .uint = bytes.len } },
        });
    }

    fn handleSnapshot(self: *State, value: msgpack.Value) LoopError!void {
        _ = value; // mode/lines reserved for scrollback support; visible-only for v1
        const managed_terminal = if (self.terminal) |*t| t else {
            try self.emitError("state", "no terminal");
            return;
        };
        var core = managed_terminal.asCore();
        const snap = core.snapshot(&core, self.allocator) catch |err| {
            try self.emitError("snapshot", @errorName(err));
            return;
        };
        defer snap.deinit(self.allocator);

        try self.emitSnapshotFrame(snap);
    }

    fn handleEncodeKey(self: *State, value: msgpack.Value, arena: std.mem.Allocator) LoopError!void {
        _ = arena;
        const managed_terminal = if (self.terminal) |*t| t else {
            try self.emitError("state", "no terminal");
            return;
        };
        var core = managed_terminal.asCore();

        const input: terminal.KeyInput = blk: {
            if (value.mapGet("text")) |text_v| {
                if (text_v != .str) {
                    try self.emitError("protocol", "encode_key: text must be string");
                    return;
                }
                if (encodeKeyTextError(text_v.str)) |detail| {
                    try self.emitError("protocol", detail);
                    return;
                }
                break :blk .{ .text = text_v.str };
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
        try self.emitTerminalOutputFromCurrentScreen();
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
        const redacted_title = try self.redactForScope(.terminal_text, title);
        defer redacted_title.deinit(self.allocator);
        if (!self.title_filter.shouldEmit(self.terminal_observation, redacted_title.bytes)) return;

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = "title" } },
            .{ "text", .{ .str = redacted_title.bytes } },
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
        const managed_terminal = if (self.terminal) |*t| t else return;
        var core = managed_terminal.asCore();
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

        try self.emitTerminalOutputFromText(visible);
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
        if (!self.prompt_filter.shouldEmit(event)) return;

        switch (event) {
            .approval_requested => |approval| {
                const redacted_text = try self.redactForScope(.terminal_text, approval.text);
                defer redacted_text.deinit(self.allocator);
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "approval_requested" } },
                    .{ "text", .{ .str = redacted_text.bytes } },
                    .{ "risk", .{ .str = @tagName(approval.risk) } },
                });
                try self.emitActionRequestUnlessFastPath(approval.text, redacted_text.bytes, @tagName(approval.risk));
            },
            .question_asked => |question| {
                const redacted_text = try self.redactForScope(.terminal_text, question.text);
                defer redacted_text.deinit(self.allocator);
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "question_asked" } },
                    .{ "text", .{ .str = redacted_text.bytes } },
                });
            },
            .final_answer_candidate => |text| {
                const redacted_text = try self.redactForScope(.terminal_text, text);
                defer redacted_text.deinit(self.allocator);
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "final_answer_candidate" } },
                    .{ "text", .{ .str = redacted_text.bytes } },
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
                const redacted_text = try self.redactForScope(.terminal_text, text);
                defer redacted_text.deinit(self.allocator);
                try self.emitMap(.event, &.{
                    .{ "type", .{ .str = "PROMPT_DETECTED" } },
                    .{ "prompt_kind", .{ .str = "error_detected" } },
                    .{ "text", .{ .str = redacted_text.bytes } },
                    .{ "risk", .{ .str = "high" } },
                });
            },
        }
    }

    fn emitActionRequestUnlessFastPath(
        self: *State,
        prompt_text: []const u8,
        redacted_prompt_text: []const u8,
        risk: []const u8,
    ) LoopError!void {
        const fast_path = matchesAnyPattern(prompt_text, self.tier2_fast_path_rules);

        var id_buf: [64]u8 = undefined;
        const producer_request_id = std.fmt.bufPrint(
            &id_buf,
            "terminal-scrape-{d}",
            .{self.next_producer_request_id},
        ) catch "terminal-scrape";
        self.next_producer_request_id += 1;

        if (fast_path) {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "ACTION_REQUESTED" } },
                .{ "producer_request_id", .{ .str = producer_request_id } },
                .{ "origin", .{ .str = "terminal_scrape" } },
                .{ "action", .{ .str = "terminal_input" } },
                .{ "risk", .{ .str = risk } },
                .{ "context", .{ .str = redacted_prompt_text } },
                .{ "auto_decision", .{ .str = "authorize" } },
                .{ "decided_by", .{ .str = "policy" } },
                .{ "fast_path", .{ .boolean = true } },
            });
        } else {
            try self.emitMap(.event, &.{
                .{ "type", .{ .str = "ACTION_REQUESTED" } },
                .{ "producer_request_id", .{ .str = producer_request_id } },
                .{ "origin", .{ .str = "terminal_scrape" } },
                .{ "action", .{ .str = "terminal_input" } },
                .{ "risk", .{ .str = risk } },
                .{ "context", .{ .str = redacted_prompt_text } },
            });
        }
    }

    fn emitError(self: *State, kind: []const u8, detail: []const u8) LoopError!void {
        try self.emitMap(.event, &.{
            .{ "type", .{ .str = transport.TypedEventName.error_reported.wireName() } },
            .{ "kind", .{ .str = kind } },
            .{ "detail", .{ .str = detail } },
        });
    }

    fn emitControlTextError(
        self: *State,
        command: []const u8,
        field: []const u8,
        err: ControlTextError,
    ) LoopError!void {
        var detail_buf: [128]u8 = undefined;
        const reason = switch (err) {
            .too_large => "too large",
            .nul => "contains NUL",
            .invalid_utf8 => "must be valid UTF-8",
            .empty => "empty",
        };
        const detail = std.fmt.bufPrint(&detail_buf, "{s}: {s} {s}", .{ command, field, reason }) catch "invalid control text";
        try self.emitError("protocol", detail);
    }

    fn emitTerminalOutputFromCurrentScreen(self: *State) LoopError!void {
        const managed_terminal = if (self.terminal) |*t| t else return;
        var core = managed_terminal.asCore();
        const visible = core.visible_text(&core, self.allocator) catch |err| {
            try self.emitError("terminal_output", @errorName(err));
            return;
        };
        defer self.allocator.free(visible);

        try self.emitTerminalOutputFromText(visible);
    }

    fn emitTerminalOutputFromText(self: *State, rendered_text: []const u8) LoopError!void {
        const output = terminalOutputTextAlloc(self.allocator, self.terminal_observation, rendered_text) catch return error.OutOfMemory;
        defer self.allocator.free(output);
        if (output.len == 0) return;

        const redacted = try self.redactForScope(.terminal_text, output);
        defer redacted.deinit(self.allocator);
        if (!self.terminal_output_filter.shouldEmit(redacted.bytes)) return;

        try self.emitMap(.event, &.{
            .{ "type", .{ .str = transport.TypedEventName.terminal_output.wireName() } },
            .{ "text", .{ .str = redacted.bytes } },
            .{ "provider", .{ .str = self.provider.value } },
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

        if (buf.items.len + 1 > protocol.max_frame_bytes) {
            try self.emitError("snapshot", "FrameTooLarge");
            return;
        }
        self.writer.writeFrame(.snapshot, buf.items) catch return error.IoError;
    }

    fn redactForScope(
        self: *State,
        scope: redaction.RedactionScope,
        bytes: []const u8,
    ) LoopError!BoundaryBytes {
        return redactForScopeAlloc(
            self.allocator,
            scope,
            bytes,
            self.redaction_patterns,
        ) catch return error.OutOfMemory;
    }
};

const SidecarAdapterSink = struct {
    state: *State,
    skip_token_usage: bool = false,

    fn sink(self: *SidecarAdapterSink) adapters.AdapterEventSink {
        return .{ .context = self, .emit = emit };
    }

    fn emit(context: *anyopaque, event: adapters.AdapterEvent) anyerror!void {
        const self: *SidecarAdapterSink = @ptrCast(@alignCast(context));
        switch (event) {
            .structured => |bytes| {
                if (self.skip_token_usage and isNormalizedTokenUsage(bytes)) return;
                const redacted = try self.state.redactForScope(.provider_event, bytes);
                defer redacted.deinit(self.state.allocator);
                try self.state.emitMap(.event, &.{
                    .{ "type", .{ .str = transport.TypedEventName.structured_output.wireName() } },
                    .{ "stream", .{ .str = "normalized" } },
                    .{ "bytes", .{ .bin = redacted.bytes } },
                });
            },
            else => {},
        }
    }
};

// -- helpers ----------------------------------------------------------------

const fnv64_offset: u64 = 0xcbf29ce484222325;
const fnv64_prime: u64 = 0x100000001b3;

fn stdoutHasCanonicalTokenUsage(bytes: []const u8) bool {
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;
        if (isCanonicalTokenUsageJson(line)) return true;
    }
    return false;
}

fn isCanonicalTokenUsageJson(line: []const u8) bool {
    return std.mem.indexOf(u8, line, "\"type\"") != null and
        std.mem.indexOf(u8, line, "\"token_usage\"") != null;
}

fn isNormalizedTokenUsage(bytes: []const u8) bool {
    return isCanonicalTokenUsageJson(bytes);
}

const PtyRawRefFilter = struct {
    last_signature: u64 = 0,
    last_signature_valid: bool = false,

    fn shouldEmit(
        self: *PtyRawRefFilter,
        profile: adapters.TerminalObservationProfile,
        bytes: []const u8,
    ) bool {
        const signature = rawSemanticSignature(profile, bytes) orelse return false;
        if (self.last_signature_valid and self.last_signature == signature) return false;

        self.last_signature = signature;
        self.last_signature_valid = true;
        return true;
    }
};

const PromptEventFilter = struct {
    last_signature: u64 = 0,
    last_signature_valid: bool = false,

    fn shouldEmit(self: *PromptEventFilter, event: prompt.PromptEvent) bool {
        if (event.kind() == .idle_detected) return false;

        const signature = promptEventSignature(event);
        if (self.last_signature_valid and self.last_signature == signature) return false;

        self.last_signature = signature;
        self.last_signature_valid = true;
        return true;
    }
};

const TitleEventFilter = struct {
    last_signature: u64 = 0,
    last_signature_valid: bool = false,

    fn shouldEmit(
        self: *TitleEventFilter,
        profile: adapters.TerminalObservationProfile,
        title: []const u8,
    ) bool {
        if (profile.suppress_title_events) return false;

        const signature = titleSignature(title) orelse return false;
        if (self.last_signature_valid and self.last_signature == signature) return false;

        self.last_signature = signature;
        self.last_signature_valid = true;
        return true;
    }
};

const TerminalOutputFilter = struct {
    last_signature: u64 = 0,
    last_signature_valid: bool = false,

    fn shouldEmit(self: *TerminalOutputFilter, text: []const u8) bool {
        const signature = semanticTextSignature(text) orelse return false;
        if (self.last_signature_valid and self.last_signature == signature) return false;

        self.last_signature = signature;
        self.last_signature_valid = true;
        return true;
    }
};

fn rawSemanticSignature(profile: adapters.TerminalObservationProfile, bytes: []const u8) ?u64 {
    if (!profile.emit_raw_refs) return null;

    var hash = fnv64_offset;
    var semantic_chars: usize = 0;
    var strong_chars: usize = 0;
    var i: usize = 0;

    while (i < bytes.len) {
        const b = bytes[i];
        if (b == 0x1b) {
            i += skipEscape(bytes[i..]);
            continue;
        }
        if (b < 0x20 or b == 0x7f) {
            i += 1;
            continue;
        }
        if (b < 0x80) {
            if (isSemanticAscii(b)) {
                hashByte(&hash, std.ascii.toLower(b));
                semantic_chars += 1;
                if (std.ascii.isAlphanumeric(b)) strong_chars += 1;
            }
            i += 1;
            continue;
        }

        const width = utf8Width(bytes[i..]) orelse {
            i += 1;
            continue;
        };
        const codepoint = std.unicode.utf8Decode(bytes[i .. i + width]) catch {
            i += 1;
            continue;
        };
        if (!isDecorativeCodepoint(codepoint)) {
            hashByte(&hash, 0xff);
            for (bytes[i .. i + width]) |part| hashByte(&hash, part);
            semantic_chars += 1;
            strong_chars += 1;
        }
        i += width;
    }

    const min_strong = profile.raw_min_strong_chars;
    if (strong_chars < min_strong or semantic_chars < min_strong) return null;
    return hash;
}

fn terminalOutputTextAlloc(
    allocator: std.mem.Allocator,
    profile: adapters.TerminalObservationProfile,
    rendered_text: []const u8,
) ![]u8 {
    if (!profile.extract_visible_text) return allocator.dupe(u8, "");

    var fallback: std.ArrayList(u8) = .empty;
    defer fallback.deinit(allocator);
    var bullets: std.ArrayList(u8) = .empty;
    defer bullets.deinit(allocator);

    var lines = std.mem.splitScalar(u8, rendered_text, '\n');
    while (lines.next()) |raw_line| {
        const clean = try cleanTerminalLineAlloc(allocator, raw_line);
        defer allocator.free(clean);
        if (clean.len == 0 or isTerminalChromeLine(profile, clean)) continue;

        if (terminalBulletText(clean)) |bullet_text| {
            const trimmed = stripTrailingOrdinal(std.mem.trim(u8, bullet_text, " \t\r\n"));
            if (trimmed.len > 0 and !isTerminalChromeLine(profile, trimmed)) {
                try appendOutputLine(allocator, &bullets, trimmed);
            }
        }

        try appendOutputLine(allocator, &fallback, clean);
    }

    if (bullets.items.len > 0) return bullets.toOwnedSlice(allocator);
    return fallback.toOwnedSlice(allocator);
}

fn cleanTerminalLineAlloc(allocator: std.mem.Allocator, raw_line: []const u8) ![]u8 {
    const status_index = std.mem.indexOf(u8, raw_line, "›") orelse raw_line.len;
    const line = raw_line[0..status_index];

    var out: std.ArrayList(u8) = .empty;
    defer out.deinit(allocator);
    var pending_space = false;
    var i: usize = 0;

    while (i < line.len) {
        const b = line[i];
        if (b < 0x20 or b == 0x7f) {
            i += 1;
            continue;
        }
        if (b < 0x80) {
            if (std.ascii.isWhitespace(b)) {
                pending_space = out.items.len > 0;
            } else {
                if (pending_space) try out.append(allocator, ' ');
                pending_space = false;
                try out.append(allocator, b);
            }
            i += 1;
            continue;
        }

        const width = utf8Width(line[i..]) orelse {
            i += 1;
            continue;
        };
        const codepoint = std.unicode.utf8Decode(line[i .. i + width]) catch {
            i += 1;
            continue;
        };
        if (isTerminalFrameCodepoint(codepoint)) {
            pending_space = out.items.len > 0;
        } else {
            if (pending_space) try out.append(allocator, ' ');
            pending_space = false;
            try out.appendSlice(allocator, line[i .. i + width]);
        }
        i += width;
    }

    const trimmed = std.mem.trim(u8, out.items, " \t\r\n");
    return try allocator.dupe(u8, trimmed);
}

fn appendOutputLine(allocator: std.mem.Allocator, out: *std.ArrayList(u8), line: []const u8) !void {
    if (out.items.len > 0) try out.append(allocator, '\n');
    try out.appendSlice(allocator, line);
}

fn terminalBulletText(line: []const u8) ?[]const u8 {
    const bullet_index = std.mem.indexOf(u8, line, "•") orelse return null;
    var start = bullet_index + "•".len;
    while (start < line.len and (line[start] == ' ' or line[start] == '\t')) : (start += 1) {}
    if (start >= line.len) return null;
    return line[start..];
}

fn stripTrailingOrdinal(text: []const u8) []const u8 {
    var end = text.len;
    while (end > 0 and std.ascii.isWhitespace(text[end - 1])) : (end -= 1) {}
    var digits_start = end;
    while (digits_start > 0 and std.ascii.isDigit(text[digits_start - 1])) : (digits_start -= 1) {}
    if (digits_start == end) return text[0..end];
    if (digits_start == 0 or !std.ascii.isWhitespace(text[digits_start - 1])) return text[0..end];
    return std.mem.trimRight(u8, text[0 .. digits_start - 1], " \t\r\n");
}

fn isTerminalChromeLine(profile: adapters.TerminalObservationProfile, line: []const u8) bool {
    if (!containsAlnum(line)) return true;
    return containsAny(line, profile.chrome_line_markers);
}

fn containsAlnum(text: []const u8) bool {
    for (text) |byte| {
        if (byte < 0x80 and std.ascii.isAlphanumeric(byte)) return true;
    }
    return false;
}

fn containsAny(text: []const u8, needles: []const []const u8) bool {
    for (needles) |needle| {
        if (std.mem.indexOf(u8, text, needle) != null) return true;
    }
    return false;
}

fn semanticTextSignature(text: []const u8) ?u64 {
    var hash = fnv64_offset;
    var strong_chars: usize = 0;
    for (text) |byte| {
        if (byte < 0x80 and std.ascii.isWhitespace(byte)) {
            hashByte(&hash, ' ');
        } else {
            hashByte(&hash, byte);
            if (byte < 0x80 and std.ascii.isAlphanumeric(byte)) strong_chars += 1;
            if (byte >= 0x80) strong_chars += 1;
        }
    }
    if (strong_chars == 0) return null;
    return hash;
}

fn titleSignature(title: []const u8) ?u64 {
    var hash = fnv64_offset;
    var strong_chars: usize = 0;
    var i: usize = 0;
    var seen_strong = false;

    while (i < title.len) {
        const b = title[i];
        if (b == 0x1b) {
            i += skipEscape(title[i..]);
            continue;
        }
        if (b < 0x20 or b == 0x7f) {
            i += 1;
            continue;
        }
        if (b < 0x80) {
            if (!seen_strong and isLeadingTitleNoise(b)) {
                i += 1;
                continue;
            }
            if (isSemanticAscii(b)) {
                hashByte(&hash, std.ascii.toLower(b));
                if (std.ascii.isAlphanumeric(b)) {
                    strong_chars += 1;
                    seen_strong = true;
                }
            }
            i += 1;
            continue;
        }

        const width = utf8Width(title[i..]) orelse {
            i += 1;
            continue;
        };
        const codepoint = std.unicode.utf8Decode(title[i .. i + width]) catch {
            i += 1;
            continue;
        };
        if (!isDecorativeCodepoint(codepoint)) {
            hashByte(&hash, 0xff);
            for (title[i .. i + width]) |part| hashByte(&hash, part);
            strong_chars += 1;
            seen_strong = true;
        }
        i += width;
    }

    if (strong_chars == 0) return null;
    return hash;
}

fn promptEventSignature(event: prompt.PromptEvent) u64 {
    var hash = fnv64_offset;
    switch (event) {
        .approval_requested => |approval| {
            hashSlice(&hash, "approval_requested");
            hashSlice(&hash, approval.text);
            hashSlice(&hash, @tagName(approval.risk));
        },
        .question_asked => |question| {
            hashSlice(&hash, "question_asked");
            hashSlice(&hash, question.text);
        },
        .final_answer_candidate => |text| {
            hashSlice(&hash, "final_answer_candidate");
            hashSlice(&hash, text);
        },
        .idle_detected => hashSlice(&hash, "idle_detected"),
        .error_detected => |text| {
            hashSlice(&hash, "error_detected");
            hashSlice(&hash, text);
        },
    }
    return hash;
}

fn skipEscape(bytes: []const u8) usize {
    if (bytes.len < 2) return bytes.len;
    switch (bytes[1]) {
        '[' => {
            var i: usize = 2;
            while (i < bytes.len) : (i += 1) {
                if (bytes[i] >= 0x40 and bytes[i] <= 0x7e) return i + 1;
            }
            return bytes.len;
        },
        ']', 'P', '_', '^', 'X' => {
            var i: usize = 2;
            while (i < bytes.len) : (i += 1) {
                if (bytes[i] == 0x07) return i + 1;
                if (bytes[i] == 0x1b and i + 1 < bytes.len and bytes[i + 1] == '\\') return i + 2;
            }
            return bytes.len;
        },
        '(', ')' => return @min(bytes.len, @as(usize, 3)),
        else => return 2,
    }
}

fn isSemanticAscii(b: u8) bool {
    if (std.ascii.isAlphanumeric(b)) return true;
    return switch (b) {
        '@', '/', '.', '_', ':', '#', '$', '%' => true,
        else => false,
    };
}

fn isLeadingTitleNoise(b: u8) bool {
    return switch (b) {
        ' ', '\t', ':', '.', '-', '_', '|', '/', '\\' => true,
        else => false,
    };
}

fn utf8Width(bytes: []const u8) ?usize {
    if (bytes.len == 0) return null;
    const first = bytes[0];
    const width: usize = if ((first & 0b1110_0000) == 0b1100_0000)
        2
    else if ((first & 0b1111_0000) == 0b1110_0000)
        3
    else if ((first & 0b1111_1000) == 0b1111_0000)
        4
    else
        return null;
    if (width > bytes.len) return null;
    return width;
}

fn isDecorativeCodepoint(codepoint: u21) bool {
    return (codepoint >= 0x2500 and codepoint <= 0x257f) or // box drawing
        (codepoint >= 0x2580 and codepoint <= 0x259f) or // block elements
        (codepoint >= 0x2800 and codepoint <= 0x28ff) or // braille spinners
        codepoint == 0x2022 or // bullet
        codepoint == 0x00b7 or // middle dot
        codepoint == 0x25cf or // black circle
        codepoint == 0x25cb; // white circle
}

fn isTerminalFrameCodepoint(codepoint: u21) bool {
    return (codepoint >= 0x2500 and codepoint <= 0x257f) or // box drawing
        (codepoint >= 0x2580 and codepoint <= 0x259f); // block elements
}

fn hashSlice(hash: *u64, bytes: []const u8) void {
    for (bytes) |byte| hashByte(hash, byte);
    hashByte(hash, 0);
}

fn hashByte(hash: *u64, byte: u8) void {
    hash.* ^= byte;
    hash.* *%= fnv64_prime;
}

const BoundaryBytes = struct {
    bytes: []const u8,
    owned: ?[]u8 = null,

    fn deinit(self: BoundaryBytes, allocator: std.mem.Allocator) void {
        if (self.owned) |owned| allocator.free(owned);
    }
};

const test_terminal_chrome_markers = [_][]const u8{
    "StatusChrome",
    "ProgressChrome",
    "Boot server",
    "interrupt",
};
const test_terminal_observation_provider_tui = adapters.terminalObservationProviderTui(&test_terminal_chrome_markers);

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

fn redactForScopeAlloc(
    allocator: std.mem.Allocator,
    scope: redaction.RedactionScope,
    bytes: []const u8,
    patterns: []const []const u8,
) !BoundaryBytes {
    const literal = try redactPatternsAlloc(allocator, bytes, patterns);
    defer literal.deinit(allocator);

    const default_redactor: redaction.Redactor = .{};
    const redacted = try default_redactor.redactAlloc(allocator, scope, literal.bytes);
    return .{ .bytes = redacted, .owned = redacted };
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
        try validateCString(item.str);
        out[i] = item.str;
    }
    return out;
}

fn terminalDimension(value: ?msgpack.Value, default: u16, max: u16) !u16 {
    const v = value orelse return default;
    const raw = v.asUint() orelse return error.InvalidSize;
    if (raw == 0 or raw > max) return error.InvalidSize;
    return @intCast(raw);
}

fn idleMilliseconds(value: ?msgpack.Value, default: i64) !i64 {
    const v = value orelse return default;
    const raw = v.asUint() orelse return error.InvalidIdleMs;
    if (raw > 60_000) return error.InvalidIdleMs;
    return @intCast(raw);
}

fn signalNumber(value: ?msgpack.Value, default: u8) !u8 {
    const v = value orelse return default;
    const raw = v.asUint() orelse return error.InvalidSignal;
    if (raw == 0 or raw > max_signal_number) return error.InvalidSignal;
    return @intCast(raw);
}

fn shutdownGraceMilliseconds(value: ?msgpack.Value, default: u32) !u32 {
    const v = value orelse return default;
    const raw = v.asUint() orelse return error.InvalidShutdownGraceMs;
    if (raw > max_shutdown_grace_ms) return error.InvalidShutdownGraceMs;
    return @intCast(raw);
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
            try validateEnvPair(pair.key.str, pair.value.str);
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
            try validateEnvPair(name.str, val.str);
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
    try validateCString(provider_value.str);
    try validateCString(task_value.str);

    const order_value = value.mapGet("transport_order") orelse value.mapGet("adapter_order");
    const order = try collectTransportOrder(arena, order_value);
    const env = try collectEnvVars(arena, value.mapGet("env"));
    const launch_policy = try parseLaunchPolicy(arena, value.mapGet("policy"));
    const authority = try parseAuthorityPlane(value.mapGet("authority"));
    const argv = if (value.mapGet("argv")) |argv_value| argv: {
        if (argv_value != .array) return error.InvalidArgument;
        break :argv try collectStringArray(arena, argv_value.array);
    } else &.{};

    const cols = try terminalDimension(value.mapGet("cols"), 80, max_terminal_cols);
    const rows = try terminalDimension(value.mapGet("rows"), 24, max_terminal_rows);
    const idle_ms = try idleMilliseconds(value.mapGet("idle_ms"), default_idle_ms);
    const cwd: ?[]const u8 = if (value.mapGet("cwd")) |v| cwd: {
        if (v != .str) break :cwd null;
        try validateCString(v.str);
        break :cwd v.str;
    } else null;

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
            try validateEnvPair(pair.key.str, pair.value.str);
            list.appendAssumeCapacity(.{ .name = pair.key.str, .value = pair.value.str });
        }
    } else if (v == .array) {
        try list.ensureTotalCapacity(arena, v.array.len);
        for (v.array) |item| {
            if (item != .map) return error.InvalidArgument;
            const name = item.mapGet("name") orelse return error.InvalidArgument;
            const val = item.mapGet("value") orelse return error.InvalidArgument;
            if (name != .str or val != .str) return error.InvalidArgument;
            try validateEnvPair(name.str, val.str);
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
    try appendAuthorityAdapterEnv(&list, authority);
    return list.toOwnedSlice(arena);
}

fn validateCString(value: []const u8) !void {
    if (std.mem.indexOfScalar(u8, value, 0) != null) return error.InvalidCString;
}

fn validateEnvPair(name: []const u8, value: []const u8) !void {
    if (name.len == 0) return error.InvalidEnvName;
    if (std.mem.indexOfScalar(u8, name, 0) != null) return error.InvalidEnvName;
    if (std.mem.indexOfScalar(u8, name, '=') != null) return error.InvalidEnvName;
    if (std.mem.indexOfScalar(u8, value, 0) != null) return error.InvalidEnvValue;
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
        try validateCString(socket_path);
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_DAEMON_URL=unix://{s}", .{socket_path}));
    }
    if (authority.project_id) |project_id| {
        try validateCString(project_id);
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_PROJECT_ID={s}", .{project_id}));
    }
    if (authority.run_id) |run_id| {
        try validateCString(run_id);
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_RUN_ID={s}", .{run_id}));
    }
    if (authority.token) |token| {
        try validateCString(token);
        try list.append(arena, try std.fmt.allocPrint(arena, "OD_TOKEN={s}", .{token}));
    }
}

fn appendAuthorityAdapterEnv(
    list: *std.ArrayList(adapters.EnvVar),
    authority: transport.AuthorityPlane,
) !void {
    if (authority.socket_path) |socket_path| {
        try validateCString(socket_path);
        list.appendAssumeCapacity(.{ .name = "OD_DAEMON_URL", .value = socket_path });
    }
    if (authority.project_id) |project_id| {
        try validateCString(project_id);
        list.appendAssumeCapacity(.{ .name = "OD_PROJECT_ID", .value = project_id });
    }
    if (authority.run_id) |run_id| {
        try validateCString(run_id);
        list.appendAssumeCapacity(.{ .name = "OD_RUN_ID", .value = run_id });
    }
    if (authority.token) |token| {
        try validateCString(token);
        list.appendAssumeCapacity(.{ .name = "OD_TOKEN", .value = token });
    }
}

fn stringField(value: msgpack.Value, key: []const u8) ?[]const u8 {
    const field = value.mapGet(key) orelse return null;
    return if (field == .str) field.str else null;
}

fn envValue(env: []const []const u8, key: []const u8) ?[]const u8 {
    for (env) |entry| {
        if (entry.len > key.len and
            entry[key.len] == '=' and
            std.mem.startsWith(u8, entry, key))
        {
            return entry[key.len + 1 ..];
        }
    }
    return null;
}

fn matchesAnyPattern(text: []const u8, patterns: []const []const u8) bool {
    for (patterns) |pattern| {
        if (pattern.len == 0) continue;
        if (std.mem.indexOf(u8, text, pattern) != null) return true;
    }
    return false;
}

fn encodeKeyTextError(text: []const u8) ?[]const u8 {
    const err = controlTextError(text, max_encode_key_text_bytes, false) orelse return null;
    return switch (err) {
        .too_large => "encode_key: text too large",
        .nul => "encode_key: text contains NUL",
        .invalid_utf8 => "encode_key: text must be valid UTF-8",
        .empty => null,
    };
}

fn steerTextError(text: []const u8, require_nonblank: bool) ?[]const u8 {
    const err = controlTextError(text, max_steer_text_bytes, require_nonblank) orelse return null;
    return switch (err) {
        .too_large => "steer: text too large",
        .nul => "steer: text contains NUL",
        .invalid_utf8 => "steer: text must be valid UTF-8",
        .empty => "steer: text empty",
    };
}

const ControlTextError = enum {
    too_large,
    nul,
    invalid_utf8,
    empty,
};

fn controlTextError(text: []const u8, max_bytes: usize, require_nonblank: bool) ?ControlTextError {
    if (text.len > max_bytes) return .too_large;
    if (std.mem.indexOfScalar(u8, text, 0) != null) return .nul;
    if (!validUtf8(text)) return .invalid_utf8;
    if (require_nonblank and std.mem.trim(u8, text, " \t\r\n").len == 0) return .empty;
    return null;
}

fn steerInteractionRequiresText(name: []const u8) bool {
    return std.ascii.eqlIgnoreCase(name, "answer") or std.ascii.eqlIgnoreCase(name, "prompt");
}

fn validUtf8(bytes: []const u8) bool {
    var i: usize = 0;
    while (i < bytes.len) {
        const width = std.unicode.utf8ByteSequenceLength(bytes[i]) catch return false;
        if (i + width > bytes.len) return false;
        _ = std.unicode.utf8Decode(bytes[i .. i + width]) catch return false;
        i += width;
    }
    return true;
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

test "control parser rejects c-string truncation in argv and launch spec" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    const argv = try a.alloc(msgpack.Value, 1);
    argv[0] = .{ .str = "sh\x00ignored" };
    try std.testing.expectError(error.InvalidCString, collectStringArray(a, argv));

    var pairs = try a.alloc(msgpack.Value.Pair, 2);
    pairs[0] = .{ .key = .{ .str = "provider" }, .value = .{ .str = "fake" } };
    pairs[1] = .{ .key = .{ .str = "task" }, .value = .{ .str = "hello\x00ignored" } };
    try std.testing.expectError(error.InvalidCString, parseLaunchSpec(a, .{ .map = pairs }));

    pairs = try a.alloc(msgpack.Value.Pair, 3);
    pairs[0] = .{ .key = .{ .str = "provider" }, .value = .{ .str = "fake" } };
    pairs[1] = .{ .key = .{ .str = "task" }, .value = .{ .str = "hello" } };
    pairs[2] = .{ .key = .{ .str = "cwd" }, .value = .{ .str = "/tmp\x00ignored" } };
    try std.testing.expectError(error.InvalidCString, parseLaunchSpec(a, .{ .map = pairs }));
}

test "control parser rejects invalid environment names and values" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    var pairs = try a.alloc(msgpack.Value.Pair, 1);
    pairs[0] = .{ .key = .{ .str = "BAD=NAME" }, .value = .{ .str = "value" } };
    try std.testing.expectError(error.InvalidEnvName, collectEnv(a, .{ .map = pairs }));
    try std.testing.expectError(error.InvalidEnvName, collectEnvVars(a, .{ .map = pairs }));

    pairs = try a.alloc(msgpack.Value.Pair, 1);
    pairs[0] = .{ .key = .{ .str = "BAD\x00NAME" }, .value = .{ .str = "value" } };
    try std.testing.expectError(error.InvalidEnvName, collectEnv(a, .{ .map = pairs }));
    try std.testing.expectError(error.InvalidEnvName, collectEnvVars(a, .{ .map = pairs }));

    pairs = try a.alloc(msgpack.Value.Pair, 1);
    pairs[0] = .{ .key = .{ .str = "GOOD" }, .value = .{ .str = "value\x00ignored" } };
    try std.testing.expectError(error.InvalidEnvValue, collectEnv(a, .{ .map = pairs }));
    try std.testing.expectError(error.InvalidEnvValue, collectEnvVars(a, .{ .map = pairs }));

    const item_pairs = try a.alloc(msgpack.Value.Pair, 2);
    item_pairs[0] = .{ .key = .{ .str = "name" }, .value = .{ .str = "" } };
    item_pairs[1] = .{ .key = .{ .str = "value" }, .value = .{ .str = "value" } };
    const items = try a.alloc(msgpack.Value, 1);
    items[0] = .{ .map = item_pairs };
    try std.testing.expectError(error.InvalidEnvName, collectEnv(a, .{ .array = items }));
    try std.testing.expectError(error.InvalidEnvName, collectEnvVars(a, .{ .array = items }));
}

test "authority env injection rejects c-string truncation" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    const authority: transport.AuthorityPlane = .{ .socket_path = "/tmp/authority\x00ignored" };
    try std.testing.expectError(error.InvalidCString, collectEnvWithAuthority(a, null, authority));
    try std.testing.expectError(error.InvalidCString, collectAdapterEnvVarsWithAuthority(a, null, authority));
}

test "authorization control events redact tokens and denial secrets" {
    if (builtin.os.tag != .linux) return error.SkipZigTest;

    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    var state = State.init(allocator, -1, fds[1]);
    defer state.deinit();

    var values_arena = std.heap.ArenaAllocator.init(allocator);
    defer values_arena.deinit();
    const a = values_arena.allocator();

    const authorize_pairs = try a.alloc(msgpack.Value.Pair, 4);
    authorize_pairs[0] = .{ .key = .{ .str = "request_id" }, .value = .{ .str = "request-1" } };
    authorize_pairs[1] = .{ .key = .{ .str = "token" }, .value = .{ .str = "plain-secret-token" } };
    authorize_pairs[2] = .{ .key = .{ .str = "origin" }, .value = .{ .str = "terminal_scrape" } };
    authorize_pairs[3] = .{ .key = .{ .str = "cmd" }, .value = .{ .str = "AUTHORIZE" } };

    try state.handleAuthorize(.{ .map = authorize_pairs });

    var reader = protocol.FrameReader.init(allocator, fds[0]);
    var authorize_frame = try reader.readFrame();
    defer authorize_frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, authorize_frame.stream);

    var decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer decode_arena.deinit();
    var authorize_decoder = msgpack.Decoder.init(authorize_frame.body);
    const authorize_event = try authorize_decoder.decode(decode_arena.allocator());
    const token_value = authorize_event.mapGet("token").?;
    try std.testing.expect(token_value == .str);
    try std.testing.expectEqualStrings("[REDACTED]", token_value.str);

    const deny_pairs = try a.alloc(msgpack.Value.Pair, 4);
    deny_pairs[0] = .{ .key = .{ .str = "request_id" }, .value = .{ .str = "request-1" } };
    deny_pairs[1] = .{ .key = .{ .str = "reason" }, .value = .{ .str = "blocked API_KEY=plainsecret" } };
    deny_pairs[2] = .{ .key = .{ .str = "origin" }, .value = .{ .str = "terminal_scrape" } };
    deny_pairs[3] = .{ .key = .{ .str = "cmd" }, .value = .{ .str = "DENY" } };

    try state.handleDeny(.{ .map = deny_pairs });

    var deny_frame = try reader.readFrame();
    defer deny_frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, deny_frame.stream);

    var deny_decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer deny_decode_arena.deinit();
    var deny_decoder = msgpack.Decoder.init(deny_frame.body);
    const deny_event = try deny_decoder.decode(deny_decode_arena.allocator());
    const reason_value = deny_event.mapGet("reason").?;
    try std.testing.expect(reason_value == .str);
    try std.testing.expectEqualStrings("blocked API_KEY=[REDACTED]", reason_value.str);

    const oversized_request_id = try allocator.alloc(u8, max_authority_id_bytes + 1);
    defer allocator.free(oversized_request_id);
    @memset(oversized_request_id, 'r');
    const bad_authorize_pairs = try a.alloc(msgpack.Value.Pair, 2);
    bad_authorize_pairs[0] = .{ .key = .{ .str = "request_id" }, .value = .{ .str = oversized_request_id } };
    bad_authorize_pairs[1] = .{ .key = .{ .str = "token" }, .value = .{ .str = "token" } };

    try state.handleAuthorize(.{ .map = bad_authorize_pairs });

    var bad_authorize_frame = try reader.readFrame();
    defer bad_authorize_frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, bad_authorize_frame.stream);

    var bad_authorize_decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer bad_authorize_decode_arena.deinit();
    var bad_authorize_decoder = msgpack.Decoder.init(bad_authorize_frame.body);
    const bad_authorize_event = try bad_authorize_decoder.decode(bad_authorize_decode_arena.allocator());
    const bad_authorize_detail = bad_authorize_event.mapGet("detail").?;
    try std.testing.expect(bad_authorize_detail == .str);
    try std.testing.expectEqualStrings("authorize: request_id too large", bad_authorize_detail.str);

    const oversized_reason = try allocator.alloc(u8, max_authority_response_bytes + 1);
    defer allocator.free(oversized_reason);
    @memset(oversized_reason, 'd');
    const bad_deny_pairs = try a.alloc(msgpack.Value.Pair, 2);
    bad_deny_pairs[0] = .{ .key = .{ .str = "request_id" }, .value = .{ .str = "request-1" } };
    bad_deny_pairs[1] = .{ .key = .{ .str = "reason" }, .value = .{ .str = oversized_reason } };

    try state.handleDeny(.{ .map = bad_deny_pairs });

    var bad_deny_frame = try reader.readFrame();
    defer bad_deny_frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, bad_deny_frame.stream);

    var bad_deny_decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer bad_deny_decode_arena.deinit();
    var bad_deny_decoder = msgpack.Decoder.init(bad_deny_frame.body);
    const bad_deny_event = try bad_deny_decoder.decode(bad_deny_decode_arena.allocator());
    const bad_deny_detail = bad_deny_event.mapGet("detail").?;
    try std.testing.expect(bad_deny_detail == .str);
    try std.testing.expectEqualStrings("deny: reason too large", bad_deny_detail.str);
}

test "encode_key text emits bounded bytes and rejects unsafe text" {
    if (builtin.os.tag != .linux) return error.SkipZigTest;

    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    var state = State.init(allocator, -1, fds[1]);
    defer state.deinit();
    state.terminal = try ghostty.ManagedTerminal.init(allocator, .{
        .backend = .compat,
        .terminal = .{ .cols = 80, .rows = 24 },
    });

    var values_arena = std.heap.ArenaAllocator.init(allocator);
    defer values_arena.deinit();
    const a = values_arena.allocator();

    var text_pairs = try a.alloc(msgpack.Value.Pair, 1);
    text_pairs[0] = .{ .key = .{ .str = "text" }, .value = .{ .str = "abc" } };

    try state.handleEncodeKey(.{ .map = text_pairs }, a);

    var reader = protocol.FrameReader.init(allocator, fds[0]);
    var encoded_frame = try reader.readFrame();
    defer encoded_frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, encoded_frame.stream);

    var encoded_decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer encoded_decode_arena.deinit();
    var encoded_decoder = msgpack.Decoder.init(encoded_frame.body);
    const encoded_event = try encoded_decoder.decode(encoded_decode_arena.allocator());
    const encoded_type = encoded_event.mapGet("type").?;
    try std.testing.expect(encoded_type == .str);
    try std.testing.expectEqualStrings("key_encoded", encoded_type.str);
    const encoded_bytes = encoded_event.mapGet("bytes").?;
    try std.testing.expect(encoded_bytes == .bin);
    try std.testing.expectEqualStrings("abc", encoded_bytes.bin);

    const oversized = try allocator.alloc(u8, max_encode_key_text_bytes + 1);
    defer allocator.free(oversized);
    @memset(oversized, 'x');
    text_pairs[0] = .{ .key = .{ .str = "text" }, .value = .{ .str = oversized } };

    try state.handleEncodeKey(.{ .map = text_pairs }, a);

    var error_frame = try reader.readFrame();
    defer error_frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, error_frame.stream);

    var error_decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer error_decode_arena.deinit();
    var error_decoder = msgpack.Decoder.init(error_frame.body);
    const error_event = try error_decoder.decode(error_decode_arena.allocator());
    const error_type = error_event.mapGet("type").?;
    try std.testing.expect(error_type == .str);
    try std.testing.expectEqualStrings(transport.TypedEventName.error_reported.wireName(), error_type.str);
    const error_kind = error_event.mapGet("kind").?;
    try std.testing.expect(error_kind == .str);
    try std.testing.expectEqualStrings("protocol", error_kind.str);
    const error_detail = error_event.mapGet("detail").?;
    try std.testing.expect(error_detail == .str);
    try std.testing.expectEqualStrings("encode_key: text too large", error_detail.str);

    try std.testing.expectEqualStrings("encode_key: text contains NUL", encodeKeyTextError("a\x00b").?);
    try std.testing.expectEqualStrings("encode_key: text must be valid UTF-8", encodeKeyTextError("\xff").?);
}

test "steer control validates text before session state" {
    if (builtin.os.tag != .linux) return error.SkipZigTest;

    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    var state = State.init(allocator, -1, fds[1]);
    defer state.deinit();

    var values_arena = std.heap.ArenaAllocator.init(allocator);
    defer values_arena.deinit();
    const a = values_arena.allocator();

    const oversized = try allocator.alloc(u8, max_steer_text_bytes + 1);
    defer allocator.free(oversized);
    @memset(oversized, 'x');

    const pairs = try a.alloc(msgpack.Value.Pair, 2);
    pairs[0] = .{ .key = .{ .str = "interaction" }, .value = .{ .str = "prompt" } };
    pairs[1] = .{ .key = .{ .str = "text" }, .value = .{ .str = oversized } };

    try state.handleSteer(.{ .map = pairs });

    var reader = protocol.FrameReader.init(allocator, fds[0]);
    var frame = try reader.readFrame();
    defer frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, frame.stream);

    var decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer decode_arena.deinit();
    var decoder = msgpack.Decoder.init(frame.body);
    const event = try decoder.decode(decode_arena.allocator());
    const event_type = event.mapGet("type").?;
    try std.testing.expect(event_type == .str);
    try std.testing.expectEqualStrings(transport.TypedEventName.error_reported.wireName(), event_type.str);
    const kind = event.mapGet("kind").?;
    try std.testing.expect(kind == .str);
    try std.testing.expectEqualStrings("protocol", kind.str);
    const detail = event.mapGet("detail").?;
    try std.testing.expect(detail == .str);
    try std.testing.expectEqualStrings("steer: text too large", detail.str);

    try std.testing.expectEqualStrings("steer: text empty", steerTextError("", true).?);
    try std.testing.expectEqualStrings("steer: text contains NUL", steerTextError("a\x00b", true).?);
    try std.testing.expectEqualStrings("steer: text must be valid UTF-8", steerTextError("\xff", true).?);
    try std.testing.expect(steerTextError("", false) == null);
}

test "control frame rejects trailing msgpack values" {
    if (builtin.os.tag != .linux) return error.SkipZigTest;

    const allocator = std.testing.allocator;
    const input_fds = try std.posix.pipe();
    defer std.posix.close(input_fds[0]);
    defer std.posix.close(input_fds[1]);
    const output_fds = try std.posix.pipe();
    defer std.posix.close(output_fds[0]);
    defer std.posix.close(output_fds[1]);

    var control_body: std.ArrayList(u8) = .empty;
    defer control_body.deinit(allocator);
    var writer = msgpack.Writer.init(allocator, &control_body);
    try writer.beginMap(1);
    try writer.writeStr("cmd");
    try writer.writeStr("hello");
    try writer.writeNil();

    var input_writer = protocol.FrameWriter.init(input_fds[1]);
    try input_writer.writeFrame(.control, control_body.items);

    var state = State.init(allocator, input_fds[0], output_fds[1]);
    defer state.deinit();
    try state.handleStdin();

    var reader = protocol.FrameReader.init(allocator, output_fds[0]);
    var frame = try reader.readFrame();
    defer frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, frame.stream);

    var decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer decode_arena.deinit();
    var decoder = msgpack.Decoder.init(frame.body);
    const event = try decoder.decode(decode_arena.allocator());
    const event_type = event.mapGet("type").?;
    try std.testing.expect(event_type == .str);
    try std.testing.expectEqualStrings(transport.TypedEventName.error_reported.wireName(), event_type.str);
    const kind = event.mapGet("kind").?;
    try std.testing.expect(kind == .str);
    try std.testing.expectEqualStrings("decode", kind.str);
    const detail = event.mapGet("detail").?;
    try std.testing.expect(detail == .str);
    try std.testing.expectEqualStrings("TrailingBytes", detail.str);
}

test "write_stdin control enforces byte cap before session state" {
    if (builtin.os.tag != .linux) return error.SkipZigTest;

    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    var state = State.init(allocator, -1, fds[1]);
    defer state.deinit();

    var values_arena = std.heap.ArenaAllocator.init(allocator);
    defer values_arena.deinit();
    const a = values_arena.allocator();

    const oversized = try allocator.alloc(u8, max_stdin_bytes + 1);
    defer allocator.free(oversized);
    @memset(oversized, 'i');

    const pairs = try a.alloc(msgpack.Value.Pair, 1);
    pairs[0] = .{ .key = .{ .str = "bytes" }, .value = .{ .bin = oversized } };

    try state.handleWriteStdin(.{ .map = pairs });

    var reader = protocol.FrameReader.init(allocator, fds[0]);
    var frame = try reader.readFrame();
    defer frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, frame.stream);

    var decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer decode_arena.deinit();
    var decoder = msgpack.Decoder.init(frame.body);
    const event = try decoder.decode(decode_arena.allocator());
    const event_type = event.mapGet("type").?;
    try std.testing.expect(event_type == .str);
    try std.testing.expectEqualStrings(transport.TypedEventName.error_reported.wireName(), event_type.str);
    const kind = event.mapGet("kind").?;
    try std.testing.expect(kind == .str);
    try std.testing.expectEqualStrings("protocol", kind.str);
    const detail = event.mapGet("detail").?;
    try std.testing.expect(detail == .str);
    try std.testing.expectEqualStrings("write_stdin: bytes too large", detail.str);
}

test "oversized snapshot emits protocol error instead of failing frame write" {
    if (builtin.os.tag != .linux) return error.SkipZigTest;

    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    var state = State.init(allocator, -1, fds[1]);
    defer state.deinit();

    const huge_text = try allocator.alloc(u8, @as(usize, protocol.max_frame_bytes));
    @memset(huge_text, 's');
    const cells = try allocator.alloc(terminal.Cell, 1);
    cells[0] = .{
        .x = 0,
        .y = 0,
        .text = huge_text,
    };
    const snap: terminal.ScreenSnapshot = .{
        .cols = 1,
        .rows = 1,
        .cursor = .{ .x = 0, .y = 0 },
        .cells = cells,
        .timestamp_ms = 1,
    };
    defer snap.deinit(allocator);

    try state.emitSnapshotFrame(snap);

    var reader = protocol.FrameReader.init(allocator, fds[0]);
    var frame = try reader.readFrame();
    defer frame.deinit(allocator);
    try std.testing.expectEqual(protocol.Stream.event, frame.stream);

    var decode_arena = std.heap.ArenaAllocator.init(allocator);
    defer decode_arena.deinit();
    var decoder = msgpack.Decoder.init(frame.body);
    const event = try decoder.decode(decode_arena.allocator());
    const event_type = event.mapGet("type").?;
    try std.testing.expect(event_type == .str);
    try std.testing.expectEqualStrings(transport.TypedEventName.error_reported.wireName(), event_type.str);
    const kind = event.mapGet("kind").?;
    try std.testing.expect(kind == .str);
    try std.testing.expectEqualStrings("snapshot", kind.str);
    const detail = event.mapGet("detail").?;
    try std.testing.expect(detail == .str);
    try std.testing.expectEqualStrings("FrameTooLarge", detail.str);
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

test "redactForScopeAlloc applies configured patterns and generic secret masking" {
    const redacted = try redactForScopeAlloc(
        std.testing.allocator,
        .provider_event,
        "literal\nAuthorization: Bearer plain-secret-token\nAPI_KEY=plainsecret\nnormal=value",
        &.{"literal"},
    );
    defer redacted.deinit(std.testing.allocator);

    try std.testing.expect(!std.mem.containsAtLeast(u8, redacted.bytes, 1, "literal"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, redacted.bytes, 1, "plain-secret-token"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, redacted.bytes, 1, "plainsecret"));
    try std.testing.expect(std.mem.containsAtLeast(u8, redacted.bytes, 3, "[REDACTED]"));
    try std.testing.expect(std.mem.containsAtLeast(u8, redacted.bytes, 1, "normal=value"));
}

test "redacted terminal output scrubber covers extracted provider text" {
    const output = try terminalOutputTextAlloc(
        std.testing.allocator,
        test_terminal_observation_provider_tui,
        "MM • API_KEY=plainsecret ghp_abcdefghijklmnopqrstuvwxyz 6 › prompt StatusChrome",
    );
    defer std.testing.allocator.free(output);

    const redacted = try redactForScopeAlloc(
        std.testing.allocator,
        .terminal_text,
        output,
        &.{},
    );
    defer redacted.deinit(std.testing.allocator);

    try std.testing.expect(!std.mem.containsAtLeast(u8, redacted.bytes, 1, "plainsecret"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, redacted.bytes, 1, "ghp_abcdefghijklmnopqrstuvwxyz"));
    try std.testing.expectEqualStrings("API_KEY=[REDACTED] [REDACTED]", redacted.bytes);
}

test "raw spool files are private to the sidecar user" {
    var state = State.init(std.testing.allocator, -1, -1);
    try state.ensureRawSpool();

    const path = try std.testing.allocator.dupe(u8, state.raw_spool_path.?);
    defer std.testing.allocator.free(path);
    defer std.fs.deleteFileAbsolute(path) catch {};
    defer state.deinit();

    const stat = try state.raw_spool_file.?.stat();
    try std.testing.expectEqual(@as(std.fs.File.Mode, 0o600), stat.mode & 0o777);
}

test "raw spool file is removed when sidecar state is deinitialized" {
    var state = State.init(std.testing.allocator, -1, -1);
    try state.ensureRawSpool();

    const path = try std.testing.allocator.dupe(u8, state.raw_spool_path.?);
    defer std.testing.allocator.free(path);

    state.deinit();

    try std.testing.expectError(error.FileNotFound, std.fs.openFileAbsolute(path, .{}));
}

test "pty raw ref filter follows adapter observation profile" {
    const provider_tui = test_terminal_observation_provider_tui;
    const generic = adapters.terminal_observation_generic_pty;
    const redraw_noise =
        "\x1b[39;49m\x1b[K\x1b[2m" ++
        "\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80\xe2\x94\x80" ++
        "\x1b[39m\x1b[49m\x1b[0m\x1b[?25h\x1b[27;3H\x1b[?2026l";
    try std.testing.expect(rawSemanticSignature(provider_tui, redraw_noise) == null);

    var filter: PtyRawRefFilter = .{};
    try std.testing.expect(!filter.shouldEmit(provider_tui, "\x1b[39mhello from codex\n"));
    try std.testing.expect(filter.shouldEmit(generic, "\x1b[39mhello from shell\n"));
    try std.testing.expect(!filter.shouldEmit(generic, "\x1b[31mhello from shell\x1b[0m\r\n"));
    try std.testing.expect(filter.shouldEmit(generic, "\x1b[39mreal new output\n"));
    try std.testing.expect(filter.shouldEmit(generic, "FAKE_FINAL: done\n"));
}

test "prompt event filter suppresses consecutive duplicate prompts" {
    var filter: PromptEventFilter = .{};
    const first: prompt.PromptEvent = .{ .approval_requested = .{
        .text = "Do you want to proceed?",
        .risk = .medium,
    } };
    const same: prompt.PromptEvent = .{ .approval_requested = .{
        .text = "Do you want to proceed?",
        .risk = .medium,
    } };
    const changed: prompt.PromptEvent = .{ .approval_requested = .{
        .text = "Allow this command?",
        .risk = .medium,
    } };
    const idle: prompt.PromptEvent = .idle_detected;

    try std.testing.expect(filter.shouldEmit(first));
    try std.testing.expect(!filter.shouldEmit(same));
    try std.testing.expect(filter.shouldEmit(changed));
    try std.testing.expect(!filter.shouldEmit(idle));
}

test "title event filter follows adapter observation profile" {
    const provider_tui = test_terminal_observation_provider_tui;
    const generic = adapters.terminal_observation_generic_pty;

    var filter: TitleEventFilter = .{};
    try std.testing.expect(!filter.shouldEmit(provider_tui, "mitos"));
    try std.testing.expect(!filter.shouldEmit(provider_tui, ": mitos"));
    try std.testing.expect(filter.shouldEmit(generic, "mitos"));
    try std.testing.expect(!filter.shouldEmit(generic, ": mitos"));
    try std.testing.expect(!filter.shouldEmit(generic, "::. mitos"));
    try std.testing.expect(filter.shouldEmit(generic, "mitos run"));
}

test "terminal output extraction keeps codex answer from rendered status line" {
    const output = try terminalOutputTextAlloc(
        std.testing.allocator,
        test_terminal_observation_provider_tui,
        "MM • Hello. 6 › Explain this codebase StatusChrome",
    );
    defer std.testing.allocator.free(output);

    try std.testing.expectEqualStrings("Hello.", output);
}

test "terminal output extraction suppresses provider progress chrome" {
    const output = try terminalOutputTextAlloc(
        std.testing.allocator,
        test_terminal_observation_provider_tui,
        "• ProgressChrome 5 ProgressChrome\nBoot server: runtime(0s • interrupt)\n",
    );
    defer std.testing.allocator.free(output);

    try std.testing.expectEqualStrings("", output);
}

test "terminal output extraction is disabled for generic raw sessions" {
    const output = try terminalOutputTextAlloc(
        std.testing.allocator,
        adapters.terminal_observation_generic_pty,
        "mitos OK\n",
    );
    defer std.testing.allocator.free(output);

    try std.testing.expectEqualStrings("", output);
}

test "terminal output filter suppresses duplicate semantic text" {
    var filter: TerminalOutputFilter = .{};

    try std.testing.expect(filter.shouldEmit("Hello."));
    try std.testing.expect(!filter.shouldEmit("Hello."));
    try std.testing.expect(filter.shouldEmit("Different."));
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

test "parseLaunchSpec rejects invalid terminal size and idle values" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    var pairs = try a.alloc(msgpack.Value.Pair, 4);
    pairs[0] = .{ .key = .{ .str = "provider" }, .value = .{ .str = "fake" } };
    pairs[1] = .{ .key = .{ .str = "task" }, .value = .{ .str = "hello" } };
    pairs[2] = .{ .key = .{ .str = "cols" }, .value = .{ .uint = max_terminal_cols + 1 } };
    pairs[3] = .{ .key = .{ .str = "rows" }, .value = .{ .uint = 24 } };
    try std.testing.expectError(error.InvalidSize, parseLaunchSpec(a, .{ .map = pairs }));

    pairs = try a.alloc(msgpack.Value.Pair, 4);
    pairs[0] = .{ .key = .{ .str = "provider" }, .value = .{ .str = "fake" } };
    pairs[1] = .{ .key = .{ .str = "task" }, .value = .{ .str = "hello" } };
    pairs[2] = .{ .key = .{ .str = "cols" }, .value = .{ .uint = 80 } };
    pairs[3] = .{ .key = .{ .str = "rows" }, .value = .{ .uint = 0 } };
    try std.testing.expectError(error.InvalidSize, parseLaunchSpec(a, .{ .map = pairs }));

    pairs = try a.alloc(msgpack.Value.Pair, 3);
    pairs[0] = .{ .key = .{ .str = "provider" }, .value = .{ .str = "fake" } };
    pairs[1] = .{ .key = .{ .str = "task" }, .value = .{ .str = "hello" } };
    pairs[2] = .{ .key = .{ .str = "idle_ms" }, .value = .{ .uint = 60_001 } };
    try std.testing.expectError(error.InvalidIdleMs, parseLaunchSpec(a, .{ .map = pairs }));
}

test "signalNumber rejects invalid signal values" {
    try std.testing.expectEqual(@as(u8, 15), try signalNumber(null, 15));
    try std.testing.expectEqual(@as(u8, 9), try signalNumber(.{ .uint = 9 }, 15));
    try std.testing.expectError(error.InvalidSignal, signalNumber(.{ .uint = 0 }, 15));
    try std.testing.expectError(error.InvalidSignal, signalNumber(.{ .uint = max_signal_number + 1 }, 15));
    try std.testing.expectError(error.InvalidSignal, signalNumber(.{ .str = "TERM" }, 15));
}

test "shutdownGraceMilliseconds rejects invalid grace values" {
    try std.testing.expectEqual(@as(u32, 100), try shutdownGraceMilliseconds(null, 100));
    try std.testing.expectEqual(@as(u32, 0), try shutdownGraceMilliseconds(.{ .uint = 0 }, 100));
    try std.testing.expectEqual(@as(u32, max_shutdown_grace_ms), try shutdownGraceMilliseconds(.{ .uint = max_shutdown_grace_ms }, 100));
    try std.testing.expectError(error.InvalidShutdownGraceMs, shutdownGraceMilliseconds(.{ .uint = max_shutdown_grace_ms + 1 }, 100));
    try std.testing.expectError(error.InvalidShutdownGraceMs, shutdownGraceMilliseconds(.{ .str = "now" }, 100));
}

test "envValue finds exact KEY=VALUE entries only" {
    const env = [_][]const u8{ "HOMEBREW=no", "HOME=/home/dev", "CLAUDE_CONFIG_DIR=/cfg" };
    try std.testing.expectEqualStrings("/home/dev", envValue(&env, "HOME").?);
    try std.testing.expectEqualStrings("/cfg", envValue(&env, "CLAUDE_CONFIG_DIR").?);
    try std.testing.expect(envValue(&env, "PATH") == null);
}

test "parseSpecialKey resolves arrow keys" {
    try std.testing.expectEqual(terminal.SpecialKey.up, parseSpecialKey("up").?);
    try std.testing.expectEqual(terminal.SpecialKey.page_down, parseSpecialKey("page_down").?);
    try std.testing.expect(parseSpecialKey("nope") == null);
}
