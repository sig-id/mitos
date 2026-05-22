//! Minimal MessagePack encoder/decoder, scoped to the value subset the sidecar
//! protocol uses: nil, bool, signed/unsigned integers, string, binary, array,
//! map. No float, ext, or timestamp support — the protocol does not need them.
//!
//! References msgpack.org/index.html spec format.
const std = @import("std");

pub const Type = enum {
    nil,
    boolean,
    uint,
    int,
    str,
    bin,
    array,
    map,
};

pub const Value = union(Type) {
    nil,
    boolean: bool,
    uint: u64,
    int: i64,
    str: []const u8,
    bin: []const u8,
    array: []const Value,
    map: []const Pair,

    pub const Pair = struct { key: Value, value: Value };

    /// Free a value tree allocated by `decode` against `arena`.
    /// Caller is expected to use an ArenaAllocator for decoded values and
    /// drop the whole arena; this is provided only for parity with std types.
    pub fn deinit(self: Value, allocator: std.mem.Allocator) void {
        switch (self) {
            .str, .bin => |bytes| if (bytes.len > 0) allocator.free(bytes),
            .array => |items| {
                for (items) |item| item.deinit(allocator);
                if (items.len > 0) allocator.free(items);
            },
            .map => |pairs| {
                for (pairs) |pair| {
                    pair.key.deinit(allocator);
                    pair.value.deinit(allocator);
                }
                if (pairs.len > 0) allocator.free(pairs);
            },
            else => {},
        }
    }

    /// Look up a string-keyed map entry. Returns null on missing key or when
    /// `self` is not a map.
    pub fn mapGet(self: Value, key: []const u8) ?Value {
        if (self != .map) return null;
        for (self.map) |pair| {
            if (pair.key == .str and std.mem.eql(u8, pair.key.str, key)) return pair.value;
        }
        return null;
    }

    pub fn asUint(self: Value) ?u64 {
        return switch (self) {
            .uint => |v| v,
            .int => |v| if (v >= 0) @intCast(v) else null,
            else => null,
        };
    }
};

// -- writer -----------------------------------------------------------------

pub const Writer = struct {
    list: *std.ArrayList(u8),
    allocator: std.mem.Allocator,

    pub fn init(allocator: std.mem.Allocator, list: *std.ArrayList(u8)) Writer {
        return .{ .list = list, .allocator = allocator };
    }

    pub fn writeNil(self: *Writer) !void {
        try self.list.append(self.allocator, 0xc0);
    }

    pub fn writeBool(self: *Writer, b: bool) !void {
        try self.list.append(self.allocator, if (b) 0xc3 else 0xc2);
    }

    pub fn writeUint(self: *Writer, v: u64) !void {
        if (v <= 0x7f) {
            try self.list.append(self.allocator, @intCast(v));
        } else if (v <= std.math.maxInt(u8)) {
            try self.list.appendSlice(self.allocator, &.{ 0xcc, @intCast(v) });
        } else if (v <= std.math.maxInt(u16)) {
            var buf: [3]u8 = .{ 0xcd, 0, 0 };
            std.mem.writeInt(u16, buf[1..3], @intCast(v), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else if (v <= std.math.maxInt(u32)) {
            var buf: [5]u8 = .{ 0xce, 0, 0, 0, 0 };
            std.mem.writeInt(u32, buf[1..5], @intCast(v), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else {
            var buf: [9]u8 = undefined;
            buf[0] = 0xcf;
            std.mem.writeInt(u64, buf[1..9], v, .big);
            try self.list.appendSlice(self.allocator, &buf);
        }
    }

    pub fn writeInt(self: *Writer, v: i64) !void {
        if (v >= 0) return self.writeUint(@intCast(v));
        if (v >= -32) {
            // negative fixint: 111xxxxx where the value is two's complement i6.
            try self.list.append(self.allocator, @bitCast(@as(i8, @intCast(v))));
        } else if (v >= std.math.minInt(i8)) {
            try self.list.appendSlice(self.allocator, &.{ 0xd0, @bitCast(@as(i8, @intCast(v))) });
        } else if (v >= std.math.minInt(i16)) {
            var buf: [3]u8 = .{ 0xd1, 0, 0 };
            std.mem.writeInt(i16, buf[1..3], @intCast(v), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else if (v >= std.math.minInt(i32)) {
            var buf: [5]u8 = .{ 0xd2, 0, 0, 0, 0 };
            std.mem.writeInt(i32, buf[1..5], @intCast(v), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else {
            var buf: [9]u8 = undefined;
            buf[0] = 0xd3;
            std.mem.writeInt(i64, buf[1..9], v, .big);
            try self.list.appendSlice(self.allocator, &buf);
        }
    }

    pub fn writeStr(self: *Writer, s: []const u8) !void {
        if (s.len <= 31) {
            try self.list.append(self.allocator, 0xa0 | @as(u8, @intCast(s.len)));
        } else if (s.len <= std.math.maxInt(u8)) {
            try self.list.appendSlice(self.allocator, &.{ 0xd9, @intCast(s.len) });
        } else if (s.len <= std.math.maxInt(u16)) {
            var buf: [3]u8 = .{ 0xda, 0, 0 };
            std.mem.writeInt(u16, buf[1..3], @intCast(s.len), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else {
            if (s.len > std.math.maxInt(u32)) return error.StringTooLong;
            var buf: [5]u8 = .{ 0xdb, 0, 0, 0, 0 };
            std.mem.writeInt(u32, buf[1..5], @intCast(s.len), .big);
            try self.list.appendSlice(self.allocator, &buf);
        }
        try self.list.appendSlice(self.allocator, s);
    }

    pub fn writeBin(self: *Writer, b: []const u8) !void {
        if (b.len <= std.math.maxInt(u8)) {
            try self.list.appendSlice(self.allocator, &.{ 0xc4, @intCast(b.len) });
        } else if (b.len <= std.math.maxInt(u16)) {
            var buf: [3]u8 = .{ 0xc5, 0, 0 };
            std.mem.writeInt(u16, buf[1..3], @intCast(b.len), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else {
            if (b.len > std.math.maxInt(u32)) return error.BinaryTooLong;
            var buf: [5]u8 = .{ 0xc6, 0, 0, 0, 0 };
            std.mem.writeInt(u32, buf[1..5], @intCast(b.len), .big);
            try self.list.appendSlice(self.allocator, &buf);
        }
        try self.list.appendSlice(self.allocator, b);
    }

    pub fn beginArray(self: *Writer, n: usize) !void {
        if (n <= 15) {
            try self.list.append(self.allocator, 0x90 | @as(u8, @intCast(n)));
        } else if (n <= std.math.maxInt(u16)) {
            var buf: [3]u8 = .{ 0xdc, 0, 0 };
            std.mem.writeInt(u16, buf[1..3], @intCast(n), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else {
            if (n > std.math.maxInt(u32)) return error.ArrayTooLong;
            var buf: [5]u8 = .{ 0xdd, 0, 0, 0, 0 };
            std.mem.writeInt(u32, buf[1..5], @intCast(n), .big);
            try self.list.appendSlice(self.allocator, &buf);
        }
    }

    pub fn beginMap(self: *Writer, n: usize) !void {
        if (n <= 15) {
            try self.list.append(self.allocator, 0x80 | @as(u8, @intCast(n)));
        } else if (n <= std.math.maxInt(u16)) {
            var buf: [3]u8 = .{ 0xde, 0, 0 };
            std.mem.writeInt(u16, buf[1..3], @intCast(n), .big);
            try self.list.appendSlice(self.allocator, &buf);
        } else {
            if (n > std.math.maxInt(u32)) return error.MapTooLong;
            var buf: [5]u8 = .{ 0xdf, 0, 0, 0, 0 };
            std.mem.writeInt(u32, buf[1..5], @intCast(n), .big);
            try self.list.appendSlice(self.allocator, &buf);
        }
    }

    pub fn writeValue(self: *Writer, value: Value) !void {
        switch (value) {
            .nil => try self.writeNil(),
            .boolean => |b| try self.writeBool(b),
            .uint => |v| try self.writeUint(v),
            .int => |v| try self.writeInt(v),
            .str => |s| try self.writeStr(s),
            .bin => |b| try self.writeBin(b),
            .array => |items| {
                try self.beginArray(items.len);
                for (items) |item| try self.writeValue(item);
            },
            .map => |pairs| {
                try self.beginMap(pairs.len);
                for (pairs) |pair| {
                    try self.writeValue(pair.key);
                    try self.writeValue(pair.value);
                }
            },
        }
    }
};

// -- decoder ----------------------------------------------------------------

pub const DecodeError = error{
    Truncated,
    UnsupportedType,
    OutOfMemory,
};

pub const Decoder = struct {
    bytes: []const u8,
    pos: usize = 0,

    pub fn init(bytes: []const u8) Decoder {
        return .{ .bytes = bytes };
    }

    fn need(self: *Decoder, n: usize) DecodeError!void {
        if (self.pos + n > self.bytes.len) return error.Truncated;
    }

    fn readU8(self: *Decoder) DecodeError!u8 {
        try self.need(1);
        const v = self.bytes[self.pos];
        self.pos += 1;
        return v;
    }

    fn readBig(self: *Decoder, comptime T: type) DecodeError!T {
        const n = @sizeOf(T);
        try self.need(n);
        const v = std.mem.readInt(T, self.bytes[self.pos..][0..n], .big);
        self.pos += n;
        return v;
    }

    fn readBytes(self: *Decoder, n: usize) DecodeError![]const u8 {
        try self.need(n);
        const slice = self.bytes[self.pos .. self.pos + n];
        self.pos += n;
        return slice;
    }

    pub fn decode(self: *Decoder, allocator: std.mem.Allocator) DecodeError!Value {
        const head = try self.readU8();

        // positive fixint
        if (head & 0x80 == 0) return .{ .uint = head };
        // negative fixint
        if (head & 0xe0 == 0xe0) {
            const signed: i8 = @bitCast(head);
            return .{ .int = signed };
        }
        // fixmap
        if (head & 0xf0 == 0x80) return try self.decodeMap(allocator, head & 0x0f);
        // fixarray
        if (head & 0xf0 == 0x90) return try self.decodeArray(allocator, head & 0x0f);
        // fixstr
        if (head & 0xe0 == 0xa0) return try self.decodeStr(allocator, head & 0x1f);

        return switch (head) {
            0xc0 => .nil,
            0xc2 => .{ .boolean = false },
            0xc3 => .{ .boolean = true },
            0xc4 => try self.decodeBin(allocator, try self.readU8()),
            0xc5 => try self.decodeBin(allocator, try self.readBig(u16)),
            0xc6 => try self.decodeBin(allocator, try self.readBig(u32)),
            0xcc => .{ .uint = try self.readU8() },
            0xcd => .{ .uint = try self.readBig(u16) },
            0xce => .{ .uint = try self.readBig(u32) },
            0xcf => .{ .uint = try self.readBig(u64) },
            0xd0 => .{ .int = try self.readBig(i8) },
            0xd1 => .{ .int = try self.readBig(i16) },
            0xd2 => .{ .int = try self.readBig(i32) },
            0xd3 => .{ .int = try self.readBig(i64) },
            0xd9 => try self.decodeStr(allocator, try self.readU8()),
            0xda => try self.decodeStr(allocator, try self.readBig(u16)),
            0xdb => try self.decodeStr(allocator, try self.readBig(u32)),
            0xdc => try self.decodeArray(allocator, try self.readBig(u16)),
            0xdd => try self.decodeArray(allocator, try self.readBig(u32)),
            0xde => try self.decodeMap(allocator, try self.readBig(u16)),
            0xdf => try self.decodeMap(allocator, try self.readBig(u32)),
            else => error.UnsupportedType,
        };
    }

    fn decodeStr(self: *Decoder, allocator: std.mem.Allocator, n: usize) DecodeError!Value {
        const src = try self.readBytes(n);
        const owned = allocator.dupe(u8, src) catch return error.OutOfMemory;
        return .{ .str = owned };
    }

    fn decodeBin(self: *Decoder, allocator: std.mem.Allocator, n: usize) DecodeError!Value {
        const src = try self.readBytes(n);
        const owned = allocator.dupe(u8, src) catch return error.OutOfMemory;
        return .{ .bin = owned };
    }

    fn decodeArray(self: *Decoder, allocator: std.mem.Allocator, n: usize) DecodeError!Value {
        if (n == 0) return .{ .array = &.{} };
        const items = allocator.alloc(Value, n) catch return error.OutOfMemory;
        var filled: usize = 0;
        errdefer {
            for (items[0..filled]) |item| item.deinit(allocator);
            allocator.free(items);
        }
        while (filled < n) : (filled += 1) {
            items[filled] = try self.decode(allocator);
        }
        return .{ .array = items };
    }

    fn decodeMap(self: *Decoder, allocator: std.mem.Allocator, n: usize) DecodeError!Value {
        if (n == 0) return .{ .map = &.{} };
        const pairs = allocator.alloc(Value.Pair, n) catch return error.OutOfMemory;
        var filled: usize = 0;
        errdefer {
            for (pairs[0..filled]) |pair| {
                pair.key.deinit(allocator);
                pair.value.deinit(allocator);
            }
            allocator.free(pairs);
        }
        while (filled < n) : (filled += 1) {
            const k = try self.decode(allocator);
            errdefer k.deinit(allocator);
            const v = try self.decode(allocator);
            pairs[filled] = .{ .key = k, .value = v };
        }
        return .{ .map = pairs };
    }
};

// -- tests ------------------------------------------------------------------

test "encode/decode roundtrips integers across width boundaries" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const alloc = arena.allocator();

    var buf: std.ArrayList(u8) = .empty;
    defer buf.deinit(std.testing.allocator);
    var w = Writer.init(std.testing.allocator, &buf);

    const samples = [_]i64{ 0, 1, 127, 128, 255, 256, 65535, 65536, -1, -32, -33, -128, -129, -32768 };
    for (samples) |s| try w.writeInt(s);

    var d = Decoder.init(buf.items);
    for (samples) |expected| {
        const v = try d.decode(alloc);
        if (expected >= 0) {
            try std.testing.expectEqual(@as(u64, @intCast(expected)), v.uint);
        } else {
            try std.testing.expectEqual(expected, v.int);
        }
    }
}

test "encode/decode roundtrips a control map with mixed types" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const alloc = arena.allocator();

    var buf: std.ArrayList(u8) = .empty;
    defer buf.deinit(std.testing.allocator);
    var w = Writer.init(std.testing.allocator, &buf);

    try w.beginMap(3);
    try w.writeStr("cmd");
    try w.writeStr("open");
    try w.writeStr("cols");
    try w.writeUint(80);
    try w.writeStr("argv");
    try w.beginArray(2);
    try w.writeStr("/bin/sh");
    try w.writeStr("-c");

    var d = Decoder.init(buf.items);
    const v = try d.decode(alloc);
    try std.testing.expectEqualStrings("open", v.mapGet("cmd").?.str);
    try std.testing.expectEqual(@as(u64, 80), v.mapGet("cols").?.uint);
    try std.testing.expectEqual(@as(usize, 2), v.mapGet("argv").?.array.len);
    try std.testing.expectEqualStrings("/bin/sh", v.mapGet("argv").?.array[0].str);
}

test "decoder rejects truncated input" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();

    // 0xd2 declares an int32 but no bytes follow.
    var d = Decoder.init(&[_]u8{0xd2});
    try std.testing.expectError(error.Truncated, d.decode(arena.allocator()));
}
