//! webpty: run an agent inside a PTY and bridge its I/O to a browser terminal
//! over WebSockets. The PTY master is pumped to every connected client, and
//! client keystrokes / resize messages are written back into the PTY.
//!
//! Threading model (all threads joined before `run` returns):
//!   - main thread runs the PTY read loop and broadcasts output to clients;
//!   - one accept thread owns the listening socket and, per connection, either
//!     serves the static page or upgrades to a WebSocket and spawns a client
//!     reader thread;
//!   - each client reader thread reads inbound frames (input/resize) until the
//!     socket closes.
const std = @import("std");
const builtin = @import("builtin");
const adapters = @import("../adapters/types.zig");
const pty_core = @import("../pty/core.zig");
const pty_unix = @import("../pty/unix.zig");
const terminal = @import("../terminal/core.zig");
const ghostty = @import("../terminal/ghostty.zig");
const prompt = @import("../prompt/root.zig");
const ws = @import("ws.zig");

const index_html = @embedFile("index.html");

/// Cap on retained PTY output replayed to clients that connect late.
const scrollback_cap: usize = 256 * 1024;

/// How many scrollback lines feed prompt detection from the libghostty tap.
const detection_scrollback_lines: usize = 1024;

/// Synthetic adapter label attached to prompt events from the webpty tap.
const webpty_adapter_id = adapters.AdapterId{ .value = "webpty" };

pub const WebPtyError = error{
    UnsupportedPlatform,
    InvalidAddress,
};

pub const Options = struct {
    argv: []const []const u8,
    cwd: ?[]const u8 = null,
    env: []const adapters.EnvVar = &.{},
    /// Provider id (e.g. "claude") used to pick prompt-detection rules.
    provider: []const u8 = "",
    addr: []const u8 = "127.0.0.1",
    port: u16 = 7681,
    size: pty_core.PtySize = .{ .cols = 80, .rows = 24 },
};

const Client = struct {
    fd: std.posix.fd_t,
    write_mutex: std.Thread.Mutex = .{},
    alive: bool = true,

    fn send(self: *Client, opcode: ws.Opcode, payload: []const u8) void {
        self.write_mutex.lock();
        defer self.write_mutex.unlock();
        if (!self.alive) return;
        ws.writeMessage(self.fd, opcode, payload) catch {
            self.alive = false;
        };
    }
};

const Server = struct {
    allocator: std.mem.Allocator,
    net_server: std.net.Server,

    pty_session: *pty_unix.UnixPtySession,
    pty_write_mutex: std.Thread.Mutex = .{},

    // Server-side libghostty-vt tap: PTY output is fed here in addition to being
    // streamed verbatim to the browser, so webpty can derive the parsed screen
    // and run prompt detection without a second emulator on the client.
    terminal_core: terminal.TerminalCore,
    terminal_mutex: std.Thread.Mutex = .{},
    provider_id: adapters.ProviderId,
    last_prompt: ?[]u8 = null,

    clients: std.ArrayList(*Client) = .empty,
    client_threads: std.ArrayList(std.Thread) = .empty,
    registry_mutex: std.Thread.Mutex = .{},

    scrollback: std.ArrayList(u8) = .empty,
    scrollback_mutex: std.Thread.Mutex = .{},

    running: std.atomic.Value(bool) = .init(true),

    fn broadcast(self: *Server, bytes: []const u8) void {
        self.registry_mutex.lock();
        defer self.registry_mutex.unlock();
        for (self.clients.items) |client| {
            if (client.alive) client.send(.binary, bytes);
        }
    }

    /// Broadcast a structured (JSON) event to clients as a text frame. Raw PTY
    /// output uses binary frames, so the client distinguishes the two by type.
    fn broadcastText(self: *Server, json: []const u8) void {
        self.registry_mutex.lock();
        defer self.registry_mutex.unlock();
        for (self.clients.items) |client| {
            if (client.alive) client.send(.text, json);
        }
    }

    /// Feed a chunk of PTY output into the libghostty-vt tap, write any terminal
    /// responses (e.g. cursor-position reports) back to the PTY, and broadcast
    /// newly detected prompts to clients. Best-effort: emulation/detection
    /// failures are swallowed so they never disrupt the byte stream the browser
    /// renders.
    fn feedTerminal(self: *Server, bytes: []const u8) void {
        const allocator = self.allocator;
        var pty_response: std.ArrayList(u8) = .empty;
        defer pty_response.deinit(allocator);
        var frames: std.ArrayList([]u8) = .empty;
        defer frames.deinit(allocator);

        // All terminal-core access is serialized: write_from_pty/snapshot run on
        // the read-loop thread while resize runs on client threads. The PTY and
        // client locks are taken only after releasing this one to avoid any
        // lock-ordering inversion with resizePty.
        self.terminal_mutex.lock();
        feed: {
            const effects = self.terminal_core.write_from_pty(&self.terminal_core, bytes, allocator) catch break :feed;
            defer effects.deinit(allocator);
            for (effects.items) |effect| switch (effect) {
                .write_to_pty => |reply| pty_response.appendSlice(allocator, reply) catch {},
                else => {},
            };

            const visible = self.terminal_core.visible_text(&self.terminal_core, allocator) catch break :feed;
            defer allocator.free(visible);
            const scrollback = self.terminal_core.scrollback_text(&self.terminal_core, allocator, detection_scrollback_lines) catch break :feed;
            defer allocator.free(scrollback);

            const events = prompt.providers.detect(.{
                .visible_text = visible,
                .scrollback_text = scrollback,
            }, .{
                .provider = self.provider_id,
                .adapter = webpty_adapter_id,
                .timestamp_ms = std.time.milliTimestamp(),
            }, allocator) catch break :feed;
            defer prompt.freeEvents(allocator, events);

            // Event texts borrow `visible`/`scrollback`, so serialize to JSON
            // while those buffers are still alive.
            for (events) |event| {
                const json = renderPromptJson(allocator, event) catch continue;
                // Suppress consecutive identical prompts (a static prompt screen
                // would otherwise re-fire on every redraw).
                if (self.last_prompt) |last| {
                    if (std.mem.eql(u8, last, json)) {
                        allocator.free(json);
                        continue;
                    }
                }
                if (self.last_prompt) |last| allocator.free(last);
                self.last_prompt = allocator.dupe(u8, json) catch null;
                frames.append(allocator, json) catch allocator.free(json);
            }
        }
        self.terminal_mutex.unlock();

        if (pty_response.items.len > 0) self.writeToPty(pty_response.items);
        for (frames.items) |json| {
            self.broadcastText(json);
            allocator.free(json);
        }
    }

    fn appendScrollback(self: *Server, bytes: []const u8) void {
        self.scrollback_mutex.lock();
        defer self.scrollback_mutex.unlock();
        self.scrollback.appendSlice(self.allocator, bytes) catch return;
        if (self.scrollback.items.len > scrollback_cap) {
            const drop = self.scrollback.items.len - scrollback_cap;
            std.mem.copyForwards(
                u8,
                self.scrollback.items,
                self.scrollback.items[drop..],
            );
            self.scrollback.shrinkRetainingCapacity(scrollback_cap);
        }
    }

    fn writeToPty(self: *Server, bytes: []const u8) void {
        self.pty_write_mutex.lock();
        defer self.pty_write_mutex.unlock();
        _ = self.pty_session.pty.write(bytes) catch {};
    }

    fn resizePty(self: *Server, cols: u16, rows: u16) void {
        if (cols == 0 or rows == 0) return;
        {
            self.pty_write_mutex.lock();
            defer self.pty_write_mutex.unlock();
            self.pty_session.pty.resize(.{ .cols = cols, .rows = rows }) catch {};
        }
        // Keep the libghostty-vt tap's grid in sync with the PTY window. Taken
        // separately from the PTY lock so feedTerminal (terminal lock -> nothing)
        // and resize (pty lock; terminal lock) never nest in opposite orders.
        self.terminal_mutex.lock();
        defer self.terminal_mutex.unlock();
        self.terminal_core.resize(&self.terminal_core, cols, rows, default_cell_size) catch {};
    }
};

const default_cell_size = terminal.CellSize{ .width_px = 8, .height_px = 16 };

/// Serialize a detected prompt to the JSON event the browser consumes:
/// `{"type":"prompt","kind":...,"risk":...,"text":...}`.
fn renderPromptJson(allocator: std.mem.Allocator, event: prompt.PromptEvent) ![]u8 {
    const runtime_event = prompt.toRuntimeEvent(event).prompt_detected;
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try json.objectField("type");
    try json.write("prompt");
    try json.objectField("kind");
    try json.write(runtime_event.prompt_kind);
    try json.objectField("risk");
    try json.write(runtime_event.risk);
    try json.objectField("text");
    try json.write(runtime_event.text);
    try json.endObject();
    return out.toOwnedSlice();
}

/// Run the webpty server until the agent process exits. Returns the child
/// exit code (0 on clean exit). Blocks the calling thread.
pub fn run(allocator: std.mem.Allocator, options: Options, stdout: anytype) !u8 {
    if (builtin.os.tag != .linux) return WebPtyError.UnsupportedPlatform;

    const address = std.net.Address.parseIp(options.addr, options.port) catch
        return WebPtyError.InvalidAddress;

    var net_server = address.listen(.{ .reuse_address = true }) catch |err| {
        return err;
    };
    errdefer net_server.deinit();

    // Inherit the parent environment (PATH/SHELL/HOME/TERM/...) and overlay the
    // explicit env vars, mirroring the headless path. Without the inherited
    // environment the child is spawned with an empty env and providers like
    // claude fail immediately ("No suitable shell found").
    var env_arena = std.heap.ArenaAllocator.init(allocator);
    defer env_arena.deinit();
    const env_alloc = env_arena.allocator();
    var env_map = try std.process.getEnvMap(env_alloc);
    // Force a known-good terminal profile for the browser path. xterm.js is a
    // full 256-color / truecolor parser, but agents inspect TERM/COLORTERM to
    // decide whether to emit ANSI color; the parent may have TERM=dumb (e.g.
    // mitos run from systemd) or a host-specific terminfo (xterm-ghostty,
    // tmux-256color) the child binary doesn't ship. Override unconditionally
    // so output rendered in the browser is always colored. Caller-supplied
    // options.env still wins (applied below).
    try env_map.put("TERM", "xterm-256color");
    try env_map.put("COLORTERM", "truecolor");
    for (options.env) |env_var| {
        try env_map.put(env_var.name, env_var.value);
    }
    var spawn_env = try std.ArrayList([]const u8).initCapacity(env_alloc, env_map.count());
    var env_it = env_map.iterator();
    while (env_it.next()) |entry| {
        spawn_env.appendAssumeCapacity(try std.fmt.allocPrint(
            env_alloc,
            "{s}={s}",
            .{ entry.key_ptr.*, entry.value_ptr.* },
        ));
    }

    var session = try pty_unix.spawn(allocator, .{
        .argv = options.argv,
        .cwd = options.cwd,
        .size = options.size,
        .env = spawn_env.items,
    });
    var session_active = true;
    defer if (session_active) {
        _ = session.kill(15) catch {};
        _ = session.wait();
        session.deinit();
    };

    // The libghostty-vt tap that PTY output is mirrored into (see Server.feedTerminal).
    var managed = try ghostty.ManagedTerminal.init(allocator, .{
        .terminal = .{ .cols = options.size.cols, .rows = options.size.rows },
    });
    defer managed.deinit();

    var server = Server{
        .allocator = allocator,
        .net_server = net_server,
        .pty_session = &session,
        .terminal_core = managed.asCore(),
        .provider_id = .{ .value = options.provider },
    };
    defer if (server.last_prompt) |last| allocator.free(last);
    defer {
        server.clients.deinit(allocator);
        server.client_threads.deinit(allocator);
        server.scrollback.deinit(allocator);
    }

    const bound = server.net_server.listen_address;
    try stdout.print(
        "webpty listening on http://{f}/  (open in a browser; agent pid {d})\n",
        .{ bound, session.pid },
    );
    try stdout.flush();

    const accept_thread = try std.Thread.spawn(.{}, acceptLoop, .{&server});

    // Main PTY read/broadcast loop.
    var pty = session.asPty();
    var buffer: [8192]u8 = undefined;
    var term: ?pty_core.ProcessTerm = null;
    while (server.running.load(.acquire)) {
        const read_len = pty.read(&pty, &buffer) catch |err| switch (err) {
            error.WouldBlock => {
                if (term == null) term = pollTerm(session.pid);
                if (term != null) break;
                std.Thread.sleep(8 * std.time.ns_per_ms);
                continue;
            },
            error.Closed => {
                if (term == null) term = pollTerm(session.pid);
                break;
            },
            else => break,
        };
        if (read_len == 0) {
            if (term == null) term = pollTerm(session.pid);
            if (term != null) break;
            continue;
        }
        const bytes = buffer[0..read_len];
        server.appendScrollback(bytes);
        server.broadcast(bytes);
        server.feedTerminal(bytes);
    }

    // Shut down: stop accepting, close the listener to unblock accept(), then
    // close every client socket so reader threads return, and join them all.
    server.running.store(false, .release);
    server.net_server.deinit();
    accept_thread.join();

    server.registry_mutex.lock();
    for (server.clients.items) |client| {
        client.alive = false;
        std.posix.shutdown(client.fd, .both) catch {};
    }
    const threads = server.client_threads.items;
    server.registry_mutex.unlock();
    for (threads) |t| t.join();
    server.registry_mutex.lock();
    for (server.clients.items) |client| allocator.destroy(client);
    server.clients.clearRetainingCapacity();
    server.registry_mutex.unlock();

    const final_term = term orelse session.wait();
    session.waited = true;
    session.pty.close();
    session_active = false;
    session.deinit();

    return switch (final_term) {
        .exited => |code| code,
        else => 1,
    };
}

fn acceptLoop(server: *Server) void {
    while (server.running.load(.acquire)) {
        const conn = server.net_server.accept() catch return;
        handleConnection(server, conn.stream) catch {
            conn.stream.close();
        };
    }
}

/// Read the HTTP request head and either serve the page or upgrade to a
/// WebSocket. On a successful upgrade the connection's fd is handed off to a
/// client reader thread and is not closed here.
fn handleConnection(server: *Server, stream: std.net.Stream) !void {
    const fd = stream.handle;
    var head_buf: [8192]u8 = undefined;
    const head = readHttpHead(fd, &head_buf) catch {
        stream.close();
        return;
    };

    const ws_key = findHeader(head, "sec-websocket-key");
    const wants_upgrade = ws_key != null and
        asciiContainsIgnoreCase(findHeader(head, "upgrade") orelse "", "websocket");

    if (wants_upgrade) {
        try completeUpgrade(fd, ws_key.?);
        try registerClient(server, fd);
        return;
    }

    serveStatic(fd, head) catch {};
    stream.close();
}

fn registerClient(server: *Server, fd: std.posix.fd_t) !void {
    const client = try server.allocator.create(Client);
    client.* = .{ .fd = fd };

    server.registry_mutex.lock();
    defer server.registry_mutex.unlock();

    // Replay retained output so a fresh tab shows current screen state.
    server.scrollback_mutex.lock();
    if (server.scrollback.items.len > 0) {
        client.send(.binary, server.scrollback.items);
    }
    server.scrollback_mutex.unlock();

    const thread = std.Thread.spawn(.{}, clientLoop, .{ server, client }) catch {
        server.allocator.destroy(client);
        return;
    };
    try server.clients.append(server.allocator, client);
    try server.client_threads.append(server.allocator, thread);
}

fn clientLoop(server: *Server, client: *Client) void {
    while (server.running.load(.acquire) and client.alive) {
        const msg = ws.readMessage(server.allocator, client.fd) catch {
            client.alive = false;
            return;
        };
        defer msg.deinit(server.allocator);

        switch (msg.opcode) {
            .close => {
                client.alive = false;
                return;
            },
            .ping => client.send(.pong, msg.payload),
            .text, .binary => handleClientMessage(server, msg.payload),
            else => {},
        }
    }
}

fn handleClientMessage(server: *Server, payload: []const u8) void {
    if (payload.len == 0) return;
    switch (payload[0]) {
        '0' => server.writeToPty(payload[1..]),
        '1' => {
            const parsed = std.json.parseFromSlice(
                ResizeMsg,
                server.allocator,
                payload[1..],
                .{ .ignore_unknown_fields = true },
            ) catch return;
            defer parsed.deinit();
            server.resizePty(parsed.value.cols, parsed.value.rows);
        },
        else => {},
    }
}

const ResizeMsg = struct { cols: u16 = 0, rows: u16 = 0 };

fn pollTerm(pid: std.posix.pid_t) ?pty_core.ProcessTerm {
    const result = std.posix.waitpid(pid, std.os.linux.W.NOHANG);
    if (result.pid == 0) return null;
    const status = result.status;
    if (std.posix.W.IFEXITED(status)) return .{ .exited = std.posix.W.EXITSTATUS(status) };
    if (std.posix.W.IFSIGNALED(status)) return .{ .signaled = std.posix.W.TERMSIG(status) };
    if (std.posix.W.IFSTOPPED(status)) return .{ .stopped = std.posix.W.STOPSIG(status) };
    return .{ .unknown = status };
}

// --- minimal HTTP plumbing -------------------------------------------------

fn readHttpHead(fd: std.posix.fd_t, buffer: []u8) ![]const u8 {
    var len: usize = 0;
    while (len < buffer.len) {
        const n = try std.posix.read(fd, buffer[len..]);
        if (n == 0) return error.Closed;
        len += n;
        if (std.mem.indexOf(u8, buffer[0..len], "\r\n\r\n")) |end| {
            return buffer[0 .. end + 4];
        }
    }
    return error.HeadTooLarge;
}

/// Return the trimmed value of the first matching header (case-insensitive).
fn findHeader(head: []const u8, name_lower: []const u8) ?[]const u8 {
    var lines = std.mem.splitSequence(u8, head, "\r\n");
    _ = lines.next(); // request line
    while (lines.next()) |line| {
        const colon = std.mem.indexOfScalar(u8, line, ':') orelse continue;
        const key = std.mem.trim(u8, line[0..colon], " \t");
        if (key.len != name_lower.len) continue;
        if (!std.ascii.eqlIgnoreCase(key, name_lower)) continue;
        return std.mem.trim(u8, line[colon + 1 ..], " \t");
    }
    return null;
}

fn asciiContainsIgnoreCase(haystack: []const u8, needle: []const u8) bool {
    if (needle.len == 0) return true;
    if (haystack.len < needle.len) return false;
    var i: usize = 0;
    while (i + needle.len <= haystack.len) : (i += 1) {
        if (std.ascii.eqlIgnoreCase(haystack[i .. i + needle.len], needle)) return true;
    }
    return false;
}

fn completeUpgrade(fd: std.posix.fd_t, key: []const u8) !void {
    var accept: [28]u8 = undefined;
    ws.acceptKey(key, &accept);
    var buf: [256]u8 = undefined;
    const response = try std.fmt.bufPrint(
        &buf,
        "HTTP/1.1 101 Switching Protocols\r\n" ++
            "Upgrade: websocket\r\n" ++
            "Connection: Upgrade\r\n" ++
            "Sec-WebSocket-Accept: {s}\r\n\r\n",
        .{accept},
    );
    try writeAll(fd, response);
}

fn serveStatic(fd: std.posix.fd_t, head: []const u8) !void {
    const path = requestPath(head);
    const is_root = std.mem.eql(u8, path, "/") or std.mem.eql(u8, path, "/index.html");

    if (!is_root) {
        const not_found = "HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n";
        try writeAll(fd, not_found);
        return;
    }

    var header_buf: [160]u8 = undefined;
    const header = try std.fmt.bufPrint(
        &header_buf,
        "HTTP/1.1 200 OK\r\n" ++
            "Content-Type: text/html; charset=utf-8\r\n" ++
            "Content-Length: {d}\r\n" ++
            "Connection: close\r\n\r\n",
        .{index_html.len},
    );
    try writeAll(fd, header);
    try writeAll(fd, index_html);
}

fn requestPath(head: []const u8) []const u8 {
    const line_end = std.mem.indexOf(u8, head, "\r\n") orelse head.len;
    const line = head[0..line_end];
    var parts = std.mem.tokenizeScalar(u8, line, ' ');
    _ = parts.next(); // method
    return parts.next() orelse "/";
}

fn writeAll(fd: std.posix.fd_t, bytes: []const u8) !void {
    var offset: usize = 0;
    while (offset < bytes.len) {
        const n = try std.posix.write(fd, bytes[offset..]);
        if (n == 0) return error.WriteFailed;
        offset += n;
    }
}

test "findHeader is case-insensitive and trims" {
    const head =
        "GET /ws HTTP/1.1\r\n" ++
        "Host: localhost\r\n" ++
        "Upgrade: websocket\r\n" ++
        "Sec-WebSocket-Key:  dGhlIHNhbXBsZSBub25jZQ==  \r\n\r\n";
    try std.testing.expectEqualStrings("websocket", findHeader(head, "upgrade").?);
    try std.testing.expectEqualStrings("dGhlIHNhbXBsZSBub25jZQ==", findHeader(head, "sec-websocket-key").?);
    try std.testing.expect(findHeader(head, "missing") == null);
}

test "requestPath extracts the target" {
    const head = "GET /index.html HTTP/1.1\r\nHost: x\r\n\r\n";
    try std.testing.expectEqualStrings("/index.html", requestPath(head));
}

test "asciiContainsIgnoreCase finds token in connection header" {
    try std.testing.expect(asciiContainsIgnoreCase("keep-alive, Upgrade", "upgrade"));
    try std.testing.expect(!asciiContainsIgnoreCase("keep-alive", "upgrade"));
}

test "renderPromptJson serializes a detected prompt for the browser" {
    const json = try renderPromptJson(std.testing.allocator, .{ .approval_requested = .{
        .text = "Do you want to proceed?",
        .risk = .medium,
    } });
    defer std.testing.allocator.free(json);

    try std.testing.expect(std.mem.indexOf(u8, json, "\"type\":\"prompt\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, json, "\"kind\":\"approval_requested\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, json, "\"risk\":\"medium\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, json, "Do you want to proceed?") != null);
}

fn readExactTest(fd: std.posix.fd_t, buffer: []u8) !void {
    var offset: usize = 0;
    while (offset < buffer.len) {
        const n = try std.posix.read(fd, buffer[offset..]);
        if (n == 0) return error.Closed;
        offset += n;
    }
}

test "feedTerminal renders PTY bytes through libghostty-vt and streams prompts as text frames" {
    const allocator = std.testing.allocator;

    var managed = try ghostty.ManagedTerminal.init(allocator, .{ .terminal = .{ .cols = 80, .rows = 24 } });
    defer managed.deinit();

    // A connected client backed by a pipe so we can read what the server
    // actually puts on the wire (fds[1] = write end the client writes to,
    // fds[0] = read end the test drains).
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);
    var client = Client{ .fd = fds[1] };

    // pty_session/net_server are unused on this path: plain-text input produces
    // no write_to_pty effects, so the PTY is never touched.
    var server = Server{
        .allocator = allocator,
        .net_server = undefined,
        .pty_session = undefined,
        .terminal_core = managed.asCore(),
        .provider_id = .{ .value = "claude" },
    };
    defer if (server.last_prompt) |last| allocator.free(last);
    try server.clients.append(allocator, &client);
    defer server.clients.deinit(allocator);

    // Feed a claude approval marker as if it had come off the PTY.
    server.feedTerminal("Do you want to proceed?\n");

    // The server should have emitted exactly one text (JSON) frame describing
    // the detected prompt — unmasked, payload < 126 bytes.
    var header: [2]u8 = undefined;
    try readExactTest(fds[0], &header);
    const opcode: u4 = @truncate(header[0] & 0x0f);
    const len = header[1] & 0x7f;
    const payload = try allocator.alloc(u8, len);
    defer allocator.free(payload);
    try readExactTest(fds[0], payload);

    try std.testing.expectEqual(ws.Opcode.text, @as(ws.Opcode, @enumFromInt(opcode)));
    try std.testing.expect(std.mem.indexOf(u8, payload, "\"type\":\"prompt\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, payload, "approval_requested") != null);
}
