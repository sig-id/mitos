//! Minimal server-side WebSocket (RFC 6455) helpers: the opening handshake
//! accept-key derivation plus frame encode/decode over a raw POSIX fd.
//!
//! Only the subset webpty needs is implemented: unfragmented text/binary
//! frames, client masking, and the control opcodes (ping/pong/close). Browser
//! clients never fragment the tiny keystroke/resize messages we receive, so
//! continuation frames are intentionally unsupported.
const std = @import("std");

/// Magic GUID appended to the client key before hashing (RFC 6455 §1.3).
pub const handshake_guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";

/// Maximum inbound payload we are willing to buffer for a single frame.
/// Inbound frames are keystrokes and resize messages, so this is generous.
pub const max_payload_len: u64 = 16 * 1024 * 1024;

pub const WsError = error{
    Closed,
    WriteFailed,
    PayloadTooLarge,
};

pub const Opcode = enum(u4) {
    continuation = 0x0,
    text = 0x1,
    binary = 0x2,
    close = 0x8,
    ping = 0x9,
    pong = 0xa,
    _,
};

/// Compute the `Sec-WebSocket-Accept` value for a client key.
/// `out` receives the 28-byte base64 of SHA1(key ++ guid).
pub fn acceptKey(client_key: []const u8, out: *[28]u8) void {
    var hasher = std.crypto.hash.Sha1.init(.{});
    hasher.update(client_key);
    hasher.update(handshake_guid);
    var digest: [std.crypto.hash.Sha1.digest_length]u8 = undefined;
    hasher.final(&digest);
    _ = std.base64.standard.Encoder.encode(out, &digest);
}

/// A decoded inbound WebSocket message. `payload` is owned by the caller and
/// must be freed with the same allocator passed to `readMessage`.
pub const Message = struct {
    opcode: Opcode,
    payload: []u8,

    pub fn deinit(self: Message, allocator: std.mem.Allocator) void {
        if (self.payload.len > 0) allocator.free(self.payload);
    }
};

fn readExact(fd: std.posix.fd_t, buffer: []u8) WsError!void {
    var offset: usize = 0;
    while (offset < buffer.len) {
        const n = std.posix.read(fd, buffer[offset..]) catch return WsError.Closed;
        if (n == 0) return WsError.Closed;
        offset += n;
    }
}

fn writeAll(fd: std.posix.fd_t, bytes: []const u8) WsError!void {
    var offset: usize = 0;
    while (offset < bytes.len) {
        const n = std.posix.write(fd, bytes[offset..]) catch return WsError.WriteFailed;
        if (n == 0) return WsError.WriteFailed;
        offset += n;
    }
}

/// Read one (assumed-unfragmented) WebSocket frame from `fd`, unmasking the
/// payload. Returns `WsError.Closed` on EOF or a transport error.
pub fn readMessage(allocator: std.mem.Allocator, fd: std.posix.fd_t) (WsError || error{OutOfMemory})!Message {
    var header: [2]u8 = undefined;
    try readExact(fd, &header);

    const opcode: Opcode = @enumFromInt(@as(u4, @truncate(header[0] & 0x0f)));
    const masked = (header[1] & 0x80) != 0;
    var len: u64 = header[1] & 0x7f;

    if (len == 126) {
        var ext: [2]u8 = undefined;
        try readExact(fd, &ext);
        len = std.mem.readInt(u16, &ext, .big);
    } else if (len == 127) {
        var ext: [8]u8 = undefined;
        try readExact(fd, &ext);
        len = std.mem.readInt(u64, &ext, .big);
    }
    if (len > max_payload_len) return WsError.PayloadTooLarge;

    var mask: [4]u8 = .{ 0, 0, 0, 0 };
    if (masked) try readExact(fd, &mask);

    const payload = try allocator.alloc(u8, @intCast(len));
    errdefer allocator.free(payload);
    if (payload.len > 0) try readExact(fd, payload);

    if (masked) {
        for (payload, 0..) |*byte, i| byte.* ^= mask[i % 4];
    }

    return .{ .opcode = opcode, .payload = payload };
}

/// Write one unmasked server frame (server frames are never masked).
/// Callers must serialize writes to the same `fd` themselves.
pub fn writeMessage(fd: std.posix.fd_t, opcode: Opcode, payload: []const u8) WsError!void {
    var header: [10]u8 = undefined;
    header[0] = 0x80 | @as(u8, @intFromEnum(opcode)); // FIN + opcode

    var header_len: usize = 2;
    if (payload.len < 126) {
        header[1] = @intCast(payload.len);
    } else if (payload.len <= std.math.maxInt(u16)) {
        header[1] = 126;
        std.mem.writeInt(u16, header[2..4], @intCast(payload.len), .big);
        header_len = 4;
    } else {
        header[1] = 127;
        std.mem.writeInt(u64, header[2..10], payload.len, .big);
        header_len = 10;
    }

    try writeAll(fd, header[0..header_len]);
    if (payload.len > 0) try writeAll(fd, payload);
}

test "accept key matches RFC 6455 example" {
    var out: [28]u8 = undefined;
    acceptKey("dGhlIHNhbXBsZSBub25jZQ==", &out);
    try std.testing.expectEqualStrings("s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", &out);
}

test "writeMessage encodes small frame header" {
    const tmp = try std.fs.cwd().makeOpenPath(".zig-cache/ws-test", .{});
    var dir = tmp;
    defer dir.close();
    const file = try dir.createFile("frame.bin", .{ .read = true });
    defer file.close();
    defer dir.deleteFile("frame.bin") catch {};

    try writeMessage(file.handle, .binary, "hi");
    try file.seekTo(0);
    var buf: [16]u8 = undefined;
    const n = try file.readAll(&buf);
    try std.testing.expectEqual(@as(usize, 4), n);
    try std.testing.expectEqual(@as(u8, 0x82), buf[0]); // FIN + binary
    try std.testing.expectEqual(@as(u8, 2), buf[1]); // unmasked len 2
    try std.testing.expectEqualStrings("hi", buf[2..4]);
}

test "readMessage decodes a masked client frame" {
    const dir = try std.fs.cwd().makeOpenPath(".zig-cache/ws-test", .{});
    var d = dir;
    defer d.close();
    const file = try d.createFile("inbound.bin", .{ .read = true });
    defer file.close();
    defer d.deleteFile("inbound.bin") catch {};

    // FIN+text, masked len 3, mask 01 02 03 04, payload "abc" XOR mask
    const mask = [_]u8{ 0x01, 0x02, 0x03, 0x04 };
    const plain = "abc";
    var masked: [3]u8 = undefined;
    for (plain, 0..) |c, i| masked[i] = c ^ mask[i % 4];
    var frame: [9]u8 = undefined;
    frame[0] = 0x81;
    frame[1] = 0x80 | 3;
    @memcpy(frame[2..6], &mask);
    @memcpy(frame[6..9], &masked);
    try file.writeAll(&frame);
    try file.seekTo(0);

    const msg = try readMessage(std.testing.allocator, file.handle);
    defer msg.deinit(std.testing.allocator);
    try std.testing.expectEqual(Opcode.text, msg.opcode);
    try std.testing.expectEqualStrings("abc", msg.payload);
}
