//! Ground-truth progress extraction for interactive `claude` sessions.
//!
//! Claude Code appends every turn of a session to
//! `<config>/projects/<sanitized-cwd>/<session-id>.jsonl` while the TUI runs.
//! Steering a claude PTY therefore never needs to scrape the screen for
//! state: the PTY stays the *control* channel (keystrokes in), and this
//! module is the *observation* channel (structured turns out).
//!
//! The watcher is poll-based and incremental: discover the session file that
//! appeared after the agent was spawned, then tail complete JSONL lines as
//! they are flushed. Each line yields zero or more `LogEvent`s — assistant
//! text, tool invocations, tool results, user prompts — that the sidecar
//! forwards as `AGENT_LOG` events.
const std = @import("std");

/// Claude Code's default config dir name under `$HOME`.
pub const default_config_dir_name = ".claude";

/// Upper bound for one event's extracted text. Tool results can be megabytes;
/// the trace stores the full session file path for anyone who needs raw data.
pub const max_event_text_bytes: usize = 4096;

/// New session files must have an mtime no earlier than this many seconds
/// before the watcher started, so a stale session in the same project dir is
/// never mistaken for ours.
const discovery_slack_s: i64 = 5;

const max_line_bytes: usize = 4 * 1024 * 1024;

pub const LogEventKind = enum {
    user_prompt,
    assistant_text,
    tool_use,
    tool_result,
    error_reported,
};

pub const LogEvent = struct {
    kind: LogEventKind,
    /// Extracted text (assistant prose, tool input JSON, tool result body...).
    text: []u8,
    tool_name: ?[]u8 = null,
    is_error: bool = false,

    pub fn deinit(self: LogEvent, allocator: std.mem.Allocator) void {
        if (self.text.len > 0) allocator.free(self.text);
        if (self.tool_name) |name| allocator.free(name);
    }
};

pub fn freeEvents(allocator: std.mem.Allocator, events: []LogEvent) void {
    for (events) |event| event.deinit(allocator);
    if (events.len > 0) allocator.free(events);
}

/// Claude Code keys project log directories by the working directory with
/// every non-alphanumeric byte replaced by `-`:
/// `/media/data/proj/mitos` -> `-media-data-proj-mitos`.
pub fn sanitizeProjectSegmentAlloc(allocator: std.mem.Allocator, cwd: []const u8) ![]u8 {
    const out = try allocator.dupe(u8, cwd);
    for (out) |*byte| {
        if (!std.ascii.isAlphanumeric(byte.*)) byte.* = '-';
    }
    return out;
}

/// `<config_dir>/projects/<sanitized-cwd>`
pub fn projectLogDirAlloc(
    allocator: std.mem.Allocator,
    config_dir: []const u8,
    cwd: []const u8,
) ![]u8 {
    const segment = try sanitizeProjectSegmentAlloc(allocator, cwd);
    defer allocator.free(segment);
    return std.fs.path.join(allocator, &.{ config_dir, "projects", segment });
}

/// Parse one session-log line into owned events. Lines that are not valid
/// JSON objects, or that carry only UI bookkeeping (`mode`, `ai-title`,
/// `file-history-snapshot`, ...), yield an empty slice.
pub fn parseLineAlloc(allocator: std.mem.Allocator, line: []const u8) ![]LogEvent {
    const trimmed = std.mem.trim(u8, line, " \t\r\n");
    if (trimmed.len == 0) return &.{};

    var parsed = std.json.parseFromSlice(std.json.Value, allocator, trimmed, .{}) catch
        return &.{};
    defer parsed.deinit();
    if (parsed.value != .object) return &.{};
    const entry = parsed.value.object;

    const entry_type = stringMember(entry, "type") orelse return &.{};

    var events: std.ArrayList(LogEvent) = .empty;
    errdefer {
        for (events.items) |event| event.deinit(allocator);
        events.deinit(allocator);
    }

    if (std.mem.eql(u8, entry_type, "assistant")) {
        try collectAssistantEvents(allocator, &events, entry);
    } else if (std.mem.eql(u8, entry_type, "user")) {
        try collectUserEvents(allocator, &events, entry);
    } else if (std.mem.eql(u8, entry_type, "system")) {
        try collectSystemEvents(allocator, &events, entry);
    }

    return events.toOwnedSlice(allocator);
}

fn collectAssistantEvents(
    allocator: std.mem.Allocator,
    events: *std.ArrayList(LogEvent),
    entry: std.json.ObjectMap,
) !void {
    const blocks = messageContent(entry) orelse return;
    if (blocks != .array) return;

    for (blocks.array.items) |block| {
        if (block != .object) continue;
        const block_type = stringMember(block.object, "type") orelse continue;

        if (std.mem.eql(u8, block_type, "text")) {
            const text = stringMember(block.object, "text") orelse continue;
            if (std.mem.trim(u8, text, " \t\r\n").len == 0) continue;
            try events.append(allocator, .{
                .kind = .assistant_text,
                .text = try truncatedDupe(allocator, text),
            });
        } else if (std.mem.eql(u8, block_type, "tool_use")) {
            const name = stringMember(block.object, "name") orelse continue;
            const input_json = if (block.object.get("input")) |input|
                try stringifyTruncatedAlloc(allocator, input)
            else
                try allocator.dupe(u8, "{}");
            errdefer allocator.free(input_json);
            try events.append(allocator, .{
                .kind = .tool_use,
                .text = input_json,
                .tool_name = try allocator.dupe(u8, name),
            });
        }
    }
}

fn collectUserEvents(
    allocator: std.mem.Allocator,
    events: *std.ArrayList(LogEvent),
    entry: std.json.ObjectMap,
) !void {
    const content = messageContent(entry) orelse return;

    switch (content) {
        // Typed prompt: lets observers correlate steering input with turns.
        .string => |text| {
            if (std.mem.trim(u8, text, " \t\r\n").len == 0) return;
            try events.append(allocator, .{
                .kind = .user_prompt,
                .text = try truncatedDupe(allocator, text),
            });
        },
        .array => |blocks| {
            for (blocks.items) |block| {
                if (block != .object) continue;
                const block_type = stringMember(block.object, "type") orelse continue;
                if (!std.mem.eql(u8, block_type, "tool_result")) continue;

                const is_error = if (block.object.get("is_error")) |flag|
                    flag == .bool and flag.bool
                else
                    false;
                const body = try toolResultTextAlloc(allocator, block.object.get("content"));
                errdefer allocator.free(body);
                try events.append(allocator, .{
                    .kind = .tool_result,
                    .text = body,
                    .is_error = is_error,
                });
            }
        },
        else => {},
    }
}

fn collectSystemEvents(
    allocator: std.mem.Allocator,
    events: *std.ArrayList(LogEvent),
    entry: std.json.ObjectMap,
) !void {
    const content = stringMember(entry, "content") orelse return;
    const level = stringMember(entry, "level") orelse "";
    if (!std.mem.eql(u8, level, "error")) return;

    try events.append(allocator, .{
        .kind = .error_reported,
        .text = try truncatedDupe(allocator, content),
        .is_error = true,
    });
}

fn messageContent(entry: std.json.ObjectMap) ?std.json.Value {
    const message = entry.get("message") orelse return null;
    if (message != .object) return null;
    return message.object.get("content");
}

fn toolResultTextAlloc(allocator: std.mem.Allocator, content: ?std.json.Value) ![]u8 {
    const value = content orelse return allocator.dupe(u8, "");
    switch (value) {
        .string => |text| return truncatedDupe(allocator, text),
        .array => |blocks| {
            var out: std.ArrayList(u8) = .empty;
            defer out.deinit(allocator);
            for (blocks.items) |block| {
                if (block != .object) continue;
                const text = stringMember(block.object, "text") orelse continue;
                if (out.items.len > 0) try out.append(allocator, '\n');
                try out.appendSlice(allocator, text);
                if (out.items.len >= max_event_text_bytes) break;
            }
            const owned = try out.toOwnedSlice(allocator);
            if (owned.len <= max_event_text_bytes) return owned;
            defer allocator.free(owned);
            return truncatedDupe(allocator, owned);
        },
        else => return stringifyTruncatedAlloc(allocator, value),
    }
}

fn stringMember(object: std.json.ObjectMap, name: []const u8) ?[]const u8 {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .string => |text| text,
        else => null,
    };
}

fn truncatedDupe(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    if (text.len <= max_event_text_bytes) return allocator.dupe(u8, text);

    // Cut on a UTF-8 boundary so downstream JSON/msgpack encoding stays valid.
    var end = max_event_text_bytes;
    while (end > 0 and (text[end] & 0b1100_0000) == 0b1000_0000) end -= 1;
    return std.mem.concat(allocator, u8, &.{ text[0..end], "…[truncated]" });
}

fn stringifyTruncatedAlloc(allocator: std.mem.Allocator, value: std.json.Value) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    defer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer, .options = .{} };
    json.write(value) catch return allocator.dupe(u8, "");

    const rendered = out.writer.buffered();
    return truncatedDupe(allocator, rendered);
}

/// Incremental tail over the session log of one agent run.
pub const SessionLogWatcher = struct {
    allocator: std.mem.Allocator,
    project_dir: []u8,
    started_at_s: i64,
    file: ?std.fs.File = null,
    file_path: ?[]u8 = null,
    carry: std.ArrayList(u8) = .empty,

    pub fn init(
        allocator: std.mem.Allocator,
        config_dir: []const u8,
        project_cwd: []const u8,
        started_at_s: i64,
    ) !SessionLogWatcher {
        return .{
            .allocator = allocator,
            .project_dir = try projectLogDirAlloc(allocator, config_dir, project_cwd),
            .started_at_s = started_at_s,
        };
    }

    pub fn deinit(self: *SessionLogWatcher) void {
        if (self.file) |file| file.close();
        if (self.file_path) |path| self.allocator.free(path);
        self.carry.deinit(self.allocator);
        self.allocator.free(self.project_dir);
        self.* = undefined;
    }

    /// Absolute path of the discovered session file, once known.
    pub fn sessionPath(self: *const SessionLogWatcher) ?[]const u8 {
        return self.file_path;
    }

    /// Read everything appended since the last poll and parse it. Returns
    /// owned events (free with `freeEvents`). Before the agent has created
    /// its session file this returns an empty slice.
    pub fn poll(self: *SessionLogWatcher, allocator: std.mem.Allocator) ![]LogEvent {
        if (self.file == null) try self.discover();
        const file = self.file orelse return &.{};

        var buf: [32 * 1024]u8 = undefined;
        while (true) {
            const n = file.read(&buf) catch break;
            if (n == 0) break;
            try self.carry.appendSlice(self.allocator, buf[0..n]);
        }

        var events: std.ArrayList(LogEvent) = .empty;
        errdefer {
            for (events.items) |event| event.deinit(allocator);
            events.deinit(allocator);
        }

        var consumed: usize = 0;
        while (std.mem.indexOfScalarPos(u8, self.carry.items, consumed, '\n')) |newline| {
            const line = self.carry.items[consumed..newline];
            consumed = newline + 1;
            if (line.len > max_line_bytes) continue;

            const line_events = try parseLineAlloc(allocator, line);
            defer if (line_events.len > 0) allocator.free(line_events);
            try events.appendSlice(allocator, line_events);
        }
        if (consumed > 0) self.carry.replaceRangeAssumeCapacity(0, consumed, &.{});

        return events.toOwnedSlice(allocator);
    }

    fn discover(self: *SessionLogWatcher) !void {
        var dir = std.fs.cwd().openDir(self.project_dir, .{ .iterate = true }) catch return;
        defer dir.close();

        const threshold_ns: i128 =
            @as(i128, self.started_at_s - discovery_slack_s) * std.time.ns_per_s;
        var newest_ns: i128 = threshold_ns;
        var newest_name_buf: [std.fs.max_name_bytes]u8 = undefined;
        var newest_name: ?[]u8 = null;

        var it = dir.iterate();
        while (it.next() catch return) |entry| {
            if (entry.kind != .file) continue;
            if (!std.mem.endsWith(u8, entry.name, ".jsonl")) continue;
            const stat = dir.statFile(entry.name) catch continue;
            if (stat.mtime < newest_ns) continue;
            newest_ns = stat.mtime;
            @memcpy(newest_name_buf[0..entry.name.len], entry.name);
            newest_name = newest_name_buf[0..entry.name.len];
        }

        const name = newest_name orelse return;
        const path = try std.fs.path.join(self.allocator, &.{ self.project_dir, name });
        errdefer self.allocator.free(path);
        const file = dir.openFile(name, .{}) catch {
            self.allocator.free(path);
            return;
        };
        self.file = file;
        self.file_path = path;
    }
};

// -- tests --------------------------------------------------------------------

test "project segment sanitizing matches claude's directory naming" {
    const simple = try sanitizeProjectSegmentAlloc(std.testing.allocator, "/media/data/proj/mitos");
    defer std.testing.allocator.free(simple);
    try std.testing.expectEqualStrings("-media-data-proj-mitos", simple);

    const dotted = try sanitizeProjectSegmentAlloc(std.testing.allocator, "/a/b.c_d/.ralph");
    defer std.testing.allocator.free(dotted);
    try std.testing.expectEqualStrings("-a-b-c-d--ralph", dotted);
}

test "project log dir is rooted at <config>/projects" {
    const dir = try projectLogDirAlloc(std.testing.allocator, "/home/dev/.claude", "/work/repo");
    defer std.testing.allocator.free(dir);
    try std.testing.expectEqualStrings("/home/dev/.claude/projects/-work-repo", dir);
}

test "assistant line yields text and tool_use events" {
    const line =
        \\{"type":"assistant","message":{"role":"assistant","content":[
        \\{"type":"thinking","thinking":"hidden"},
        \\{"type":"text","text":"Running the tests now."},
        \\{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"zig build test"}}
        \\]}}
    ;
    const events = try parseLineAlloc(std.testing.allocator, line);
    defer freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 2), events.len);
    try std.testing.expectEqual(LogEventKind.assistant_text, events[0].kind);
    try std.testing.expectEqualStrings("Running the tests now.", events[0].text);
    try std.testing.expectEqual(LogEventKind.tool_use, events[1].kind);
    try std.testing.expectEqualStrings("Bash", events[1].tool_name.?);
    try std.testing.expect(std.mem.indexOf(u8, events[1].text, "zig build test") != null);
}

test "user line with typed prompt yields user_prompt" {
    const line =
        \\{"type":"user","message":{"role":"user","content":"please fix the flaky test"}}
    ;
    const events = try parseLineAlloc(std.testing.allocator, line);
    defer freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqual(LogEventKind.user_prompt, events[0].kind);
    try std.testing.expectEqualStrings("please fix the flaky test", events[0].text);
}

test "user line with tool_result yields tool_result with error flag" {
    const line =
        \\{"type":"user","message":{"role":"user","content":[
        \\{"type":"tool_result","tool_use_id":"toolu_1","content":"build failed","is_error":true}
        \\]}}
    ;
    const events = try parseLineAlloc(std.testing.allocator, line);
    defer freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqual(LogEventKind.tool_result, events[0].kind);
    try std.testing.expect(events[0].is_error);
    try std.testing.expectEqualStrings("build failed", events[0].text);
}

test "tool_result block arrays are flattened to text" {
    const line =
        \\{"type":"user","message":{"role":"user","content":[
        \\{"type":"tool_result","content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}
        \\]}}
    ;
    const events = try parseLineAlloc(std.testing.allocator, line);
    defer freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqualStrings("line one\nline two", events[0].text);
}

test "ui bookkeeping and invalid lines yield no events" {
    const chrome = try parseLineAlloc(std.testing.allocator, "{\"type\":\"mode\",\"mode\":\"normal\"}");
    defer freeEvents(std.testing.allocator, chrome);
    try std.testing.expectEqual(@as(usize, 0), chrome.len);

    const invalid = try parseLineAlloc(std.testing.allocator, "not json at all");
    defer freeEvents(std.testing.allocator, invalid);
    try std.testing.expectEqual(@as(usize, 0), invalid.len);
}

test "oversized text is truncated on a utf-8 boundary" {
    const big = try std.testing.allocator.alloc(u8, max_event_text_bytes + 100);
    defer std.testing.allocator.free(big);
    @memset(big, 'a');

    const out = try truncatedDupe(std.testing.allocator, big);
    defer std.testing.allocator.free(out);
    try std.testing.expect(out.len < big.len);
    try std.testing.expect(std.mem.endsWith(u8, out, "…[truncated]"));
    try std.testing.expect(std.unicode.utf8ValidateSlice(out));
}

test "watcher discovers a fresh session file and tails incrementally" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    // Project layout: <config>/projects/<sanitized>/<session>.jsonl
    const config_dir = try tmp.dir.realpathAlloc(std.testing.allocator, ".");
    defer std.testing.allocator.free(config_dir);
    const project_dir = try projectLogDirAlloc(std.testing.allocator, config_dir, "/work/repo");
    defer std.testing.allocator.free(project_dir);
    try std.fs.cwd().makePath(project_dir);

    var watcher = try SessionLogWatcher.init(
        std.testing.allocator,
        config_dir,
        "/work/repo",
        std.time.timestamp() - 60,
    );
    defer watcher.deinit();

    // No session file yet: poll is a quiet no-op.
    const empty = try watcher.poll(std.testing.allocator);
    defer freeEvents(std.testing.allocator, empty);
    try std.testing.expectEqual(@as(usize, 0), empty.len);

    const session_path = try std.fs.path.join(
        std.testing.allocator,
        &.{ project_dir, "0000-session.jsonl" },
    );
    defer std.testing.allocator.free(session_path);
    const log_file = try std.fs.cwd().createFile(session_path, .{});
    defer log_file.close();

    try log_file.writeAll(
        "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"first\"}]}}\n",
    );
    // Plus an incomplete trailing line that must carry over to the next poll.
    try log_file.writeAll("{\"type\":\"assistant\",\"message\":{\"content\":[{\"ty");

    const first = try watcher.poll(std.testing.allocator);
    defer freeEvents(std.testing.allocator, first);
    try std.testing.expectEqual(@as(usize, 1), first.len);
    try std.testing.expectEqualStrings("first", first[0].text);
    try std.testing.expect(watcher.sessionPath() != null);

    try log_file.writeAll("pe\":\"text\",\"text\":\"second\"}]}}\n");

    const second = try watcher.poll(std.testing.allocator);
    defer freeEvents(std.testing.allocator, second);
    try std.testing.expectEqual(@as(usize, 1), second.len);
    try std.testing.expectEqualStrings("second", second[0].text);
}

test "watcher ignores session files older than the run" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const config_dir = try tmp.dir.realpathAlloc(std.testing.allocator, ".");
    defer std.testing.allocator.free(config_dir);
    const project_dir = try projectLogDirAlloc(std.testing.allocator, config_dir, "/work/repo");
    defer std.testing.allocator.free(project_dir);
    try std.fs.cwd().makePath(project_dir);

    const stale_path = try std.fs.path.join(
        std.testing.allocator,
        &.{ project_dir, "stale.jsonl" },
    );
    defer std.testing.allocator.free(stale_path);
    try std.fs.cwd().writeFile(.{ .sub_path = stale_path, .data = "{}\n" });

    var watcher = try SessionLogWatcher.init(
        std.testing.allocator,
        config_dir,
        "/work/repo",
        std.time.timestamp() + 3600,
    );
    defer watcher.deinit();

    const events = try watcher.poll(std.testing.allocator);
    defer freeEvents(std.testing.allocator, events);
    try std.testing.expectEqual(@as(usize, 0), events.len);
    try std.testing.expect(watcher.sessionPath() == null);
}
