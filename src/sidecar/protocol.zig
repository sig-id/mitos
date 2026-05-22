//! Wire framing for the mitos-term sidecar protocol.
//!
//! Every frame on the stdin/stdout pipe is a 4-byte big-endian length prefix
//! (compatible with Erlang's `{packet, 4}` port option) followed by a payload.
//! The payload's first byte is a stream tag identifying what the body means:
//!
//!   0x01 RAW       opaque PTY output (sidecar -> Elixir)
//!   0x02 EVENT     MessagePack-encoded event map (sidecar -> Elixir)
//!   0x03 SNAPSHOT  MessagePack-encoded grid snapshot (sidecar -> Elixir)
//!   0x10 CONTROL   MessagePack-encoded command (Elixir -> sidecar)
//!
//! The display path (RAW) is byte-opaque so the BEAM never parses VT — it just
//! forwards. The structured paths (EVENT/SNAPSHOT/CONTROL) carry MessagePack so
//! the brain can reason about them.
const std = @import("std");

pub const Stream = enum(u8) {
    raw = 0x01,
    event = 0x02,
    snapshot = 0x03,
    control = 0x10,
    _,
};

pub const max_frame_bytes: u32 = 16 * 1024 * 1024; // 16 MiB hard cap

pub const ReadError = error{
    Truncated,
    FrameTooLarge,
    EmptyFrame,
    UnknownStream,
    Closed,
    IoError,
    OutOfMemory,
};

pub const WriteError = error{
    IoError,
    FrameTooLarge,
};

pub const Frame = struct {
    stream: Stream,
    body: []u8,

    pub fn deinit(self: Frame, allocator: std.mem.Allocator) void {
        if (self.body.len > 0) allocator.free(self.body);
    }
};

/// Buffered reader over a single fd. Owns no fd lifetime; reads are blocking
/// `std.posix.read` calls returning Closed on EOF.
pub const FrameReader = struct {
    allocator: std.mem.Allocator,
    fd: std.posix.fd_t,

    pub fn init(allocator: std.mem.Allocator, fd: std.posix.fd_t) FrameReader {
        return .{ .allocator = allocator, .fd = fd };
    }

    /// Read exactly one frame; allocates body via `self.allocator`. Caller owns.
    pub fn readFrame(self: *FrameReader) ReadError!Frame {
        var len_buf: [4]u8 = undefined;
        try readExact(self.fd, &len_buf);
        const len = std.mem.readInt(u32, &len_buf, .big);
        if (len == 0) return error.EmptyFrame;
        if (len > max_frame_bytes) return error.FrameTooLarge;

        const total = self.allocator.alloc(u8, len) catch return error.OutOfMemory;
        errdefer self.allocator.free(total);
        try readExact(self.fd, total);

        const tag = total[0];
        const stream: Stream = @enumFromInt(tag);
        switch (stream) {
            .raw, .event, .snapshot, .control => {},
            _ => return error.UnknownStream,
        }

        // Drop the leading stream byte by shifting in place; callers want only
        // the body. Most frames are small (control messages) so this is cheap.
        const body_len = len - 1;
        if (body_len == 0) {
            self.allocator.free(total);
            return .{ .stream = stream, .body = &.{} };
        }
        std.mem.copyForwards(u8, total[0..body_len], total[1..len]);
        const body = self.allocator.realloc(total, body_len) catch total[0..body_len];
        return .{ .stream = stream, .body = body };
    }
};

/// Writer over a single fd. Holds a mutex so multiple threads (PTY pump,
/// event emitter, snapshot responder) can share the stdout pipe safely.
pub const FrameWriter = struct {
    fd: std.posix.fd_t,
    mutex: std.Thread.Mutex = .{},

    pub fn init(fd: std.posix.fd_t) FrameWriter {
        return .{ .fd = fd };
    }

    pub fn writeFrame(self: *FrameWriter, stream: Stream, body: []const u8) WriteError!void {
        const total_u: u64 = @as(u64, body.len) + 1;
        if (total_u > max_frame_bytes) return error.FrameTooLarge;
        var header: [5]u8 = undefined;
        std.mem.writeInt(u32, header[0..4], @intCast(total_u), .big);
        header[4] = @intFromEnum(stream);

        self.mutex.lock();
        defer self.mutex.unlock();
        writeAll(self.fd, &header) catch return error.IoError;
        if (body.len > 0) writeAll(self.fd, body) catch return error.IoError;
    }
};

fn readExact(fd: std.posix.fd_t, buffer: []u8) ReadError!void {
    var offset: usize = 0;
    while (offset < buffer.len) {
        const n = std.posix.read(fd, buffer[offset..]) catch return error.IoError;
        if (n == 0) return error.Closed;
        offset += n;
    }
}

fn writeAll(fd: std.posix.fd_t, bytes: []const u8) !void {
    var offset: usize = 0;
    while (offset < bytes.len) {
        const n = try std.posix.write(fd, bytes[offset..]);
        if (n == 0) return error.WriteFailed;
        offset += n;
    }
}

// -- tests ------------------------------------------------------------------

test "FrameWriter -> FrameReader roundtrip over a pipe" {
    if (@import("builtin").os.tag != .linux) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    var w = FrameWriter.init(fds[1]);
    try w.writeFrame(.raw, "hello");
    try w.writeFrame(.event, &[_]u8{ 0x80, 0xff });

    var r = FrameReader.init(allocator, fds[0]);
    var frame = try r.readFrame();
    defer frame.deinit(allocator);
    try std.testing.expectEqual(Stream.raw, frame.stream);
    try std.testing.expectEqualSlices(u8, "hello", frame.body);

    var frame2 = try r.readFrame();
    defer frame2.deinit(allocator);
    try std.testing.expectEqual(Stream.event, frame2.stream);
    try std.testing.expectEqualSlices(u8, &[_]u8{ 0x80, 0xff }, frame2.body);
}

test "FrameReader rejects unknown stream tag" {
    if (@import("builtin").os.tag != .linux) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    // Length=1, stream=0x42 (unknown).
    var raw: [5]u8 = .{ 0, 0, 0, 1, 0x42 };
    _ = try std.posix.write(fds[1], &raw);
    var r = FrameReader.init(allocator, fds[0]);
    try std.testing.expectError(error.UnknownStream, r.readFrame());
}

test "FrameReader rejects empty frames (no stream byte)" {
    if (@import("builtin").os.tag != .linux) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    const fds = try std.posix.pipe();
    defer std.posix.close(fds[0]);
    defer std.posix.close(fds[1]);

    var raw: [4]u8 = .{ 0, 0, 0, 0 };
    _ = try std.posix.write(fds[1], &raw);
    var r = FrameReader.init(allocator, fds[0]);
    try std.testing.expectError(error.EmptyFrame, r.readFrame());
}
