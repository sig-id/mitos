const std = @import("std");
const adapters = @import("../adapters/types.zig");
const prompt = @import("../prompt/root.zig");
const pty_core = @import("../pty/core.zig");
const pty_unix = @import("../pty/unix.zig");
const terminal = @import("../terminal/core.zig");
const transport = @import("../transport/root.zig");

pub const provider_id = adapters.ProviderId{ .value = "generic-pty" };
pub const adapter_id = adapters.AdapterId{ .value = "generic-pty.terminal" };

pub const PtyAdapterError = error{
    MissingTerminalCore,
    ReadLoopExceeded,
    UnsupportedPlatform,
};

const supported_transports = [_]transport.TransportName{.pty};

pub const PtyCommand = struct {
    argv: []const []const u8,
    cwd: ?[]const u8 = null,
    size: pty_core.PtySize = .{ .cols = 80, .rows = 24 },
    max_idle_polls_after_exit: usize = 10,
    max_total_polls: usize = 10_000,
    env: []const adapters.EnvVar = &.{},
};

pub fn initAdapter() adapters.AgentAdapter {
    return .{
        .id = adapter_id,
        .provider = provider_id,
        .capabilities = .{
            .supports_interactive_pty = true,
            .supports_streaming_events = true,
        },
        .detect = detect,
        .start = start,
    };
}

fn detect(
    _: *adapters.AgentAdapter,
    _: std.mem.Allocator,
) anyerror!adapters.AdapterDetection {
    return .{
        .available = true,
        .auth_status = .not_required,
        .supported_transports = &supported_transports,
    };
}

fn start(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    task: adapters.Task,
) anyerror!adapters.AdapterRun {
    const core = context.terminal_core orelse return PtyAdapterError.MissingTerminalCore;

    if (context.event_sink) |sink| {
        try sink.emit(sink.context, .{ .terminal_bytes = task.text });
    }

    const effects = try core.write_from_pty(core, task.text, context.allocator);
    effects.deinit(context.allocator);
    try detectPrompts(self, context, core);

    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .transport_name = .pty,
        .status = .completed,
        .started_at_ms = context.started_at_ms,
    };
}

pub fn runCommand(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    command: PtyCommand,
) anyerror!adapters.AdapterRun {
    const core = context.terminal_core orelse return PtyAdapterError.MissingTerminalCore;

    if (@import("builtin").os.tag != .linux) return PtyAdapterError.UnsupportedPlatform;

    var env_arena = std.heap.ArenaAllocator.init(context.allocator);
    defer env_arena.deinit();
    const arena = env_arena.allocator();

    // Inherit the parent environment (PATH/SHELL/HOME/TERM/...) and overlay the
    // explicit --env vars on top, mirroring the headless path. Without this the
    // child process is spawned with an empty environment and providers like
    // claude fail immediately ("No suitable shell found").
    var env_map = try std.process.getEnvMap(arena);
    for (command.env) |env_var| {
        try env_map.put(env_var.name, env_var.value);
    }
    var spawn_env = try std.ArrayList([]const u8).initCapacity(arena, env_map.count());
    var env_it = env_map.iterator();
    while (env_it.next()) |entry| {
        spawn_env.appendAssumeCapacity(try std.fmt.allocPrint(
            arena,
            "{s}={s}",
            .{ entry.key_ptr.*, entry.value_ptr.* },
        ));
    }

    var session = try pty_unix.spawn(context.allocator, .{
        .argv = command.argv,
        .cwd = command.cwd,
        .size = command.size,
        .env = spawn_env.items,
    });
    var session_active = true;
    defer if (session_active) {
        _ = session.kill(15) catch {};
        _ = session.wait();
        session.deinit();
    };

    if (context.event_sink) |sink| {
        try sink.emit(sink.context, .{ .process_spawned = .{
            .argv = command.argv,
            .cwd = command.cwd orelse context.workspace_host_path,
            .pid = @intCast(session.pid),
        } });
    }

    var pty = session.asPty();
    var buffer: [4096]u8 = undefined;
    var term: ?pty_core.ProcessTerm = null;
    var idle_after_exit: usize = 0;
    var total_polls: usize = 0;

    while (total_polls < command.max_total_polls) : (total_polls += 1) {
        const read_len = pty.read(&pty, &buffer) catch |err| switch (err) {
            error.WouldBlock => {
                if (term == null) term = pollProcessTerm(session.pid);
                if (term != null) {
                    idle_after_exit += 1;
                    if (idle_after_exit >= command.max_idle_polls_after_exit) break;
                }
                std.Thread.sleep(10 * std.time.ns_per_ms);
                continue;
            },
            error.Closed => {
                if (term == null) term = pollProcessTerm(session.pid);
                break;
            },
            else => return err,
        };

        if (read_len == 0) {
            if (term == null) term = pollProcessTerm(session.pid);
            if (term != null) break;
            continue;
        }

        const bytes = buffer[0..read_len];
        if (context.event_sink) |sink| {
            try sink.emit(sink.context, .{ .terminal_bytes = bytes });
        }
        const effects = try core.write_from_pty(core, bytes, context.allocator);
        effects.deinit(context.allocator);
        try detectPrompts(self, context, core);

        // Check for pending approval response to send to PTY
        if (context.get_pending_response) |get_resp| {
            if (context.response_context) |resp_ctx| {
                if (get_resp(resp_ctx)) |response| {
                    // Send the response to the PTY
                    const encoded = try core.encode_paste(core, response, context.allocator);
                    defer context.allocator.free(encoded);
                    const written = try pty.write(&pty, encoded);
                    _ = written;

                    // If user requested quit, terminate the session
                    if (std.mem.startsWith(u8, response, "q") or std.mem.startsWith(u8, response, "Q")) {
                        break;
                    }
                }
            }
        }
    } else {
        return PtyAdapterError.ReadLoopExceeded;
    }

    const final_term = term orelse session.wait();
    session.waited = true;
    session.pty.close();
    session_active = false;
    session.deinit();

    if (context.event_sink) |sink| {
        try sink.emit(sink.context, .{ .process_exited = processExited(final_term) });
    }

    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .transport_name = .pty,
        .status = switch (final_term) {
            .exited => |code| if (code == 0) .completed else .failed,
            else => .failed,
        },
        .process_id = null,
        .started_at_ms = context.started_at_ms,
    };
}

fn detectPrompts(
    self: *adapters.AgentAdapter,
    context: adapters.AdapterContext,
    core: *terminal.TerminalCore,
) !void {
    const sink = context.event_sink orelse return;

    const visible = try core.visible_text(core, context.allocator);
    defer context.allocator.free(visible);
    const scrollback = try core.scrollback_text(core, context.allocator, 4096);
    defer context.allocator.free(scrollback);

    const snapshot_json = try renderSnapshotJson(context.allocator, visible);
    defer context.allocator.free(snapshot_json);
    try sink.emit(sink.context, .{ .terminal_snapshot = snapshot_json });

    const events = try prompt.providers.detect(.{
        .visible_text = visible,
        .scrollback_text = scrollback,
    }, .{
        .provider = self.provider,
        .adapter = self.id,
        .timestamp_ms = context.started_at_ms,
    }, context.allocator);
    defer prompt.freeEvents(context.allocator, events);

    for (events) |event| {
        const runtime_event = prompt.toRuntimeEvent(event);
        try sink.emit(sink.context, .{ .prompt_detected = runtime_event.prompt_detected });
    }
}

fn renderSnapshotJson(allocator: std.mem.Allocator, visible_text: []const u8) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try json.objectField("visible_text");
    try json.write(visible_text);
    try json.endObject();
    try out.writer.writeByte('\n');
    return out.toOwnedSlice();
}

fn pollProcessTerm(pid: std.posix.pid_t) ?pty_core.ProcessTerm {
    const result = std.posix.waitpid(pid, std.os.linux.W.NOHANG);
    if (result.pid == 0) return null;
    return processTermFromStatus(result.status);
}

fn processTermFromStatus(status: u32) pty_core.ProcessTerm {
    if (std.posix.W.IFEXITED(status)) return .{ .exited = std.posix.W.EXITSTATUS(status) };
    if (std.posix.W.IFSIGNALED(status)) return .{ .signaled = std.posix.W.TERMSIG(status) };
    if (std.posix.W.IFSTOPPED(status)) return .{ .stopped = std.posix.W.STOPSIG(status) };
    return .{ .unknown = status };
}

fn processExited(term: pty_core.ProcessTerm) @import("../runtime/types.zig").ProcessExited {
    return switch (term) {
        .exited => |code| .{ .exit_code = code },
        .signaled => |signal| .{ .signal = @intCast(@min(signal, std.math.maxInt(u8))) },
        .stopped => |signal| .{ .signal = @intCast(@min(signal, std.math.maxInt(u8))) },
        .unknown => .{},
    };
}

const TestTerminal = struct {
    wrote: bool = false,
    text: std.ArrayList(u8) = .empty,

    fn deinit(self: *TestTerminal, allocator: std.mem.Allocator) void {
        self.text.deinit(allocator);
    }
};

fn testWriteFromPty(
    core: *terminal.TerminalCore,
    bytes: []const u8,
    allocator: std.mem.Allocator,
) terminal.TerminalError!terminal.TerminalEffects {
    const self: *TestTerminal = @ptrCast(@alignCast(core.context));
    self.wrote = true;
    self.text.appendSlice(allocator, bytes) catch return error.OutOfMemory;
    return .{};
}

fn testResize(
    _: *terminal.TerminalCore,
    _: u16,
    _: u16,
    _: terminal.CellSize,
) terminal.TerminalError!void {}

fn testSnapshot(
    _: *terminal.TerminalCore,
    _: std.mem.Allocator,
) terminal.TerminalError!terminal.ScreenSnapshot {
    return error.BackendFailure;
}

fn testVisibleText(
    core: *terminal.TerminalCore,
    allocator: std.mem.Allocator,
) terminal.TerminalError![]u8 {
    const self: *TestTerminal = @ptrCast(@alignCast(core.context));
    return allocator.dupe(u8, self.text.items) catch return error.OutOfMemory;
}

fn testScrollbackText(
    core: *terminal.TerminalCore,
    allocator: std.mem.Allocator,
    _: usize,
) terminal.TerminalError![]u8 {
    const self: *TestTerminal = @ptrCast(@alignCast(core.context));
    return allocator.dupe(u8, self.text.items) catch return error.OutOfMemory;
}

fn testEncodeKey(
    _: *terminal.TerminalCore,
    _: terminal.KeyInput,
    allocator: std.mem.Allocator,
) terminal.TerminalError![]u8 {
    return allocator.dupe(u8, "") catch return error.OutOfMemory;
}

fn testEncodePaste(
    _: *terminal.TerminalCore,
    bytes: []const u8,
    allocator: std.mem.Allocator,
) terminal.TerminalError![]u8 {
    return allocator.dupe(u8, bytes) catch return error.OutOfMemory;
}

test "pty adapter feeds bytes into terminal core without injecting input" {
    var adapter = initAdapter();
    var terminal_state: TestTerminal = .{};
    defer terminal_state.deinit(std.testing.allocator);

    var core: terminal.TerminalCore = .{
        .context = &terminal_state,
        .write_from_pty = testWriteFromPty,
        .resize = testResize,
        .snapshot = testSnapshot,
        .visible_text = testVisibleText,
        .scrollback_text = testScrollbackText,
        .encode_key = testEncodeKey,
        .encode_paste = testEncodePaste,
    };

    const run_id = try @import("../runtime/types.zig").RunId.init("run-pty");
    const run = try adapter.start(&adapter, .{
        .allocator = std.testing.allocator,
        .run_id = run_id,
        .workspace_host_path = ".",
        .terminal_core = &core,
    }, .{ .text = "Run tests? [y/N]" });

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, run.status);
    try std.testing.expect(terminal_state.wrote);
    try std.testing.expectEqualStrings("Run tests? [y/N]", terminal_state.text.items);
}

test "pty adapter emits semantic prompt events from terminal text" {
    var adapter = initAdapter();
    var terminal_state: TestTerminal = .{};
    defer terminal_state.deinit(std.testing.allocator);

    var core: terminal.TerminalCore = .{
        .context = &terminal_state,
        .write_from_pty = testWriteFromPty,
        .resize = testResize,
        .snapshot = testSnapshot,
        .visible_text = testVisibleText,
        .scrollback_text = testScrollbackText,
        .encode_key = testEncodeKey,
        .encode_paste = testEncodePaste,
    };
    var recorder: PromptRecorder = .{};

    const run_id = try @import("../runtime/types.zig").RunId.init("run-pty-prompt");
    _ = try adapter.start(&adapter, .{
        .allocator = std.testing.allocator,
        .run_id = run_id,
        .workspace_host_path = ".",
        .terminal_core = &core,
        .event_sink = .{
            .context = &recorder,
            .emit = recordPrompt,
        },
    }, .{ .text = "Run npm install? [y/N]" });

    try std.testing.expect(recorder.saw_terminal_bytes);
    try std.testing.expect(recorder.saw_snapshot);
    try std.testing.expect(recorder.saw_prompt);
}

test "pty runner spawns a command and observes output through terminal core" {
    var adapter = initAdapter();
    var terminal_state: TestTerminal = .{};
    defer terminal_state.deinit(std.testing.allocator);

    var core: terminal.TerminalCore = .{
        .context = &terminal_state,
        .write_from_pty = testWriteFromPty,
        .resize = testResize,
        .snapshot = testSnapshot,
        .visible_text = testVisibleText,
        .scrollback_text = testScrollbackText,
        .encode_key = testEncodeKey,
        .encode_paste = testEncodePaste,
    };
    var recorder: PromptRecorder = .{};
    const argv = [_][]const u8{
        "/bin/sh",
        "-c",
        "printf 'Run npm install? [y/N]\\n'",
    };

    const run_id = try @import("../runtime/types.zig").RunId.init("run-pty-command");
    const run = try runCommand(&adapter, .{
        .allocator = std.testing.allocator,
        .run_id = run_id,
        .workspace_host_path = ".",
        .terminal_core = &core,
        .event_sink = .{
            .context = &recorder,
            .emit = recordPrompt,
        },
    }, .{
        .argv = &argv,
        .cwd = ".",
        .max_idle_polls_after_exit = 2,
    });

    try std.testing.expectEqual(adapters.AdapterRunStatus.completed, run.status);
    try std.testing.expect(terminal_state.wrote);
    try std.testing.expect(std.mem.indexOf(u8, terminal_state.text.items, "Run npm install?") != null);
    try std.testing.expect(recorder.saw_process_spawned);
    try std.testing.expect(recorder.saw_process_exited);
    try std.testing.expectEqual(@as(?u8, 0), recorder.exit_code);
    try std.testing.expect(recorder.saw_snapshot);
    try std.testing.expect(recorder.saw_prompt);
}

test "pty runner reports real process failure status" {
    var adapter = initAdapter();
    var terminal_state: TestTerminal = .{};
    defer terminal_state.deinit(std.testing.allocator);

    var core: terminal.TerminalCore = .{
        .context = &terminal_state,
        .write_from_pty = testWriteFromPty,
        .resize = testResize,
        .snapshot = testSnapshot,
        .visible_text = testVisibleText,
        .scrollback_text = testScrollbackText,
        .encode_key = testEncodeKey,
        .encode_paste = testEncodePaste,
    };
    var recorder: PromptRecorder = .{};
    const argv = [_][]const u8{
        "/bin/sh",
        "-c",
        "printf 'provider failed\\n'; exit 7",
    };

    const run_id = try @import("../runtime/types.zig").RunId.init("run-pty-fail");
    const run = try runCommand(&adapter, .{
        .allocator = std.testing.allocator,
        .run_id = run_id,
        .workspace_host_path = ".",
        .terminal_core = &core,
        .event_sink = .{
            .context = &recorder,
            .emit = recordPrompt,
        },
    }, .{
        .argv = &argv,
        .cwd = ".",
        .max_idle_polls_after_exit = 2,
    });

    try std.testing.expectEqual(adapters.AdapterRunStatus.failed, run.status);
    try std.testing.expect(std.mem.indexOf(u8, terminal_state.text.items, "provider failed") != null);
    try std.testing.expect(recorder.saw_process_spawned);
    try std.testing.expect(recorder.saw_process_exited);
    try std.testing.expectEqual(@as(?u8, 7), recorder.exit_code);
}

const PromptRecorder = struct {
    exit_code: ?u8 = null,
    saw_process_spawned: bool = false,
    saw_process_exited: bool = false,
    saw_terminal_bytes: bool = false,
    saw_snapshot: bool = false,
    saw_prompt: bool = false,
};

fn recordPrompt(context: *anyopaque, event: adapters.AdapterEvent) anyerror!void {
    const recorder: *PromptRecorder = @ptrCast(@alignCast(context));
    switch (event) {
        .process_spawned => recorder.saw_process_spawned = true,
        .process_exited => |process| {
            recorder.saw_process_exited = true;
            recorder.exit_code = process.exit_code;
        },
        .terminal_bytes => recorder.saw_terminal_bytes = true,
        .terminal_snapshot => recorder.saw_snapshot = true,
        .prompt_detected => recorder.saw_prompt = true,
        else => {},
    }
}
