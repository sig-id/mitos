const std = @import("std");
const builtin = @import("builtin");
const core = @import("core.zig");

const win = std.os.windows;

pub const WindowsConPty = struct {
    conpty: ?win32.HPCON = null,
    input_write: ?win.HANDLE = null,
    output_read: ?win.HANDLE = null,
    size: core.PtySize,

    pub fn init(size: core.PtySize) core.PtyError!WindowsConPty {
        try size.validate();
        if (builtin.os.tag != .windows) return error.UnsupportedPlatform;
        return try initWindows(size);
    }

    pub fn asPty(self: *WindowsConPty) core.Pty {
        return .{
            .context = self,
            .read = readAdapter,
            .write = writeAdapter,
            .resize = resizeAdapter,
            .close = closeAdapter,
        };
    }

    pub fn read(self: *WindowsConPty, buffer: []u8) core.PtyError!usize {
        if (buffer.len == 0) return 0;
        const handle = self.output_read orelse return error.Closed;

        if (builtin.os.tag == .windows) {
            var available: win.DWORD = 0;
            if (win32.PeekNamedPipe(handle, null, 0, null, &available, null) == 0) {
                return switch (win.GetLastError()) {
                    .BROKEN_PIPE, .INVALID_HANDLE => error.Closed,
                    else => error.ReadFailed,
                };
            }
            if (available == 0) return error.WouldBlock;

            const limited = buffer[0..@min(buffer.len, available)];
            return win.ReadFile(handle, limited, null) catch |err| switch (err) {
                error.BrokenPipe, error.NotOpenForReading => error.Closed,
                else => error.ReadFailed,
            };
        }

        return error.UnsupportedPlatform;
    }

    pub fn write(self: *WindowsConPty, bytes: []const u8) core.PtyError!usize {
        if (bytes.len == 0) return 0;
        const handle = self.input_write orelse return error.Closed;

        if (builtin.os.tag == .windows) {
            return win.WriteFile(handle, bytes, null) catch |err| switch (err) {
                error.BrokenPipe, error.NotOpenForWriting => error.Closed,
                else => error.WriteFailed,
            };
        }

        return error.UnsupportedPlatform;
    }

    pub fn resize(self: *WindowsConPty, size: core.PtySize) core.PtyError!void {
        try size.validate();
        const conpty = self.conpty orelse return error.Closed;

        if (builtin.os.tag == .windows) {
            if (failed(win32.ResizePseudoConsole(conpty, try coordFromSize(size)))) {
                return error.ResizeFailed;
            }
            self.size = size;
            return;
        }

        return error.UnsupportedPlatform;
    }

    pub fn close(self: *WindowsConPty) void {
        if (builtin.os.tag == .windows) {
            if (self.conpty) |conpty| win32.ClosePseudoConsole(conpty);
            if (self.input_write) |handle| win.CloseHandle(handle);
            if (self.output_read) |handle| win.CloseHandle(handle);
        }
        self.conpty = null;
        self.input_write = null;
        self.output_read = null;
    }

    fn initWindows(size: core.PtySize) core.PtyError!WindowsConPty {
        var input_read: ?win.HANDLE = null;
        var input_write: ?win.HANDLE = null;
        var output_read: ?win.HANDLE = null;
        var output_write: ?win.HANDLE = null;
        var conpty: ?win32.HPCON = null;

        var security = win.SECURITY_ATTRIBUTES{
            .nLength = @sizeOf(win.SECURITY_ATTRIBUTES),
            .lpSecurityDescriptor = null,
            .bInheritHandle = win.FALSE,
        };

        var input_read_handle: win.HANDLE = undefined;
        var input_write_handle: win.HANDLE = undefined;
        win.CreatePipe(&input_read_handle, &input_write_handle, &security) catch return error.OpenFailed;
        input_read = input_read_handle;
        input_write = input_write_handle;
        errdefer if (input_read) |handle| win.CloseHandle(handle);
        errdefer if (input_write) |handle| win.CloseHandle(handle);

        var output_read_handle: win.HANDLE = undefined;
        var output_write_handle: win.HANDLE = undefined;
        win.CreatePipe(&output_read_handle, &output_write_handle, &security) catch return error.OpenFailed;
        output_read = output_read_handle;
        output_write = output_write_handle;
        errdefer if (output_read) |handle| win.CloseHandle(handle);
        errdefer if (output_write) |handle| win.CloseHandle(handle);

        if (failed(win32.CreatePseudoConsole(
            try coordFromSize(size),
            input_read.?,
            output_write.?,
            0,
            &conpty,
        ))) {
            return error.OpenFailed;
        }
        errdefer if (conpty) |handle| win32.ClosePseudoConsole(handle);

        win.CloseHandle(input_read.?);
        input_read = null;
        win.CloseHandle(output_write.?);
        output_write = null;

        return .{
            .conpty = conpty,
            .input_write = input_write,
            .output_read = output_read,
            .size = size,
        };
    }
};

fn fromPty(pty: *core.Pty) *WindowsConPty {
    return @ptrCast(@alignCast(pty.context));
}

fn readAdapter(pty: *core.Pty, buffer: []u8) core.PtyError!usize {
    return try fromPty(pty).read(buffer);
}

fn writeAdapter(pty: *core.Pty, bytes: []const u8) core.PtyError!usize {
    return try fromPty(pty).write(bytes);
}

fn resizeAdapter(pty: *core.Pty, size: core.PtySize) core.PtyError!void {
    try fromPty(pty).resize(size);
}

fn closeAdapter(pty: *core.Pty) void {
    fromPty(pty).close();
}

fn coordFromSize(size: core.PtySize) core.PtyError!win.COORD {
    if (size.cols > std.math.maxInt(win.SHORT) or size.rows > std.math.maxInt(win.SHORT)) {
        return error.InvalidSize;
    }
    return .{
        .X = @intCast(size.cols),
        .Y = @intCast(size.rows),
    };
}

fn failed(hr: win.HRESULT) bool {
    return hr < 0;
}

const win32 = struct {
    const HPCON = *anyopaque;

    extern "kernel32" fn CreatePseudoConsole(
        size: win.COORD,
        input: win.HANDLE,
        output: win.HANDLE,
        flags: win.DWORD,
        conpty: *?HPCON,
    ) callconv(.winapi) win.HRESULT;

    extern "kernel32" fn ResizePseudoConsole(
        conpty: HPCON,
        size: win.COORD,
    ) callconv(.winapi) win.HRESULT;

    extern "kernel32" fn ClosePseudoConsole(
        conpty: HPCON,
    ) callconv(.winapi) void;

    extern "kernel32" fn PeekNamedPipe(
        named_pipe: win.HANDLE,
        buffer: ?[*]u8,
        buffer_size: win.DWORD,
        bytes_read: ?*win.DWORD,
        total_bytes_available: ?*win.DWORD,
        bytes_left_this_message: ?*win.DWORD,
    ) callconv(.winapi) win.BOOL;
};

test "windows conpty validates size before platform support" {
    try std.testing.expectError(error.InvalidSize, WindowsConPty.init(.{ .cols = 0, .rows = 24 }));
}

test "windows conpty is behind the pty abstraction on non-windows hosts" {
    if (builtin.os.tag != .windows) {
        try std.testing.expectError(error.UnsupportedPlatform, WindowsConPty.init(.{ .cols = 80, .rows = 24 }));
    }
}

test "windows conpty exposes the shared pty vtable on windows" {
    if (builtin.os.tag != .windows) return error.SkipZigTest;

    var pty = WindowsConPty.init(.{ .cols = 80, .rows = 24 }) catch |err| switch (err) {
        error.OpenFailed => return error.SkipZigTest,
        else => return err,
    };
    defer pty.close();

    var raw = pty.asPty();
    try raw.resize(&raw, .{ .cols = 100, .rows = 32 });
    try std.testing.expectEqual(@as(usize, 0), try raw.read(&raw, &.{}));
    try std.testing.expectEqual(@as(usize, 0), try raw.write(&raw, ""));
}
