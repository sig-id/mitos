const std = @import("std");

pub const TerminalError = error{
    BackendFailure,
    InvalidSize,
    OutOfMemory,
    UnsupportedInput,
};

pub const CellSize = struct {
    width_px: u16,
    height_px: u16,
};

pub const CursorState = struct {
    x: u16,
    y: u16,
    visible: bool = true,
};

pub const RgbColor = struct {
    r: u8,
    g: u8,
    b: u8,
};

pub const Color = union(enum) {
    indexed: u8,
    rgb: RgbColor,
};

pub const CellStyle = struct {
    bold: bool = false,
    italic: bool = false,
    underline: bool = false,
    inverse: bool = false,
    fg: ?Color = null,
    bg: ?Color = null,
};

pub const Cell = struct {
    x: u16,
    y: u16,
    text: []const u8,
    style: CellStyle = .{},
};

pub const ScreenSnapshot = struct {
    cols: u16,
    rows: u16,
    cursor: CursorState,
    title: ?[]const u8 = null,
    alternate_screen: bool = false,
    cells: []const Cell,
    timestamp_ms: i64,

    pub fn deinit(self: ScreenSnapshot, allocator: std.mem.Allocator) void {
        if (self.title) |title| {
            if (title.len > 0) allocator.free(title);
        }
        for (self.cells) |cell| {
            if (cell.text.len > 0) allocator.free(cell.text);
        }
        if (self.cells.len > 0) allocator.free(self.cells);
    }
};

pub const TerminalEffect = union(enum) {
    bell,
    title_changed: []const u8,
    write_to_pty: []const u8,
    clipboard_request,
    clipboard_set: []const u8,
    screen_changed,

    pub fn deinit(self: TerminalEffect, allocator: std.mem.Allocator) void {
        switch (self) {
            .title_changed, .write_to_pty, .clipboard_set => |bytes| {
                if (bytes.len > 0) allocator.free(bytes);
            },
            else => {},
        }
    }
};

pub const TerminalEffects = struct {
    items: []const TerminalEffect = &.{},

    pub fn deinit(self: TerminalEffects, allocator: std.mem.Allocator) void {
        for (self.items) |effect| {
            effect.deinit(allocator);
        }
        if (self.items.len > 0) allocator.free(self.items);
    }
};

pub const KeyModifiers = packed struct {
    shift: bool = false,
    alt: bool = false,
    ctrl: bool = false,
    super: bool = false,
};

pub const SpecialKey = enum {
    enter,
    escape,
    backspace,
    tab,
    up,
    down,
    left,
    right,
    home,
    end,
    page_up,
    page_down,
    delete,
    insert,
};

pub const KeyInput = union(enum) {
    text: []const u8,
    special: struct {
        key: SpecialKey,
        modifiers: KeyModifiers = .{},
    },
};

pub const TerminalCore = struct {
    context: *anyopaque,
    write_from_pty: *const fn (*TerminalCore, []const u8, std.mem.Allocator) TerminalError!TerminalEffects,
    resize: *const fn (*TerminalCore, u16, u16, CellSize) TerminalError!void,
    snapshot: *const fn (*TerminalCore, std.mem.Allocator) TerminalError!ScreenSnapshot,
    visible_text: *const fn (*TerminalCore, std.mem.Allocator) TerminalError![]u8,
    scrollback_text: *const fn (*TerminalCore, std.mem.Allocator, usize) TerminalError![]u8,
    encode_key: *const fn (*TerminalCore, KeyInput, std.mem.Allocator) TerminalError![]u8,
    encode_paste: *const fn (*TerminalCore, []const u8, std.mem.Allocator) TerminalError![]u8,
};

test "screen snapshots own allocated cell text" {
    const allocator = std.testing.allocator;
    const title = try allocator.dupe(u8, "Mitos");
    const cells = try allocator.alloc(Cell, 1);
    cells[0] = .{
        .x = 0,
        .y = 0,
        .text = try allocator.dupe(u8, "ok"),
    };

    const snapshot: ScreenSnapshot = .{
        .cols = 80,
        .rows = 24,
        .cursor = .{ .x = 0, .y = 0 },
        .title = title,
        .cells = cells,
        .timestamp_ms = 1,
    };
    snapshot.deinit(allocator);
}

test "terminal effects own emitted byte slices" {
    const allocator = std.testing.allocator;
    const items = try allocator.alloc(TerminalEffect, 2);
    items[0] = .bell;
    items[1] = .{ .write_to_pty = try allocator.dupe(u8, "\x1b[6n") };

    const effects: TerminalEffects = .{ .items = items };
    effects.deinit(allocator);
}
