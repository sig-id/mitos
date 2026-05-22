const std = @import("std");
const builtin = @import("builtin");
const core = @import("core.zig");

pub const UnixPtyPair = struct {
    allocator: std.mem.Allocator,
    master: UnixPty,
    slave_fd: std.posix.fd_t,
    slave_path: []const u8,

    pub fn deinit(self: *UnixPtyPair) void {
        self.master.close();
        std.posix.close(self.slave_fd);
        self.allocator.free(self.slave_path);
        self.* = undefined;
    }
};

pub const UnixPty = struct {
    master_fd: std.posix.fd_t,
    owns_fd: bool = false,
    size: core.PtySize,

    pub fn open(allocator: std.mem.Allocator, size: core.PtySize) core.PtyError!UnixPtyPair {
        try size.validate();
        if (builtin.os.tag != .linux) return error.UnsupportedPlatform;

        const master_flags: std.posix.O = .{
            .ACCMODE = .RDWR,
            .NOCTTY = true,
            .CLOEXEC = true,
            .NONBLOCK = true,
        };
        const slave_flags: std.posix.O = .{
            .ACCMODE = .RDWR,
            .NOCTTY = true,
            .CLOEXEC = true,
        };
        const master_fd = std.posix.open("/dev/ptmx", master_flags, 0) catch return error.OpenFailed;
        errdefer std.posix.close(master_fd);

        var unlock: c_int = 0;
        try ioctlOpen(master_fd, std.os.linux.T.IOCSPTLCK, @intFromPtr(&unlock));

        var pty_number: c_uint = 0;
        try ioctlOpen(master_fd, std.os.linux.T.IOCGPTN, @intFromPtr(&pty_number));

        const slave_path = try std.fmt.allocPrint(allocator, "/dev/pts/{}", .{pty_number});
        errdefer allocator.free(slave_path);

        const slave_fd = std.posix.open(slave_path, slave_flags, 0) catch return error.OpenFailed;
        errdefer std.posix.close(slave_fd);

        var master = try UnixPty.init(master_fd, size);
        master.owns_fd = true;
        try master.resize(size);

        return .{
            .allocator = allocator,
            .master = master,
            .slave_fd = slave_fd,
            .slave_path = slave_path,
        };
    }

    pub fn init(master_fd: std.posix.fd_t, size: core.PtySize) core.PtyError!UnixPty {
        try size.validate();
        return .{
            .master_fd = master_fd,
            .size = size,
        };
    }

    pub fn asPty(self: *UnixPty) core.Pty {
        return .{
            .context = self,
            .read = readAdapter,
            .write = writeAdapter,
            .resize = resizeAdapter,
            .close = closeAdapter,
        };
    }

    pub fn read(self: *UnixPty, buffer: []u8) core.PtyError!usize {
        return std.posix.read(self.master_fd, buffer) catch |err| switch (err) {
            error.WouldBlock => return error.WouldBlock,
            error.InputOutput => return error.Closed,
            else => return error.ReadFailed,
        };
    }

    pub fn write(self: *UnixPty, bytes: []const u8) core.PtyError!usize {
        return std.posix.write(self.master_fd, bytes) catch return error.WriteFailed;
    }

    pub fn resize(self: *UnixPty, size: core.PtySize) core.PtyError!void {
        try size.validate();
        self.size = size;

        if (builtin.os.tag != .linux) return error.UnsupportedPlatform;

        const linux = std.os.linux;
        var winsize: std.posix.winsize = .{
            .row = size.rows,
            .col = size.cols,
            .xpixel = size.width_px,
            .ypixel = size.height_px,
        };
        const rc = linux.syscall3(
            .ioctl,
            @as(usize, @intCast(self.master_fd)),
            linux.T.IOCSWINSZ,
            @intFromPtr(&winsize),
        );

        switch (std.posix.errno(rc)) {
            .SUCCESS => {},
            else => return error.ResizeFailed,
        }
    }

    pub fn close(self: *UnixPty) void {
        if (self.owns_fd) {
            std.posix.close(self.master_fd);
            self.owns_fd = false;
        }
    }
};

pub const SpawnConfig = struct {
    argv: []const []const u8,
    cwd: ?[]const u8 = null,
    env: []const []const u8 = &.{},
    size: core.PtySize = .{ .cols = 80, .rows = 24 },
};

pub const UnixPtySession = struct {
    allocator: std.mem.Allocator,
    pty: UnixPty,
    pid: std.posix.pid_t,
    waited: bool = false,

    pub fn asPty(self: *UnixPtySession) core.Pty {
        return self.pty.asPty();
    }

    pub fn wait(self: *UnixPtySession) core.ProcessTerm {
        const result = std.posix.waitpid(self.pid, 0);
        self.waited = true;
        return processTermFromStatus(result.status);
    }

    pub fn kill(self: *UnixPtySession, signal: u8) core.PtyError!void {
        std.posix.kill(self.pid, signal) catch return error.SignalFailed;
    }

    pub fn deinit(self: *UnixPtySession) void {
        self.pty.close();
        self.* = undefined;
    }
};

pub fn spawn(allocator: std.mem.Allocator, config: SpawnConfig) core.PtyError!UnixPtySession {
    if (config.argv.len == 0) return error.ExecFailed;

    var pair = try UnixPty.open(allocator, config.size);
    errdefer pair.deinit();

    var argv = try CStringVector.init(allocator, config.argv);
    defer argv.deinit();
    var env = try CStringVector.init(allocator, config.env);
    defer env.deinit();
    const cwd = if (config.cwd) |path| try allocator.dupeZ(u8, path) else null;
    defer if (cwd) |path| allocator.free(path);

    const err_pipe = std.posix.pipe2(.{ .CLOEXEC = true }) catch return error.SpawnFailed;
    defer std.posix.close(err_pipe[0]);
    // The parent's write end of err_pipe is closed explicitly right after
    // fork so that read() can return 0 when the child execs successfully.
    // The errdefer below must therefore be conditional: closing the fd twice
    // panics with `.BADF => unreachable` on the second close.
    var err_write_open = true;
    errdefer if (err_write_open) std.posix.close(err_pipe[1]);

    const pid = std.posix.fork() catch return error.ForkFailed;
    if (pid == 0) {
        childExec(pair.master.master_fd, pair.slave_fd, err_pipe, argv.ptr(), env.ptr(), cwd);
    }

    std.posix.close(err_pipe[1]);
    err_write_open = false;
    errdefer _ = std.posix.waitpid(pid, 0);

    var child_error: [1]u8 = undefined;
    const n = std.posix.read(err_pipe[0], &child_error) catch return error.SpawnFailed;
    if (n != 0) {
        // The errdefer above reaps the child; calling waitpid here too would
        // race the kernel and trip `.CHILD => unreachable` in std.posix on
        // the second wait.
        return switch (child_error[0]) {
            2 => error.ExecFailed,
            else => error.ChildSetupFailed,
        };
    }

    std.posix.close(pair.slave_fd);
    const session = UnixPtySession{
        .allocator = allocator,
        .pty = pair.master,
        .pid = pid,
    };
    allocator.free(pair.slave_path);
    pair.master.owns_fd = false;
    pair.slave_fd = -1;
    pair.slave_path = "";
    return session;
}

const CStringVector = struct {
    allocator: std.mem.Allocator,
    owned: [][:0]u8,
    pointers: []?[*:0]const u8,

    fn init(allocator: std.mem.Allocator, values: []const []const u8) core.PtyError!CStringVector {
        const owned = allocator.alloc([:0]u8, values.len) catch return error.OutOfMemory;
        errdefer allocator.free(owned);

        var initialized: usize = 0;
        errdefer {
            for (owned[0..initialized]) |item| allocator.free(item);
        }
        for (values, 0..) |value, index_| {
            owned[index_] = allocator.dupeZ(u8, value) catch return error.OutOfMemory;
            initialized += 1;
        }

        const pointers = allocator.alloc(?[*:0]const u8, values.len + 1) catch return error.OutOfMemory;
        errdefer allocator.free(pointers);
        for (owned, 0..) |item, index_| pointers[index_] = item.ptr;
        pointers[values.len] = null;

        return .{
            .allocator = allocator,
            .owned = owned,
            .pointers = pointers,
        };
    }

    fn ptr(self: *const CStringVector) [*:null]const ?[*:0]const u8 {
        return @ptrCast(self.pointers.ptr);
    }

    fn deinit(self: *CStringVector) void {
        for (self.owned) |item| self.allocator.free(item);
        self.allocator.free(self.owned);
        self.allocator.free(self.pointers);
        self.* = undefined;
    }
};

fn childExec(
    master_fd: std.posix.fd_t,
    slave_fd: std.posix.fd_t,
    err_pipe: [2]std.posix.fd_t,
    argv: [*:null]const ?[*:0]const u8,
    env: [*:null]const ?[*:0]const u8,
    cwd: ?[:0]const u8,
) noreturn {
    std.posix.close(err_pipe[0]);
    std.posix.close(master_fd);

    setsidChild() catch {
        reportChildError(err_pipe[1], 1);
    };
    ioctlChild(slave_fd, std.os.linux.T.IOCSCTTY, 0) catch {
        reportChildError(err_pipe[1], 1);
    };
    std.posix.dup2(slave_fd, std.posix.STDIN_FILENO) catch {
        reportChildError(err_pipe[1], 1);
    };
    std.posix.dup2(slave_fd, std.posix.STDOUT_FILENO) catch {
        reportChildError(err_pipe[1], 1);
    };
    std.posix.dup2(slave_fd, std.posix.STDERR_FILENO) catch {
        reportChildError(err_pipe[1], 1);
    };
    if (slave_fd > std.posix.STDERR_FILENO) std.posix.close(slave_fd);
    if (cwd) |path| std.posix.chdirZ(path.ptr) catch {
        reportChildError(err_pipe[1], 1);
    };

    const file = argv[0] orelse reportChildError(err_pipe[1], 2);
    std.posix.execvpeZ(file, argv, env) catch {
        reportChildError(err_pipe[1], 2);
    };
    unreachable;
}

fn reportChildError(fd: std.posix.fd_t, code: u8) noreturn {
    const bytes = [_]u8{code};
    _ = std.posix.write(fd, &bytes) catch {};
    std.os.linux.exit(127);
}

fn ioctlChild(fd: std.posix.fd_t, request: usize, arg: usize) core.PtyError!void {
    const rc = std.os.linux.syscall3(
        .ioctl,
        @as(usize, @intCast(fd)),
        request,
        arg,
    );

    switch (std.posix.errno(rc)) {
        .SUCCESS => {},
        else => return error.ChildSetupFailed,
    }
}

fn setsidChild() core.PtyError!void {
    const rc: usize = std.os.linux.syscall0(.setsid);
    if (isLinuxSyscallError(rc)) return error.ChildSetupFailed;
}

fn isLinuxSyscallError(rc: usize) bool {
    return rc >= std.math.maxInt(usize) - 4095;
}

fn processTermFromStatus(status: u32) core.ProcessTerm {
    if (std.posix.W.IFEXITED(status)) return .{ .exited = std.posix.W.EXITSTATUS(status) };
    if (std.posix.W.IFSIGNALED(status)) return .{ .signaled = std.posix.W.TERMSIG(status) };
    if (std.posix.W.IFSTOPPED(status)) return .{ .stopped = std.posix.W.STOPSIG(status) };
    return .{ .unknown = status };
}

fn ioctlOpen(fd: std.posix.fd_t, request: usize, arg: usize) core.PtyError!void {
    const rc = std.os.linux.syscall3(
        .ioctl,
        @as(usize, @intCast(fd)),
        request,
        arg,
    );

    switch (std.posix.errno(rc)) {
        .SUCCESS => {},
        else => return error.OpenFailed,
    }
}

fn fromPty(pty: *core.Pty) *UnixPty {
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

test "unix pty wrapper exposes stable size metadata" {
    const pty = try UnixPty.init(0, .{ .cols = 80, .rows = 24 });
    try std.testing.expectEqual(@as(u16, 80), pty.size.cols);
    try std.testing.expectEqual(@as(u16, 24), pty.size.rows);
}
