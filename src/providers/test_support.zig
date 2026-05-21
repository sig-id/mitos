const std = @import("std");
const adapters = @import("../adapters/types.zig");

pub const FakeBin = struct {
    allocator: std.mem.Allocator,
    tmp: std.testing.TmpDir,
    path: []u8,

    pub fn init(allocator: std.mem.Allocator) !FakeBin {
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        const cwd = try std.fs.cwd().realpathAlloc(allocator, ".");
        defer allocator.free(cwd);
        const path = try std.fs.path.join(allocator, &.{
            cwd,
            ".zig-cache",
            "tmp",
            tmp.sub_path[0..],
        });
        return .{
            .allocator = allocator,
            .tmp = tmp,
            .path = path,
        };
    }

    pub fn deinit(self: *FakeBin) void {
        self.allocator.free(self.path);
        self.tmp.cleanup();
        self.* = undefined;
    }

    pub fn writeExecutable(self: *FakeBin, name: []const u8, script: []const u8) !void {
        var file = try self.tmp.dir.createFile(name, .{});
        defer file.close();
        try file.writeAll(script);
        try file.chmod(0o755);
    }

    pub fn pathEnv(self: FakeBin) adapters.EnvVar {
        return .{
            .name = "PATH",
            .value = self.path,
        };
    }
};

pub const AdapterEventRecorder = struct {
    expected_final: []const u8 = "",
    exit_code: ?u8 = null,
    event_count: usize = 0,
    saw_process_spawned: bool = false,
    saw_process_exited: bool = false,
    saw_structured: bool = false,
    saw_final: bool = false,
    saw_expected_final: bool = false,
    saw_error: bool = false,

    pub fn sink(self: *AdapterEventRecorder) adapters.AdapterEventSink {
        return .{
            .context = self,
            .emit = emit,
        };
    }

    fn emit(context: *anyopaque, event: adapters.AdapterEvent) anyerror!void {
        const self: *AdapterEventRecorder = @ptrCast(@alignCast(context));
        self.event_count += 1;
        switch (event) {
            .process_spawned => self.saw_process_spawned = true,
            .process_exited => |process| {
                self.saw_process_exited = true;
                self.exit_code = process.exit_code;
            },
            .structured => self.saw_structured = true,
            .final_output => |text| {
                self.saw_final = true;
                if (std.mem.eql(u8, text, self.expected_final)) {
                    self.saw_expected_final = true;
                }
            },
            .error_message => self.saw_error = true,
            else => {},
        }
    }
};
