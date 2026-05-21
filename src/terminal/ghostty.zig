const std = @import("std");
const builtin = @import("builtin");
const core = @import("core.zig");

const CellState = struct {
    text: []const u8 = "",
    style: core.CellStyle = .{},
};

pub const Options = struct {
    cols: u16 = 80,
    rows: u16 = 24,
    cell_size: core.CellSize = .{ .width_px = 8, .height_px = 16 },
    max_scrollback: usize = 10_000,
};

pub const GhosttyTerminal = struct {
    allocator: std.mem.Allocator,
    cols: u16,
    rows: u16,
    cell_size: core.CellSize,
    cursor: core.CursorState = .{ .x = 0, .y = 0 },
    saved_cursor: core.CursorState = .{ .x = 0, .y = 0 },
    title: ?[]const u8 = null,
    alternate_screen: bool = false,
    current_style: core.CellStyle = .{},
    main_cells: []CellState,
    alt_cells: []CellState,
    scrollback: std.ArrayList([]const u8) = .empty,

    pub fn init(allocator: std.mem.Allocator, options: Options) core.TerminalError!GhosttyTerminal {
        if (options.cols == 0 or options.rows == 0) return error.InvalidSize;

        const len = cellCount(options.cols, options.rows);
        const main_cells = try allocator.alloc(CellState, len);
        errdefer allocator.free(main_cells);
        const alt_cells = try allocator.alloc(CellState, len);
        errdefer allocator.free(alt_cells);

        clearCellSlice(main_cells);
        clearCellSlice(alt_cells);

        return .{
            .allocator = allocator,
            .cols = options.cols,
            .rows = options.rows,
            .cell_size = options.cell_size,
            .main_cells = main_cells,
            .alt_cells = alt_cells,
        };
    }

    pub fn deinit(self: *GhosttyTerminal) void {
        self.clearCells(self.main_cells);
        self.clearCells(self.alt_cells);
        self.allocator.free(self.main_cells);
        self.allocator.free(self.alt_cells);
        if (self.title) |title| self.allocator.free(title);
        for (self.scrollback.items) |line| self.allocator.free(line);
        self.scrollback.deinit(self.allocator);
        self.* = undefined;
    }

    pub fn asCore(self: *GhosttyTerminal) core.TerminalCore {
        return .{
            .context = self,
            .write_from_pty = writeFromPtyAdapter,
            .resize = resizeAdapter,
            .snapshot = snapshotAdapter,
            .visible_text = visibleTextAdapter,
            .scrollback_text = scrollbackTextAdapter,
            .encode_key = encodeKeyAdapter,
            .encode_paste = encodePasteAdapter,
        };
    }

    pub fn writeFromPty(
        self: *GhosttyTerminal,
        bytes: []const u8,
        allocator: std.mem.Allocator,
    ) core.TerminalError!core.TerminalEffects {
        var effects: std.ArrayList(core.TerminalEffect) = .empty;
        errdefer {
            for (effects.items) |effect| effect.deinit(allocator);
            effects.deinit(allocator);
        }

        var changed = false;
        var i: usize = 0;
        while (i < bytes.len) {
            const b = bytes[i];
            switch (b) {
                0x07 => {
                    try effects.append(allocator, .bell);
                    i += 1;
                },
                0x08 => {
                    if (self.cursor.x > 0) self.cursor.x -= 1;
                    changed = true;
                    i += 1;
                },
                '\t' => {
                    const next_tab = @min(
                        @as(usize, self.cols - 1),
                        ((@as(usize, self.cursor.x) / 8) + 1) * 8,
                    );
                    self.cursor.x = @as(u16, @intCast(next_tab));
                    changed = true;
                    i += 1;
                },
                '\n' => {
                    try self.newLine();
                    changed = true;
                    i += 1;
                },
                '\r' => {
                    self.cursor.x = 0;
                    changed = true;
                    i += 1;
                },
                0x1b => {
                    const consumed = try self.consumeEscape(bytes[i..], allocator, &effects, &changed);
                    if (consumed == 0) break;
                    i += consumed;
                },
                0x00...0x06, 0x0b, 0x0c, 0x0e...0x1a, 0x1c...0x1f, 0x7f => {
                    i += 1;
                },
                else => {
                    const consumed = try utf8SequenceLen(bytes[i..]);
                    try self.putGrapheme(bytes[i .. i + consumed]);
                    changed = true;
                    i += consumed;
                },
            }
        }

        if (changed) try effects.append(allocator, .screen_changed);

        return .{ .items = try effects.toOwnedSlice(allocator) };
    }

    pub fn resize(self: *GhosttyTerminal, cols: u16, rows: u16, cell_size: core.CellSize) core.TerminalError!void {
        if (cols == 0 or rows == 0) return error.InvalidSize;

        const text = try self.visibleText(self.allocator);
        defer self.allocator.free(text);

        const len = cellCount(cols, rows);
        const new_main_cells = try self.allocator.alloc(CellState, len);
        errdefer self.allocator.free(new_main_cells);
        const new_alt_cells = try self.allocator.alloc(CellState, len);
        errdefer self.allocator.free(new_alt_cells);
        clearCellSlice(new_main_cells);
        clearCellSlice(new_alt_cells);

        self.clearCells(self.main_cells);
        self.clearCells(self.alt_cells);
        self.allocator.free(self.main_cells);
        self.allocator.free(self.alt_cells);

        self.main_cells = new_main_cells;
        self.alt_cells = new_alt_cells;
        self.cols = cols;
        self.rows = rows;
        self.cell_size = cell_size;
        self.cursor = .{ .x = 0, .y = 0 };
        self.saved_cursor = .{ .x = 0, .y = 0 };

        const effects = try self.writeFromPty(text, self.allocator);
        effects.deinit(self.allocator);
    }

    pub fn snapshot(self: *GhosttyTerminal, allocator: std.mem.Allocator) core.TerminalError!core.ScreenSnapshot {
        var cells: std.ArrayList(core.Cell) = .empty;
        errdefer {
            for (cells.items) |cell| allocator.free(cell.text);
            cells.deinit(allocator);
        }

        const active = self.activeCells();
        var y: u16 = 0;
        while (y < self.rows) : (y += 1) {
            var x: u16 = 0;
            while (x < self.cols) : (x += 1) {
                const cell = active[self.index(x, y)];
                if (cell.text.len == 0) continue;
                try cells.append(allocator, .{
                    .x = x,
                    .y = y,
                    .text = try allocator.dupe(u8, cell.text),
                    .style = cell.style,
                });
            }
        }

        const title = if (self.title) |title_text| try allocator.dupe(u8, title_text) else null;
        errdefer if (title) |owned_title| allocator.free(owned_title);

        return .{
            .cols = self.cols,
            .rows = self.rows,
            .cursor = self.cursor,
            .title = title,
            .alternate_screen = self.alternate_screen,
            .cells = try cells.toOwnedSlice(allocator),
            .timestamp_ms = std.time.milliTimestamp(),
        };
    }

    pub fn visibleText(self: *GhosttyTerminal, allocator: std.mem.Allocator) core.TerminalError![]u8 {
        var out: std.ArrayList(u8) = .empty;
        errdefer out.deinit(allocator);

        const last = self.lastNonEmptyRow();
        if (last == null) return try allocator.dupe(u8, "");

        var y: u16 = 0;
        while (y <= last.?) : (y += 1) {
            if (y != 0) try out.append(allocator, '\n');
            try self.appendLineText(allocator, &out, y);
        }

        return try out.toOwnedSlice(allocator);
    }

    pub fn scrollbackText(
        self: *GhosttyTerminal,
        allocator: std.mem.Allocator,
        max_lines: usize,
    ) core.TerminalError![]u8 {
        var out: std.ArrayList(u8) = .empty;
        errdefer out.deinit(allocator);

        const start = if (self.scrollback.items.len > max_lines)
            self.scrollback.items.len - max_lines
        else
            0;

        for (self.scrollback.items[start..], 0..) |line, idx| {
            if (idx != 0) try out.append(allocator, '\n');
            try out.appendSlice(allocator, line);
        }

        return try out.toOwnedSlice(allocator);
    }

    pub fn encodeKey(
        _: *GhosttyTerminal,
        input: core.KeyInput,
        allocator: std.mem.Allocator,
    ) core.TerminalError![]u8 {
        return encodeKeyInput(input, allocator);
    }

    pub fn encodePaste(
        _: *GhosttyTerminal,
        bytes: []const u8,
        allocator: std.mem.Allocator,
    ) core.TerminalError![]u8 {
        return try std.mem.concat(allocator, u8, &.{ "\x1b[200~", bytes, "\x1b[201~" });
    }

    fn consumeEscape(
        self: *GhosttyTerminal,
        bytes: []const u8,
        allocator: std.mem.Allocator,
        effects: *std.ArrayList(core.TerminalEffect),
        changed: *bool,
    ) core.TerminalError!usize {
        if (bytes.len < 2) return 0;

        switch (bytes[1]) {
            '[' => {
                const end = findCsiEnd(bytes[2..]) orelse return 0;
                try self.handleCsi(bytes[2 .. 2 + end], bytes[2 + end], allocator, effects, changed);
                return 3 + end;
            },
            ']' => {
                const end = findOscEnd(bytes[2..]) orelse return 0;
                const osc = bytes[2 .. 2 + end.len];
                try self.handleOsc(osc, allocator, effects, changed);
                return 2 + end.consumed;
            },
            '7', 's' => {
                self.saved_cursor = self.cursor;
                return 2;
            },
            '8', 'u' => {
                self.cursor = self.saved_cursor;
                changed.* = true;
                return 2;
            },
            'c' => {
                try self.reset();
                changed.* = true;
                return 2;
            },
            'D' => {
                try self.newLine();
                changed.* = true;
                return 2;
            },
            'E' => {
                self.cursor.x = 0;
                try self.newLine();
                changed.* = true;
                return 2;
            },
            'M' => {
                if (self.cursor.y > 0) self.cursor.y -= 1;
                changed.* = true;
                return 2;
            },
            '(', ')', '=', '>' => return @min(bytes.len, @as(usize, 3)),
            else => return 2,
        }
    }

    fn handleCsi(
        self: *GhosttyTerminal,
        raw_params: []const u8,
        final: u8,
        allocator: std.mem.Allocator,
        effects: *std.ArrayList(core.TerminalEffect),
        changed: *bool,
    ) core.TerminalError!void {
        const private = raw_params.len > 0 and raw_params[0] == '?';
        const params_source = if (private) raw_params[1..] else raw_params;
        var params = parseParams(params_source);

        switch (final) {
            'A' => {
                self.cursor.y -|= boundedAmount(paramOr(&params, 0, 1), self.rows);
                changed.* = true;
            },
            'B' => {
                const next = @min(@as(usize, self.rows - 1), @as(usize, self.cursor.y) + boundedAmount(paramOr(&params, 0, 1), self.rows));
                self.cursor.y = @as(u16, @intCast(next));
                changed.* = true;
            },
            'C' => {
                const next = @min(@as(usize, self.cols - 1), @as(usize, self.cursor.x) + boundedAmount(paramOr(&params, 0, 1), self.cols));
                self.cursor.x = @as(u16, @intCast(next));
                changed.* = true;
            },
            'D' => {
                self.cursor.x -|= boundedAmount(paramOr(&params, 0, 1), self.cols);
                changed.* = true;
            },
            'H', 'f' => {
                const row = paramOr(&params, 0, 1);
                const col = paramOr(&params, 1, 1);
                self.cursor.y = clampOneBased(row, self.rows);
                self.cursor.x = clampOneBased(col, self.cols);
                changed.* = true;
            },
            'J' => {
                try self.eraseDisplay(paramOr(&params, 0, 0));
                changed.* = true;
            },
            'K' => {
                self.eraseLine(paramOr(&params, 0, 0));
                changed.* = true;
            },
            'm' => {
                self.applySgr(params.items[0..params.len]);
                changed.* = true;
            },
            'h', 'l' => if (private) {
                self.applyPrivateMode(params.items[0..params.len], final == 'h');
                changed.* = true;
            },
            'n' => {
                if (paramOr(&params, 0, 0) == 6) {
                    const reply = try std.fmt.allocPrint(
                        allocator,
                        "\x1b[{};{}R",
                        .{ self.cursor.y + 1, self.cursor.x + 1 },
                    );
                    try effects.append(allocator, .{ .write_to_pty = reply });
                }
            },
            else => {},
        }
    }

    fn handleOsc(
        self: *GhosttyTerminal,
        osc: []const u8,
        allocator: std.mem.Allocator,
        effects: *std.ArrayList(core.TerminalEffect),
        changed: *bool,
    ) core.TerminalError!void {
        const semi = std.mem.indexOfScalar(u8, osc, ';') orelse return;
        const command = osc[0..semi];
        const payload = osc[semi + 1 ..];

        if (std.mem.eql(u8, command, "0") or std.mem.eql(u8, command, "2")) {
            const owned = try self.allocator.dupe(u8, payload);
            if (self.title) |old| self.allocator.free(old);
            self.title = owned;
            try effects.append(allocator, .{ .title_changed = try allocator.dupe(u8, payload) });
            changed.* = true;
        } else if (std.mem.eql(u8, command, "52")) {
            const last_semi = std.mem.lastIndexOfScalar(u8, payload, ';') orelse return;
            const value = payload[last_semi + 1 ..];
            if (std.mem.eql(u8, value, "?")) {
                try effects.append(allocator, .clipboard_request);
            } else {
                try effects.append(allocator, .{ .clipboard_set = try allocator.dupe(u8, value) });
            }
        }
    }

    fn reset(self: *GhosttyTerminal) core.TerminalError!void {
        self.clearCells(self.main_cells);
        self.clearCells(self.alt_cells);
        self.cursor = .{ .x = 0, .y = 0 };
        self.saved_cursor = .{ .x = 0, .y = 0 };
        self.alternate_screen = false;
        self.current_style = .{};
        if (self.title) |title| {
            self.allocator.free(title);
            self.title = null;
        }
        for (self.scrollback.items) |line| self.allocator.free(line);
        self.scrollback.clearRetainingCapacity();
    }

    fn putGrapheme(self: *GhosttyTerminal, bytes: []const u8) core.TerminalError!void {
        const codepoint = std.unicode.utf8Decode(bytes) catch 0xfffd;
        if (isCombiningMark(codepoint) and self.cursor.x > 0) {
            const x = self.cursor.x - 1;
            const idx = self.index(x, self.cursor.y);
            const cell = &self.activeCells()[idx];
            const combined = try std.mem.concat(self.allocator, u8, &.{ cell.text, bytes });
            if (cell.text.len > 0) self.allocator.free(cell.text);
            cell.text = combined;
            return;
        }

        if (self.cursor.x >= self.cols) try self.newLine();

        const cell = &self.activeCells()[self.index(self.cursor.x, self.cursor.y)];
        self.clearCell(cell);
        cell.text = try self.allocator.dupe(u8, bytes);
        cell.style = self.current_style;

        if (self.cursor.x == self.cols - 1) {
            try self.newLine();
        } else {
            self.cursor.x += 1;
        }
    }

    fn newLine(self: *GhosttyTerminal) core.TerminalError!void {
        self.cursor.x = 0;
        if (self.cursor.y == self.rows - 1) {
            try self.scrollUp();
        } else {
            self.cursor.y += 1;
        }
    }

    fn scrollUp(self: *GhosttyTerminal) core.TerminalError!void {
        const active = self.activeCells();
        if (!self.alternate_screen) {
            const line = try self.lineTextOwned(0);
            try self.scrollback.append(self.allocator, line);
        }

        var row: u16 = 1;
        while (row < self.rows) : (row += 1) {
            var col: u16 = 0;
            while (col < self.cols) : (col += 1) {
                const to = self.index(col, row - 1);
                const from = self.index(col, row);
                self.clearCell(&active[to]);
                active[to] = active[from];
                active[from] = .{};
            }
        }
    }

    fn eraseDisplay(self: *GhosttyTerminal, mode: i32) core.TerminalError!void {
        const active = self.activeCells();
        switch (mode) {
            2, 3 => {
                self.clearCells(active);
                self.cursor = .{ .x = 0, .y = 0 };
            },
            1 => {
                var y: u16 = 0;
                while (y <= self.cursor.y) : (y += 1) {
                    const end_x = if (y == self.cursor.y) self.cursor.x else self.cols - 1;
                    var x: u16 = 0;
                    while (x <= end_x) : (x += 1) self.clearCell(&active[self.index(x, y)]);
                }
            },
            else => {
                var y = self.cursor.y;
                while (y < self.rows) : (y += 1) {
                    const start_x = if (y == self.cursor.y) self.cursor.x else 0;
                    var x = start_x;
                    while (x < self.cols) : (x += 1) self.clearCell(&active[self.index(x, y)]);
                }
            },
        }
    }

    fn eraseLine(self: *GhosttyTerminal, mode: i32) void {
        const active = self.activeCells();
        const start_x: u16 = if (mode == 1 or mode == 2) 0 else self.cursor.x;
        const end_x: u16 = if (mode == 0 or mode == 2) self.cols - 1 else self.cursor.x;
        var x = start_x;
        while (x <= end_x) : (x += 1) self.clearCell(&active[self.index(x, self.cursor.y)]);
    }

    fn applyPrivateMode(self: *GhosttyTerminal, params: []const i32, enabled: bool) void {
        for (params) |param| switch (param) {
            25 => self.cursor.visible = enabled,
            47, 1047, 1049 => {
                self.alternate_screen = enabled;
                self.cursor = .{ .x = 0, .y = 0 };
                if (enabled) self.clearCells(self.alt_cells);
            },
            else => {},
        };
    }

    fn applySgr(self: *GhosttyTerminal, params: []const i32) void {
        if (params.len == 0) {
            self.current_style = .{};
            return;
        }

        var i: usize = 0;
        while (i < params.len) : (i += 1) {
            const p = params[i];
            switch (p) {
                0 => self.current_style = .{},
                1 => self.current_style.bold = true,
                3 => self.current_style.italic = true,
                4 => self.current_style.underline = true,
                7 => self.current_style.inverse = true,
                22 => self.current_style.bold = false,
                23 => self.current_style.italic = false,
                24 => self.current_style.underline = false,
                27 => self.current_style.inverse = false,
                30...37 => self.current_style.fg = .{ .indexed = @as(u8, @intCast(p - 30)) },
                39 => self.current_style.fg = null,
                40...47 => self.current_style.bg = .{ .indexed = @as(u8, @intCast(p - 40)) },
                49 => self.current_style.bg = null,
                90...97 => self.current_style.fg = .{ .indexed = @as(u8, @intCast(p - 90 + 8)) },
                100...107 => self.current_style.bg = .{ .indexed = @as(u8, @intCast(p - 100 + 8)) },
                38, 48 => {
                    const is_fg = p == 38;
                    if (i + 2 < params.len and params[i + 1] == 5) {
                        setColor(&self.current_style, is_fg, .{ .indexed = @as(u8, @intCast(@max(0, @min(255, params[i + 2])))) });
                        i += 2;
                    } else if (i + 4 < params.len and params[i + 1] == 2) {
                        setColor(&self.current_style, is_fg, .{ .rgb = .{
                            .r = @as(u8, @intCast(@max(0, @min(255, params[i + 2])))),
                            .g = @as(u8, @intCast(@max(0, @min(255, params[i + 3])))),
                            .b = @as(u8, @intCast(@max(0, @min(255, params[i + 4])))),
                        } });
                        i += 4;
                    }
                },
                else => {},
            }
        }
    }

    fn lineTextOwned(self: *GhosttyTerminal, row: u16) core.TerminalError![]const u8 {
        var out: std.ArrayList(u8) = .empty;
        errdefer out.deinit(self.allocator);
        try self.appendLineText(self.allocator, &out, row);
        return try out.toOwnedSlice(self.allocator);
    }

    fn appendLineText(
        self: *GhosttyTerminal,
        allocator: std.mem.Allocator,
        out: *std.ArrayList(u8),
        row: u16,
    ) core.TerminalError!void {
        const active = self.activeCells();
        const last = self.lastNonEmptyCol(row) orelse return;
        var col: u16 = 0;
        while (col <= last) : (col += 1) {
            const text = active[self.index(col, row)].text;
            if (text.len == 0) {
                try out.append(allocator, ' ');
            } else {
                try out.appendSlice(allocator, text);
            }
        }
    }

    fn lastNonEmptyRow(self: *GhosttyTerminal) ?u16 {
        var row = self.rows;
        while (row > 0) {
            row -= 1;
            if (self.lastNonEmptyCol(row) != null) return row;
        }
        return null;
    }

    fn lastNonEmptyCol(self: *GhosttyTerminal, row: u16) ?u16 {
        const active = self.activeCells();
        var col = self.cols;
        while (col > 0) {
            col -= 1;
            if (active[self.index(col, row)].text.len > 0) return col;
        }
        return null;
    }

    fn activeCells(self: *GhosttyTerminal) []CellState {
        return if (self.alternate_screen) self.alt_cells else self.main_cells;
    }

    fn index(self: *const GhosttyTerminal, x: u16, y: u16) usize {
        return (@as(usize, y) * self.cols) + x;
    }

    fn clearCells(self: *GhosttyTerminal, cells: []CellState) void {
        for (cells) |*cell| self.clearCell(cell);
    }

    fn clearCell(self: *GhosttyTerminal, cell: *CellState) void {
        if (cell.text.len > 0) self.allocator.free(cell.text);
        cell.* = .{};
    }
};

pub const NativeGhosttyError = core.TerminalError || std.DynLib.Error;

pub const NativeGhosttyTerminal = struct {
    allocator: std.mem.Allocator,
    lib: LibGhostty,
    allocator_box: *std.mem.Allocator,
    c_allocator: *c.Allocator,
    terminal: c.Terminal,
    bridge: *CallbackBridge,
    cell_size: core.CellSize,

    pub fn initFromLibraryPath(
        allocator: std.mem.Allocator,
        library_path: []const u8,
        options: Options,
    ) NativeGhosttyError!NativeGhosttyTerminal {
        if (options.cols == 0 or options.rows == 0) return error.InvalidSize;
        if (!builtin.link_libc) return error.BackendFailure;

        var lib = try LibGhostty.open(library_path);
        errdefer lib.close();

        const allocator_box = try allocator.create(std.mem.Allocator);
        errdefer allocator.destroy(allocator_box);
        allocator_box.* = allocator;

        const c_allocator = try allocator.create(c.Allocator);
        errdefer allocator.destroy(c_allocator);
        c_allocator.* = .{
            .ctx = @ptrCast(allocator_box),
            .vtable = &native_allocator_vtable,
        };

        var terminal: c.Terminal = null;
        try resultToError(lib.terminal_new(c_allocator, &terminal, .{
            .cols = options.cols,
            .rows = options.rows,
            .max_scrollback = options.max_scrollback,
        }));
        errdefer lib.terminal_free(terminal);

        const bridge = try allocator.create(CallbackBridge);
        errdefer allocator.destroy(bridge);
        bridge.* = .{};

        try resultToError(lib.terminal_set(terminal, c.TERMINAL_OPT_USERDATA, @ptrCast(bridge)));
        try resultToError(lib.terminal_set(terminal, c.TERMINAL_OPT_WRITE_PTY, @ptrCast(&nativeWritePtyCallback)));
        try resultToError(lib.terminal_set(terminal, c.TERMINAL_OPT_BELL, @ptrCast(&nativeBellCallback)));
        try resultToError(lib.terminal_set(terminal, c.TERMINAL_OPT_TITLE_CHANGED, @ptrCast(&nativeTitleChangedCallback)));

        return .{
            .allocator = allocator,
            .lib = lib,
            .allocator_box = allocator_box,
            .c_allocator = c_allocator,
            .terminal = terminal,
            .bridge = bridge,
            .cell_size = options.cell_size,
        };
    }

    pub fn deinit(self: *NativeGhosttyTerminal) void {
        self.lib.terminal_free(self.terminal);
        self.allocator.destroy(self.bridge);
        self.allocator.destroy(self.c_allocator);
        self.allocator.destroy(self.allocator_box);
        self.lib.close();
        self.* = undefined;
    }

    pub fn asCore(self: *NativeGhosttyTerminal) core.TerminalCore {
        return .{
            .context = self,
            .write_from_pty = nativeWriteFromPtyAdapter,
            .resize = nativeResizeAdapter,
            .snapshot = nativeSnapshotAdapter,
            .visible_text = nativeVisibleTextAdapter,
            .scrollback_text = nativeScrollbackTextAdapter,
            .encode_key = nativeEncodeKeyAdapter,
            .encode_paste = nativeEncodePasteAdapter,
        };
    }

    fn writeFromPty(
        self: *NativeGhosttyTerminal,
        bytes: []const u8,
        allocator: std.mem.Allocator,
    ) core.TerminalError!core.TerminalEffects {
        var state: CallbackState = .{ .allocator = allocator };
        errdefer state.deinit();

        self.bridge.state = &state;
        self.lib.terminal_vt_write(self.terminal, bytes.ptr, bytes.len);
        self.bridge.state = null;

        if (state.failed) |err| return err;

        if (state.title_changed) {
            const title = try self.titleOwned(allocator);
            try state.effects.append(allocator, .{ .title_changed = title });
        }
        if (bytes.len > 0) try state.effects.append(allocator, .screen_changed);

        return .{ .items = try state.effects.toOwnedSlice(allocator) };
    }

    fn resize(
        self: *NativeGhosttyTerminal,
        cols: u16,
        rows: u16,
        cell_size: core.CellSize,
    ) core.TerminalError!void {
        if (cols == 0 or rows == 0) return error.InvalidSize;
        try resultToError(self.lib.terminal_resize(
            self.terminal,
            cols,
            rows,
            cell_size.width_px,
            cell_size.height_px,
        ));
        self.cell_size = cell_size;
    }

    fn snapshot(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
    ) core.TerminalError!core.ScreenSnapshot {
        const cols = try self.getU16(c.TERMINAL_DATA_COLS);
        const rows = try self.getU16(c.TERMINAL_DATA_ROWS);

        var active_screen: c_int = 0;
        try self.get(c.TERMINAL_DATA_ACTIVE_SCREEN, &active_screen);

        var cells: std.ArrayList(core.Cell) = .empty;
        errdefer {
            for (cells.items) |cell| allocator.free(cell.text);
            cells.deinit(allocator);
        }

        var y: u16 = 0;
        while (y < rows) : (y += 1) {
            var x: u16 = 0;
            while (x < cols) : (x += 1) {
                const cell = try self.cellAt(allocator, c.POINT_TAG_VIEWPORT, x, y) orelse continue;
                try cells.append(allocator, cell);
            }
        }

        const title = try self.optionalTitleOwned(allocator);
        errdefer if (title) |owned_title| allocator.free(owned_title);

        return .{
            .cols = cols,
            .rows = rows,
            .cursor = .{
                .x = try self.getU16(c.TERMINAL_DATA_CURSOR_X),
                .y = try self.getU16(c.TERMINAL_DATA_CURSOR_Y),
                .visible = try self.getBool(c.TERMINAL_DATA_CURSOR_VISIBLE),
            },
            .title = title,
            .alternate_screen = active_screen == c.TERMINAL_SCREEN_ALTERNATE,
            .cells = try cells.toOwnedSlice(allocator),
            .timestamp_ms = std.time.milliTimestamp(),
        };
    }

    fn visibleText(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
    ) core.TerminalError![]u8 {
        const rows = try self.getU16(c.TERMINAL_DATA_ROWS);
        return try self.collectText(allocator, c.POINT_TAG_VIEWPORT, 0, rows);
    }

    fn scrollbackText(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
        max_lines: usize,
    ) core.TerminalError![]u8 {
        var scrollback_rows: usize = 0;
        try self.get(c.TERMINAL_DATA_SCROLLBACK_ROWS, &scrollback_rows);
        if (scrollback_rows == 0 or max_lines == 0) return try allocator.dupe(u8, "");

        const count = @min(scrollback_rows, max_lines);
        const start = scrollback_rows - count;
        return try self.collectText(allocator, c.POINT_TAG_HISTORY, start, count);
    }

    fn encodeKey(
        _: *NativeGhosttyTerminal,
        input: core.KeyInput,
        allocator: std.mem.Allocator,
    ) core.TerminalError![]u8 {
        return encodeKeyInput(input, allocator);
    }

    fn encodePaste(
        _: *NativeGhosttyTerminal,
        bytes: []const u8,
        allocator: std.mem.Allocator,
    ) core.TerminalError![]u8 {
        return try std.mem.concat(allocator, u8, &.{ "\x1b[200~", bytes, "\x1b[201~" });
    }

    fn collectText(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
        tag: c_int,
        start: usize,
        count: usize,
    ) core.TerminalError![]u8 {
        var lines: std.ArrayList([]u8) = .empty;
        defer {
            for (lines.items) |line| allocator.free(line);
            lines.deinit(allocator);
        }

        var last_non_empty: ?usize = null;
        var row_index: usize = 0;
        while (row_index < count) : (row_index += 1) {
            const line = try self.rowText(allocator, tag, start + row_index);
            if (line.len > 0) last_non_empty = lines.items.len;
            try lines.append(allocator, line);
        }

        const last = last_non_empty orelse return try allocator.dupe(u8, "");
        var out: std.ArrayList(u8) = .empty;
        errdefer out.deinit(allocator);

        for (lines.items[0 .. last + 1], 0..) |line, idx| {
            if (idx != 0) try out.append(allocator, '\n');
            try out.appendSlice(allocator, line);
        }

        return try out.toOwnedSlice(allocator);
    }

    fn rowText(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
        tag: c_int,
        y: usize,
    ) core.TerminalError![]u8 {
        const cols = try self.getU16(c.TERMINAL_DATA_COLS);
        var out: std.ArrayList(u8) = .empty;
        defer out.deinit(allocator);

        var x: u16 = 0;
        while (x < cols) : (x += 1) {
            const text = try self.cellTextOwned(allocator, tag, x, y);
            defer allocator.free(text);
            if (text.len == 0) {
                try out.append(allocator, ' ');
            } else {
                try out.appendSlice(allocator, text);
            }
        }

        return try allocator.dupe(u8, std.mem.trimRight(u8, out.items, " "));
    }

    fn cellAt(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
        tag: c_int,
        x: u16,
        y: usize,
    ) core.TerminalError!?core.Cell {
        var ref = c.GridRef.init();
        const point = c.Point.coordinate(tag, x, y);
        try resultToError(self.lib.terminal_grid_ref(self.terminal, point, &ref));

        var cell: c.Cell = 0;
        try resultToError(self.lib.grid_ref_cell(&ref, &cell));

        var style = c.Style.init();
        try resultToError(self.lib.grid_ref_style(&ref, &style));

        var has_text = false;
        try resultToError(self.lib.cell_get(cell, c.CELL_DATA_HAS_TEXT, &has_text));
        if (!has_text and nativeStyleIsDefault(style)) return null;

        const text = try self.cellTextFromRef(allocator, &ref, cell);
        errdefer allocator.free(text);

        return .{
            .x = x,
            .y = @as(u16, @intCast(y)),
            .text = text,
            .style = nativeStyleToCore(style),
        };
    }

    fn cellTextOwned(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
        tag: c_int,
        x: u16,
        y: usize,
    ) core.TerminalError![]u8 {
        var ref = c.GridRef.init();
        const point = c.Point.coordinate(tag, x, y);
        const ref_result = self.lib.terminal_grid_ref(self.terminal, point, &ref);
        if (ref_result == c.RESULT_INVALID_VALUE or ref_result == c.RESULT_NO_VALUE) {
            return try allocator.dupe(u8, "");
        }
        try resultToError(ref_result);

        var cell: c.Cell = 0;
        try resultToError(self.lib.grid_ref_cell(&ref, &cell));

        var has_text = false;
        try resultToError(self.lib.cell_get(cell, c.CELL_DATA_HAS_TEXT, &has_text));
        if (!has_text) return try allocator.dupe(u8, "");

        return try self.cellTextFromRef(allocator, &ref, cell);
    }

    fn cellTextFromRef(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
        ref: *const c.GridRef,
        cell: c.Cell,
    ) core.TerminalError![]u8 {
        var needed: usize = 0;
        const first = self.lib.grid_ref_graphemes(ref, null, 0, &needed);
        if (first != c.RESULT_SUCCESS and first != c.RESULT_OUT_OF_SPACE) {
            try resultToError(first);
        }

        var out: std.ArrayList(u8) = .empty;
        errdefer out.deinit(allocator);

        if (needed > 0) {
            const codepoints = try allocator.alloc(u32, needed);
            defer allocator.free(codepoints);
            try resultToError(self.lib.grid_ref_graphemes(ref, codepoints.ptr, codepoints.len, &needed));
            for (codepoints[0..needed]) |codepoint| try appendCodepoint(allocator, &out, codepoint);
        } else {
            var codepoint: u32 = 0;
            try resultToError(self.lib.cell_get(cell, c.CELL_DATA_CODEPOINT, &codepoint));
            try appendCodepoint(allocator, &out, codepoint);
        }

        return try out.toOwnedSlice(allocator);
    }

    fn optionalTitleOwned(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
    ) core.TerminalError!?[]const u8 {
        var title: c.String = undefined;
        try self.get(c.TERMINAL_DATA_TITLE, &title);
        if (title.len == 0) return null;
        return try allocator.dupe(u8, title.ptr[0..title.len]);
    }

    fn titleOwned(
        self: *NativeGhosttyTerminal,
        allocator: std.mem.Allocator,
    ) core.TerminalError![]const u8 {
        var title: c.String = undefined;
        try self.get(c.TERMINAL_DATA_TITLE, &title);
        if (title.len == 0) return try allocator.dupe(u8, "");
        return try allocator.dupe(u8, title.ptr[0..title.len]);
    }

    fn getU16(self: *NativeGhosttyTerminal, data: c_int) core.TerminalError!u16 {
        var value: u16 = 0;
        try self.get(data, &value);
        return value;
    }

    fn getBool(self: *NativeGhosttyTerminal, data: c_int) core.TerminalError!bool {
        var value = false;
        try self.get(data, &value);
        return value;
    }

    fn get(self: *NativeGhosttyTerminal, data: c_int, out: anytype) core.TerminalError!void {
        try resultToError(self.lib.terminal_get(self.terminal, data, @ptrCast(out)));
    }
};

pub const Backend = enum {
    auto,
    compat,
    native,
};

pub const ManagedOptions = struct {
    backend: Backend = .auto,
    native_library_path: ?[]const u8 = null,
    terminal: Options = .{},
};

pub const ManagedTerminal = union(enum) {
    compat: GhosttyTerminal,
    native: NativeGhosttyTerminal,

    pub fn init(
        allocator: std.mem.Allocator,
        options: ManagedOptions,
    ) NativeGhosttyError!ManagedTerminal {
        switch (options.backend) {
            .compat => return .{ .compat = try GhosttyTerminal.init(allocator, options.terminal) },
            .native => {
                const path = options.native_library_path orelse return error.BackendFailure;
                return .{ .native = try NativeGhosttyTerminal.initFromLibraryPath(allocator, path, options.terminal) };
            },
            .auto => {
                if (!builtin.link_libc) {
                    return .{ .compat = try GhosttyTerminal.init(allocator, options.terminal) };
                }

                if (options.native_library_path) |path| {
                    return .{ .native = try NativeGhosttyTerminal.initFromLibraryPath(allocator, path, options.terminal) };
                }

                const env_path = std.process.getEnvVarOwned(allocator, "MITOS_LIBGHOSTTY_VT") catch |err| switch (err) {
                    error.EnvironmentVariableNotFound => null,
                    error.OutOfMemory => return error.OutOfMemory,
                    else => return error.BackendFailure,
                };
                if (env_path) |path| {
                    defer allocator.free(path);
                    return .{ .native = try NativeGhosttyTerminal.initFromLibraryPath(allocator, path, options.terminal) };
                }

                return .{ .compat = try GhosttyTerminal.init(allocator, options.terminal) };
            },
        }
    }

    pub fn deinit(self: *ManagedTerminal) void {
        switch (self.*) {
            .compat => |*terminal| terminal.deinit(),
            .native => |*terminal| terminal.deinit(),
        }
        self.* = undefined;
    }

    pub fn asCore(self: *ManagedTerminal) core.TerminalCore {
        return switch (self.*) {
            .compat => |*terminal| terminal.asCore(),
            .native => |*terminal| terminal.asCore(),
        };
    }
};

fn writeFromPtyAdapter(
    terminal: *core.TerminalCore,
    bytes: []const u8,
    allocator: std.mem.Allocator,
) core.TerminalError!core.TerminalEffects {
    return try fromCore(terminal).writeFromPty(bytes, allocator);
}

fn resizeAdapter(
    terminal: *core.TerminalCore,
    cols: u16,
    rows: u16,
    cell_size: core.CellSize,
) core.TerminalError!void {
    try fromCore(terminal).resize(cols, rows, cell_size);
}

fn snapshotAdapter(
    terminal: *core.TerminalCore,
    allocator: std.mem.Allocator,
) core.TerminalError!core.ScreenSnapshot {
    return try fromCore(terminal).snapshot(allocator);
}

fn visibleTextAdapter(
    terminal: *core.TerminalCore,
    allocator: std.mem.Allocator,
) core.TerminalError![]u8 {
    return try fromCore(terminal).visibleText(allocator);
}

fn scrollbackTextAdapter(
    terminal: *core.TerminalCore,
    allocator: std.mem.Allocator,
    max_lines: usize,
) core.TerminalError![]u8 {
    return try fromCore(terminal).scrollbackText(allocator, max_lines);
}

fn encodeKeyAdapter(
    terminal: *core.TerminalCore,
    input: core.KeyInput,
    allocator: std.mem.Allocator,
) core.TerminalError![]u8 {
    return try fromCore(terminal).encodeKey(input, allocator);
}

fn encodePasteAdapter(
    terminal: *core.TerminalCore,
    bytes: []const u8,
    allocator: std.mem.Allocator,
) core.TerminalError![]u8 {
    return try fromCore(terminal).encodePaste(bytes, allocator);
}

fn fromCore(terminal: *core.TerminalCore) *GhosttyTerminal {
    return @ptrCast(@alignCast(terminal.context));
}

fn nativeWriteFromPtyAdapter(
    terminal: *core.TerminalCore,
    bytes: []const u8,
    allocator: std.mem.Allocator,
) core.TerminalError!core.TerminalEffects {
    return try fromNativeCore(terminal).writeFromPty(bytes, allocator);
}

fn nativeResizeAdapter(
    terminal: *core.TerminalCore,
    cols: u16,
    rows: u16,
    cell_size: core.CellSize,
) core.TerminalError!void {
    try fromNativeCore(terminal).resize(cols, rows, cell_size);
}

fn nativeSnapshotAdapter(
    terminal: *core.TerminalCore,
    allocator: std.mem.Allocator,
) core.TerminalError!core.ScreenSnapshot {
    return try fromNativeCore(terminal).snapshot(allocator);
}

fn nativeVisibleTextAdapter(
    terminal: *core.TerminalCore,
    allocator: std.mem.Allocator,
) core.TerminalError![]u8 {
    return try fromNativeCore(terminal).visibleText(allocator);
}

fn nativeScrollbackTextAdapter(
    terminal: *core.TerminalCore,
    allocator: std.mem.Allocator,
    max_lines: usize,
) core.TerminalError![]u8 {
    return try fromNativeCore(terminal).scrollbackText(allocator, max_lines);
}

fn nativeEncodeKeyAdapter(
    terminal: *core.TerminalCore,
    input: core.KeyInput,
    allocator: std.mem.Allocator,
) core.TerminalError![]u8 {
    return try fromNativeCore(terminal).encodeKey(input, allocator);
}

fn nativeEncodePasteAdapter(
    terminal: *core.TerminalCore,
    bytes: []const u8,
    allocator: std.mem.Allocator,
) core.TerminalError![]u8 {
    return try fromNativeCore(terminal).encodePaste(bytes, allocator);
}

fn fromNativeCore(terminal: *core.TerminalCore) *NativeGhosttyTerminal {
    return @ptrCast(@alignCast(terminal.context));
}

fn cellCount(cols: u16, rows: u16) usize {
    return @as(usize, cols) * @as(usize, rows);
}

fn clearCellSlice(cells: []CellState) void {
    for (cells) |*cell| cell.* = .{};
}

const OscEnd = struct {
    len: usize,
    consumed: usize,
};

fn findOscEnd(bytes: []const u8) ?OscEnd {
    var i: usize = 0;
    while (i < bytes.len) : (i += 1) {
        if (bytes[i] == 0x07) return .{ .len = i, .consumed = i + 1 };
        if (bytes[i] == 0x1b and i + 1 < bytes.len and bytes[i + 1] == '\\') {
            return .{ .len = i, .consumed = i + 2 };
        }
    }
    return null;
}

fn findCsiEnd(bytes: []const u8) ?usize {
    for (bytes, 0..) |byte, idx| {
        if (byte >= 0x40 and byte <= 0x7e) return idx;
    }
    return null;
}

const ParsedParams = struct {
    items: [32]i32 = [_]i32{0} ** 32,
    len: usize = 0,
};

fn parseParams(bytes: []const u8) ParsedParams {
    var parsed: ParsedParams = .{};
    if (bytes.len == 0) {
        parsed.items[0] = 0;
        parsed.len = 1;
        return parsed;
    }

    var it = std.mem.splitScalar(u8, bytes, ';');
    while (it.next()) |part| {
        if (parsed.len == parsed.items.len) break;
        parsed.items[parsed.len] = if (part.len == 0) 0 else std.fmt.parseInt(i32, part, 10) catch 0;
        parsed.len += 1;
    }
    return parsed;
}

fn paramOr(params: *const ParsedParams, index_: usize, default: i32) i32 {
    if (index_ >= params.len) return default;
    return if (params.items[index_] == 0) default else params.items[index_];
}

fn clampOneBased(value: i32, max: u16) u16 {
    const normalized = @max(1, value) - 1;
    return @as(u16, @intCast(@min(@as(i32, max - 1), normalized)));
}

fn boundedAmount(value: i32, max: u16) u16 {
    return @as(u16, @intCast(@min(@as(i32, max), @max(0, value))));
}

fn setColor(style: *core.CellStyle, is_fg: bool, color: core.Color) void {
    if (is_fg) {
        style.fg = color;
    } else {
        style.bg = color;
    }
}

fn utf8SequenceLen(bytes: []const u8) core.TerminalError!usize {
    const len = std.unicode.utf8ByteSequenceLength(bytes[0]) catch 1;
    if (len > bytes.len) return error.UnsupportedInput;
    return len;
}

fn isCombiningMark(codepoint: u21) bool {
    return (codepoint >= 0x0300 and codepoint <= 0x036f) or
        (codepoint >= 0x1ab0 and codepoint <= 0x1aff) or
        (codepoint >= 0x1dc0 and codepoint <= 0x1dff) or
        (codepoint >= 0x20d0 and codepoint <= 0x20ff) or
        (codepoint >= 0xfe20 and codepoint <= 0xfe2f);
}

fn encodeKeyInput(input: core.KeyInput, allocator: std.mem.Allocator) core.TerminalError![]u8 {
    switch (input) {
        .text => |text| return try allocator.dupe(u8, text),
        .special => |special| {
            if (modifierCode(special.modifiers)) |code| {
                const final: u8 = switch (special.key) {
                    .up => 'A',
                    .down => 'B',
                    .right => 'C',
                    .left => 'D',
                    .home => 'H',
                    .end => 'F',
                    else => return try encodeUnmodifiedSpecial(special.key, allocator),
                };
                return try std.fmt.allocPrint(allocator, "\x1b[1;{}{c}", .{ code, final });
            }
            return try encodeUnmodifiedSpecial(special.key, allocator);
        },
    }
}

fn encodeUnmodifiedSpecial(key: core.SpecialKey, allocator: std.mem.Allocator) core.TerminalError![]u8 {
    const bytes = switch (key) {
        .enter => "\r",
        .escape => "\x1b",
        .backspace => "\x7f",
        .tab => "\t",
        .up => "\x1b[A",
        .down => "\x1b[B",
        .right => "\x1b[C",
        .left => "\x1b[D",
        .home => "\x1b[H",
        .end => "\x1b[F",
        .page_up => "\x1b[5~",
        .page_down => "\x1b[6~",
        .delete => "\x1b[3~",
        .insert => "\x1b[2~",
    };
    return try allocator.dupe(u8, bytes);
}

fn modifierCode(modifiers: core.KeyModifiers) ?u8 {
    var code: u8 = 1;
    if (modifiers.shift) code += 1;
    if (modifiers.alt) code += 2;
    if (modifiers.ctrl) code += 4;
    if (modifiers.super) code += 8;
    return if (code == 1) null else code;
}

const LibGhostty = struct {
    dyn: std.DynLib,
    terminal_new: TerminalNewFn,
    terminal_free: TerminalFreeFn,
    terminal_resize: TerminalResizeFn,
    terminal_set: TerminalSetFn,
    terminal_vt_write: TerminalVtWriteFn,
    terminal_get: TerminalGetFn,
    terminal_grid_ref: TerminalGridRefFn,
    grid_ref_cell: GridRefCellFn,
    grid_ref_style: GridRefStyleFn,
    grid_ref_graphemes: GridRefGraphemesFn,
    cell_get: CellGetFn,

    const TerminalNewFn = *const fn (?*const c.Allocator, *c.Terminal, c.TerminalOptions) callconv(.c) c_int;
    const TerminalFreeFn = *const fn (c.Terminal) callconv(.c) void;
    const TerminalResizeFn = *const fn (c.Terminal, u16, u16, u32, u32) callconv(.c) c_int;
    const TerminalSetFn = *const fn (c.Terminal, c_int, ?*const anyopaque) callconv(.c) c_int;
    const TerminalVtWriteFn = *const fn (c.Terminal, [*]const u8, usize) callconv(.c) void;
    const TerminalGetFn = *const fn (c.Terminal, c_int, *anyopaque) callconv(.c) c_int;
    const TerminalGridRefFn = *const fn (c.Terminal, c.Point, *c.GridRef) callconv(.c) c_int;
    const GridRefCellFn = *const fn (*const c.GridRef, *c.Cell) callconv(.c) c_int;
    const GridRefStyleFn = *const fn (*const c.GridRef, *c.Style) callconv(.c) c_int;
    const GridRefGraphemesFn = *const fn (*const c.GridRef, ?[*]u32, usize, *usize) callconv(.c) c_int;
    const CellGetFn = *const fn (c.Cell, c_int, *anyopaque) callconv(.c) c_int;

    fn open(path: []const u8) NativeGhosttyError!LibGhostty {
        var dyn = try std.DynLib.open(path);
        errdefer dyn.close();

        return .{
            .dyn = dyn,
            .terminal_new = try lookup(&dyn, TerminalNewFn, "ghostty_terminal_new"),
            .terminal_free = try lookup(&dyn, TerminalFreeFn, "ghostty_terminal_free"),
            .terminal_resize = try lookup(&dyn, TerminalResizeFn, "ghostty_terminal_resize"),
            .terminal_set = try lookup(&dyn, TerminalSetFn, "ghostty_terminal_set"),
            .terminal_vt_write = try lookup(&dyn, TerminalVtWriteFn, "ghostty_terminal_vt_write"),
            .terminal_get = try lookup(&dyn, TerminalGetFn, "ghostty_terminal_get"),
            .terminal_grid_ref = try lookup(&dyn, TerminalGridRefFn, "ghostty_terminal_grid_ref"),
            .grid_ref_cell = try lookup(&dyn, GridRefCellFn, "ghostty_grid_ref_cell"),
            .grid_ref_style = try lookup(&dyn, GridRefStyleFn, "ghostty_grid_ref_style"),
            .grid_ref_graphemes = try lookup(&dyn, GridRefGraphemesFn, "ghostty_grid_ref_graphemes"),
            .cell_get = try lookup(&dyn, CellGetFn, "ghostty_cell_get"),
        };
    }

    fn close(self: *LibGhostty) void {
        self.dyn.close();
    }

    fn lookup(dyn: *std.DynLib, comptime T: type, name: [:0]const u8) core.TerminalError!T {
        return dyn.lookup(T, name) orelse error.BackendFailure;
    }
};

const CallbackBridge = struct {
    state: ?*CallbackState = null,
};

pub const native_allocator_vtable: c.AllocatorVTable = .{
    .alloc = nativeAlloc,
    .resize = nativeResize,
    .remap = nativeRemap,
    .free = nativeFree,
};

fn nativeAlloc(
    ctx: *anyopaque,
    len: usize,
    alignment: u8,
    ret_addr: usize,
) callconv(.c) ?[*]u8 {
    const allocator: *const std.mem.Allocator = @ptrCast(@alignCast(ctx));
    return allocator.vtable.alloc(
        allocator.ptr,
        len,
        @enumFromInt(alignment),
        ret_addr,
    );
}

fn nativeResize(
    ctx: *anyopaque,
    memory: [*]u8,
    memory_len: usize,
    alignment: u8,
    new_len: usize,
    ret_addr: usize,
) callconv(.c) bool {
    const allocator: *const std.mem.Allocator = @ptrCast(@alignCast(ctx));
    return allocator.vtable.resize(
        allocator.ptr,
        memory[0..memory_len],
        @enumFromInt(alignment),
        new_len,
        ret_addr,
    );
}

fn nativeRemap(
    ctx: *anyopaque,
    memory: [*]u8,
    memory_len: usize,
    alignment: u8,
    new_len: usize,
    ret_addr: usize,
) callconv(.c) ?[*]u8 {
    const allocator: *const std.mem.Allocator = @ptrCast(@alignCast(ctx));
    return allocator.vtable.remap(
        allocator.ptr,
        memory[0..memory_len],
        @enumFromInt(alignment),
        new_len,
        ret_addr,
    );
}

fn nativeFree(
    ctx: *anyopaque,
    memory: [*]u8,
    memory_len: usize,
    alignment: u8,
    ret_addr: usize,
) callconv(.c) void {
    const allocator: *const std.mem.Allocator = @ptrCast(@alignCast(ctx));
    allocator.vtable.free(
        allocator.ptr,
        memory[0..memory_len],
        @enumFromInt(alignment),
        ret_addr,
    );
}

const CallbackState = struct {
    allocator: std.mem.Allocator,
    effects: std.ArrayList(core.TerminalEffect) = .empty,
    failed: ?core.TerminalError = null,
    title_changed: bool = false,

    fn deinit(self: *CallbackState) void {
        for (self.effects.items) |effect| effect.deinit(self.allocator);
        self.effects.deinit(self.allocator);
    }

    fn append(self: *CallbackState, effect: core.TerminalEffect) void {
        if (self.failed != null) {
            var owned_effect = effect;
            owned_effect.deinit(self.allocator);
            return;
        }
        self.effects.append(self.allocator, effect) catch |err| {
            self.failed = switch (err) {
                error.OutOfMemory => error.OutOfMemory,
            };
            var owned_effect = effect;
            owned_effect.deinit(self.allocator);
        };
    }
};

fn nativeWritePtyCallback(
    _: c.Terminal,
    userdata: ?*anyopaque,
    data: [*]const u8,
    len: usize,
) callconv(.c) void {
    const state = callbackState(userdata) orelse return;
    const bytes = state.allocator.dupe(u8, data[0..len]) catch {
        state.failed = error.OutOfMemory;
        return;
    };
    state.append(.{ .write_to_pty = bytes });
}

fn nativeBellCallback(_: c.Terminal, userdata: ?*anyopaque) callconv(.c) void {
    const state = callbackState(userdata) orelse return;
    state.append(.bell);
}

fn nativeTitleChangedCallback(_: c.Terminal, userdata: ?*anyopaque) callconv(.c) void {
    const state = callbackState(userdata) orelse return;
    state.title_changed = true;
}

fn callbackState(userdata: ?*anyopaque) ?*CallbackState {
    const bridge: *CallbackBridge = @ptrCast(@alignCast(userdata orelse return null));
    return bridge.state;
}

fn resultToError(result: c_int) core.TerminalError!void {
    switch (result) {
        c.RESULT_SUCCESS, c.RESULT_NO_VALUE => {},
        c.RESULT_OUT_OF_MEMORY => return error.OutOfMemory,
        c.RESULT_INVALID_VALUE => return error.UnsupportedInput,
        c.RESULT_OUT_OF_SPACE => return error.BackendFailure,
        else => return error.BackendFailure,
    }
}

fn appendCodepoint(
    allocator: std.mem.Allocator,
    out: *std.ArrayList(u8),
    raw_codepoint: u32,
) core.TerminalError!void {
    if (raw_codepoint == 0) return;

    const codepoint: u21 = if (raw_codepoint <= 0x10ffff)
        @intCast(raw_codepoint)
    else
        0xfffd;
    var buffer: [4]u8 = undefined;
    const len = std.unicode.utf8Encode(codepoint, &buffer) catch
        std.unicode.utf8Encode(0xfffd, &buffer) catch unreachable;
    try out.appendSlice(allocator, buffer[0..len]);
}

fn nativeStyleToCore(style: c.Style) core.CellStyle {
    return .{
        .bold = style.bold,
        .italic = style.italic,
        .underline = style.underline != 0,
        .inverse = style.inverse,
        .fg = nativeColorToCore(style.fg_color),
        .bg = nativeColorToCore(style.bg_color),
    };
}

fn nativeColorToCore(color: c.StyleColor) ?core.Color {
    return switch (color.tag) {
        c.STYLE_COLOR_PALETTE => .{ .indexed = color.value.palette },
        c.STYLE_COLOR_RGB => .{ .rgb = .{
            .r = color.value.rgb.r,
            .g = color.value.rgb.g,
            .b = color.value.rgb.b,
        } },
        else => null,
    };
}

fn nativeStyleIsDefault(style: c.Style) bool {
    return style.fg_color.tag == c.STYLE_COLOR_NONE and
        style.bg_color.tag == c.STYLE_COLOR_NONE and
        style.underline_color.tag == c.STYLE_COLOR_NONE and
        !style.bold and
        !style.italic and
        !style.faint and
        !style.blink and
        !style.inverse and
        !style.invisible and
        !style.strikethrough and
        !style.overline and
        style.underline == 0;
}

const c = struct {
    const RESULT_SUCCESS: c_int = 0;
    const RESULT_OUT_OF_MEMORY: c_int = -1;
    const RESULT_INVALID_VALUE: c_int = -2;
    const RESULT_OUT_OF_SPACE: c_int = -3;
    const RESULT_NO_VALUE: c_int = -4;

    const TERMINAL_OPT_USERDATA: c_int = 0;
    const TERMINAL_OPT_WRITE_PTY: c_int = 1;
    const TERMINAL_OPT_BELL: c_int = 2;
    const TERMINAL_OPT_TITLE_CHANGED: c_int = 5;

    const TERMINAL_DATA_COLS: c_int = 1;
    const TERMINAL_DATA_ROWS: c_int = 2;
    const TERMINAL_DATA_CURSOR_X: c_int = 3;
    const TERMINAL_DATA_CURSOR_Y: c_int = 4;
    const TERMINAL_DATA_ACTIVE_SCREEN: c_int = 6;
    const TERMINAL_DATA_CURSOR_VISIBLE: c_int = 7;
    const TERMINAL_DATA_TITLE: c_int = 12;
    const TERMINAL_DATA_SCROLLBACK_ROWS: c_int = 15;

    const TERMINAL_SCREEN_ALTERNATE: c_int = 1;

    const POINT_TAG_VIEWPORT: c_int = 1;
    const POINT_TAG_HISTORY: c_int = 3;

    const CELL_DATA_CODEPOINT: c_int = 1;
    const CELL_DATA_HAS_TEXT: c_int = 4;

    const STYLE_COLOR_NONE: c_int = 0;
    const STYLE_COLOR_PALETTE: c_int = 1;
    const STYLE_COLOR_RGB: c_int = 2;

    const Terminal = ?*anyopaque;
    const Cell = u64;

    pub const AllocatorVTable = extern struct {
        alloc: *const fn (*anyopaque, usize, u8, usize) callconv(.c) ?[*]u8,
        resize: *const fn (*anyopaque, [*]u8, usize, u8, usize, usize) callconv(.c) bool,
        remap: *const fn (*anyopaque, [*]u8, usize, u8, usize, usize) callconv(.c) ?[*]u8,
        free: *const fn (*anyopaque, [*]u8, usize, u8, usize) callconv(.c) void,
    };

    pub const Allocator = extern struct {
        ctx: *anyopaque,
        vtable: *const AllocatorVTable,
    };

    const TerminalOptions = extern struct {
        cols: u16,
        rows: u16,
        max_scrollback: usize,
    };

    const String = extern struct {
        ptr: [*]const u8,
        len: usize,
    };

    const Rgb = extern struct {
        r: u8,
        g: u8,
        b: u8,
    };

    const PointCoordinate = extern struct {
        x: u16,
        y: u32,
    };

    const PointValue = extern union {
        coordinate: PointCoordinate,
        _padding: [2]u64,
    };

    const Point = extern struct {
        tag: c_int,
        value: PointValue,

        fn coordinate(tag: c_int, x: u16, y: usize) Point {
            return .{
                .tag = tag,
                .value = .{ .coordinate = .{
                    .x = x,
                    .y = @as(u32, @intCast(@min(y, std.math.maxInt(u32)))),
                } },
            };
        }
    };

    const GridRef = extern struct {
        size: usize,
        node: ?*anyopaque,
        x: u16,
        y: u16,

        fn init() GridRef {
            return .{
                .size = @sizeOf(GridRef),
                .node = null,
                .x = 0,
                .y = 0,
            };
        }
    };

    const StyleColorValue = extern union {
        palette: u8,
        rgb: Rgb,
        _padding: u64,
    };

    const StyleColor = extern struct {
        tag: c_int,
        value: StyleColorValue,
    };

    const Style = extern struct {
        size: usize,
        fg_color: StyleColor,
        bg_color: StyleColor,
        underline_color: StyleColor,
        bold: bool,
        italic: bool,
        faint: bool,
        blink: bool,
        inverse: bool,
        invisible: bool,
        strikethrough: bool,
        overline: bool,
        underline: c_int,

        fn init() Style {
            return .{
                .size = @sizeOf(Style),
                .fg_color = .{ .tag = STYLE_COLOR_NONE, .value = .{ ._padding = 0 } },
                .bg_color = .{ .tag = STYLE_COLOR_NONE, .value = .{ ._padding = 0 } },
                .underline_color = .{ .tag = STYLE_COLOR_NONE, .value = .{ ._padding = 0 } },
                .bold = false,
                .italic = false,
                .faint = false,
                .blink = false,
                .inverse = false,
                .invisible = false,
                .strikethrough = false,
                .overline = false,
                .underline = 0,
            };
        }
    };
};

fn writeAll(term_core: *core.TerminalCore, bytes: []const u8) !void {
    const effects = try term_core.write_from_pty(term_core, bytes, std.testing.allocator);
    effects.deinit(std.testing.allocator);
}

test "ansi colors are captured in snapshots" {
    var terminal = try GhosttyTerminal.init(std.testing.allocator, .{ .cols = 16, .rows = 4 });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "ok \x1b[1;31mred\x1b[0m");
    const shot = try term_core.snapshot(&term_core, std.testing.allocator);
    defer shot.deinit(std.testing.allocator);

    var red_cell: ?core.Cell = null;
    for (shot.cells) |cell| {
        if (cell.x == 3 and cell.y == 0) red_cell = cell;
    }

    try std.testing.expect(red_cell != null);
    try std.testing.expectEqualStrings("r", red_cell.?.text);
    try std.testing.expect(red_cell.?.style.bold);
    try std.testing.expectEqual(@as(u8, 1), red_cell.?.style.fg.?.indexed);
}

test "managed terminal explicit compatibility backend exposes terminal core" {
    var terminal = try ManagedTerminal.init(std.testing.allocator, .{
        .backend = .compat,
        .terminal = .{ .cols = 12, .rows = 4 },
    });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "managed");
    const text = try term_core.visible_text(&term_core, std.testing.allocator);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("managed", text);
}

test "native libghostty C ABI adapter renders text when library is available" {
    const allocator = std.testing.allocator;
    const library_path = std.process.getEnvVarOwned(allocator, "MITOS_LIBGHOSTTY_VT") catch |err| switch (err) {
        error.EnvironmentVariableNotFound => return error.SkipZigTest,
        else => return err,
    };
    defer allocator.free(library_path);

    var terminal = NativeGhosttyTerminal.initFromLibraryPath(allocator, library_path, .{ .cols = 16, .rows = 4 }) catch |err| switch (err) {
        error.BackendFailure => return error.SkipZigTest,
        else => return err,
    };
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "ok \x1b[31mred\x1b[0m");
    const text = try term_core.visible_text(&term_core, allocator);
    defer allocator.free(text);
    try std.testing.expectEqualStrings("ok red", text);

    const shot = try term_core.snapshot(&term_core, allocator);
    defer shot.deinit(allocator);

    var red_cell: ?core.Cell = null;
    for (shot.cells) |cell| {
        if (cell.x == 3 and cell.y == 0) red_cell = cell;
    }

    try std.testing.expect(red_cell != null);
    try std.testing.expectEqual(@as(u8, 1), red_cell.?.style.fg.?.indexed);
}

test "cursor movement overwrites existing cells" {
    var terminal = try GhosttyTerminal.init(std.testing.allocator, .{ .cols = 8, .rows = 3 });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "abc\x1b[2DXY");
    const text = try term_core.visible_text(&term_core, std.testing.allocator);
    defer std.testing.allocator.free(text);

    try std.testing.expectEqualStrings("aXY", text);
}

test "alternate screen keeps main screen intact" {
    var terminal = try GhosttyTerminal.init(std.testing.allocator, .{ .cols = 12, .rows = 4 });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "main");
    try writeAll(&term_core, "\x1b[?1049halt");

    const alt_text = try term_core.visible_text(&term_core, std.testing.allocator);
    defer std.testing.allocator.free(alt_text);
    try std.testing.expectEqualStrings("alt", alt_text);

    try writeAll(&term_core, "\x1b[?1049l");
    const main_text = try term_core.visible_text(&term_core, std.testing.allocator);
    defer std.testing.allocator.free(main_text);
    try std.testing.expectEqualStrings("main", main_text);
}

test "unicode and combining marks stay in one visible cell" {
    var terminal = try GhosttyTerminal.init(std.testing.allocator, .{ .cols = 12, .rows = 4 });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "lambda:\xce\xbb e\xcc\x81");
    const shot = try term_core.snapshot(&term_core, std.testing.allocator);
    defer shot.deinit(std.testing.allocator);

    var found_combined = false;
    for (shot.cells) |cell| {
        if (std.mem.eql(u8, cell.text, "e\xcc\x81")) found_combined = true;
    }

    try std.testing.expect(found_combined);
}

test "approval-like screens produce deterministic visible text" {
    var terminal = try GhosttyTerminal.init(std.testing.allocator, .{ .cols = 40, .rows = 8 });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "Run npm test?\r\n\x1b[1;31mApproval required\x1b[0m\r\n[y/N]");
    const text = try term_core.visible_text(&term_core, std.testing.allocator);
    defer std.testing.allocator.free(text);

    try std.testing.expectEqualStrings("Run npm test?\nApproval required\n[y/N]", text);
}

test "terminal fixture approval screen matches golden visible text" {
    const allocator = std.testing.allocator;
    const input = try readHexFixture(allocator, "test/fixtures/terminal/approval_screen.hex");
    defer allocator.free(input);
    const expected = try std.fs.cwd().readFileAlloc(allocator, "test/fixtures/terminal/approval_screen.expected.txt", 4096);
    defer allocator.free(expected);

    var terminal = try GhosttyTerminal.init(allocator, .{ .cols = 40, .rows = 8 });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, input);
    const text = try term_core.visible_text(&term_core, allocator);
    defer allocator.free(text);

    try std.testing.expectEqualStrings(std.mem.trimRight(u8, expected, "\n"), text);
}

fn readHexFixture(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    const hex = try std.fs.cwd().readFileAlloc(allocator, path, 64 * 1024);
    defer allocator.free(hex);

    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(allocator);

    var digits: [2]u8 = undefined;
    var digit_count: usize = 0;
    for (hex) |byte| {
        if (std.ascii.isWhitespace(byte)) continue;
        digits[digit_count] = byte;
        digit_count += 1;
        if (digit_count == 2) {
            try out.append(allocator, try std.fmt.parseInt(u8, &digits, 16));
            digit_count = 0;
        }
    }

    if (digit_count != 0) return error.InvalidCharacter;
    return try out.toOwnedSlice(allocator);
}

test "resize validates dimensions and preserves visible text" {
    var terminal = try GhosttyTerminal.init(std.testing.allocator, .{ .cols = 20, .rows = 4 });
    defer terminal.deinit();
    var term_core = terminal.asCore();

    try writeAll(&term_core, "one\r\ntwo");
    try std.testing.expectError(
        error.InvalidSize,
        term_core.resize(&term_core, 0, 4, .{ .width_px = 8, .height_px = 16 }),
    );
    try term_core.resize(&term_core, 12, 4, .{ .width_px = 8, .height_px = 16 });

    const text = try term_core.visible_text(&term_core, std.testing.allocator);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("one\ntwo", text);
}

test "key and paste encoding use terminal-safe byte sequences" {
    var terminal = try GhosttyTerminal.init(std.testing.allocator, .{});
    defer terminal.deinit();
    var term_core = terminal.asCore();

    const up = try term_core.encode_key(&term_core, .{ .special = .{ .key = .up } }, std.testing.allocator);
    defer std.testing.allocator.free(up);
    try std.testing.expectEqualStrings("\x1b[A", up);

    const paste = try term_core.encode_paste(&term_core, "hello", std.testing.allocator);
    defer std.testing.allocator.free(paste);
    try std.testing.expectEqualStrings("\x1b[200~hello\x1b[201~", paste);
}
