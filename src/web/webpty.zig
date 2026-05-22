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
const ws = @import("ws.zig");

const index_html = @embedFile("index.html");

/// Cap on retained PTY output replayed to clients that connect late.
const scrollback_cap: usize = 256 * 1024;

pub const WebPtyError = error{
    UnsupportedPlatform,
    InvalidAddress,
};

pub const Options = struct {
    argv: []const []const u8,
    cwd: ?[]const u8 = null,
    env: []const adapters.EnvVar = &.{},
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
        self.pty_write_mutex.lock();
        defer self.pty_write_mutex.unlock();
        self.pty_session.pty.resize(.{ .cols = cols, .rows = rows }) catch {};
    }
};

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

    // Translate EnvVar pairs into the "KEY=VALUE" form the spawner expects.
    var env_arena = std.heap.ArenaAllocator.init(allocator);
    defer env_arena.deinit();
    const spawn_env = try env_arena.allocator().alloc([]const u8, options.env.len);
    for (options.env, 0..) |env_var, i| {
        spawn_env[i] = try std.fmt.allocPrint(
            env_arena.allocator(),
            "{s}={s}",
            .{ env_var.name, env_var.value },
        );
    }

    var session = try pty_unix.spawn(allocator, .{
        .argv = options.argv,
        .cwd = options.cwd,
        .size = options.size,
        .env = spawn_env,
    });
    var session_active = true;
    defer if (session_active) {
        _ = session.kill(15) catch {};
        _ = session.wait();
        session.deinit();
    };

    var server = Server{
        .allocator = allocator,
        .net_server = net_server,
        .pty_session = &session,
    };
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
