const std = @import("std");
const policy = @import("../policy/types.zig");
const types = @import("types.zig");

const MAX_EVENTS = 4;

pub const DetectorRules = struct {
    final_markers: []const []const u8 = &.{ "Final answer:", "FINAL:" },
    approval_markers: []const []const u8 = &.{ "? [y/N]", "? [Y/n]", "Do you want to proceed?", "Allow command?" },
    error_markers: []const []const u8 = &.{ "Error:", "error:", "fatal:", "codex error:" },
};

pub fn detectWithRules(
    allocator: std.mem.Allocator,
    input: types.PromptInput,
    rules: DetectorRules,
) ![]types.PromptEvent {
    var found: [MAX_EVENTS]types.PromptEvent = undefined;
    var count: usize = 0;
    const text = primaryText(input);

    if (firstMarker(text, rules.approval_markers) != null) {
        found[count] = .{ .approval_requested = .{
            .text = text,
            .risk = approvalRisk(text),
            .action = inferAction(text),
        } };
        count += 1;
    } else if (looksLikeQuestion(text)) {
        found[count] = .{ .question_asked = .{ .text = text } };
        count += 1;
    }

    if (firstMarker(text, rules.final_markers)) |marker| {
        found[count] = .{ .final_answer_candidate = trimLeading(finalCandidate(text, marker)) };
        count += 1;
    }

    if (firstMarker(text, rules.error_markers)) |marker| {
        found[count] = .{ .error_detected = trimLeading(finalCandidate(text, marker)) };
        count += 1;
    }

    const events = try allocator.alloc(types.PromptEvent, count);
    @memcpy(events, found[0..count]);
    return events;
}

pub fn genericDetect(
    _: *types.ProviderPromptDetector,
    input: types.PromptInput,
    _: types.PromptContext,
    allocator: std.mem.Allocator,
) anyerror![]types.PromptEvent {
    return detectWithRules(allocator, input, .{});
}

pub const generic_detector = types.ProviderPromptDetector{
    .provider = .{ .value = "generic" },
    .context = undefined,
    .detect = genericDetect,
};

fn primaryText(input: types.PromptInput) []const u8 {
    if (input.visible_text.len > 0) return input.visible_text;
    if (input.scrollback_text.len > 0) return input.scrollback_text;
    return input.raw_window;
}

fn firstMarker(text: []const u8, markers: []const []const u8) ?[]const u8 {
    for (markers) |marker| {
        if (std.mem.indexOf(u8, text, marker) != null) return marker;
    }
    return null;
}

fn finalCandidate(text: []const u8, marker: []const u8) []const u8 {
    if (std.mem.indexOf(u8, text, marker)) |index| {
        return text[index + marker.len ..];
    }
    return text;
}

fn trimLeading(text: []const u8) []const u8 {
    return std.mem.trimLeft(u8, text, " \t\r\n");
}

fn looksLikeQuestion(text: []const u8) bool {
    return std.mem.endsWith(u8, std.mem.trimRight(u8, text, " \t\r\n"), "?");
}

fn approvalRisk(text: []const u8) policy.RiskLevel {
    if (containsAny(text, &.{ "rm -rf", "delete", "secret", ".ssh", ".aws" })) return .high;
    if (containsAny(text, &.{ "install", "network", "push", "write" })) return .medium;
    return .low;
}

fn inferAction(text: []const u8) ?policy.Action {
    if (std.mem.indexOf(u8, text, "npm install") != null) {
        return .{ .install_package = .{ .manager = "npm", .package = "*" } };
    }
    if (std.mem.indexOf(u8, text, "git push") != null) {
        return .{ .git_push = .{ .remote = "origin", .branch = "unknown" } };
    }
    return null;
}

fn containsAny(text: []const u8, needles: []const []const u8) bool {
    for (needles) |needle| {
        if (std.mem.indexOf(u8, text, needle) != null) return true;
    }
    return false;
}

test "generic detector finds approval prompts from recorded provider text" {
    const fixture = try readFixture("test/fixtures/providers/codex_approval.txt");
    defer std.testing.allocator.free(fixture);
    var detector = generic_detector;
    const events = try detector.detect(
        &detector,
        .{ .visible_text = fixture },
        .{
            .provider = .{ .value = "codex" },
            .adapter = .{ .value = "codex.exec" },
        },
        std.testing.allocator,
    );
    defer types.freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqual(types.PromptKind.approval_requested, events[0].kind());
    try std.testing.expectEqual(policy.RiskLevel.medium, events[0].approval_requested.risk);
}

test "generic detector finds final answer candidates from recorded provider text" {
    const fixture = try readFixture("test/fixtures/providers/fake_final.txt");
    defer std.testing.allocator.free(fixture);
    const events = try detectWithRules(std.testing.allocator, .{ .visible_text = fixture }, .{
        .final_markers = &.{"FAKE_FINAL:"},
    });
    defer types.freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqual(types.PromptKind.final_answer_candidate, events[0].kind());
    try std.testing.expect(std.mem.startsWith(u8, events[0].final_answer_candidate, "all done"));
}

test "generic detector finds provider error screens from recorded provider text" {
    const fixture = try readFixture("test/fixtures/providers/codex_error.txt");
    defer std.testing.allocator.free(fixture);
    const events = try detectWithRules(std.testing.allocator, .{ .visible_text = fixture }, .{});
    defer types.freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqual(types.PromptKind.error_detected, events[0].kind());
}

test "provider detector finds claude approval prompts from recorded text" {
    const fixture = try readFixture("test/fixtures/providers/claude_approval.txt");
    defer std.testing.allocator.free(fixture);
    const events = try @import("providers.zig").detect(.{
        .visible_text = fixture,
    }, .{
        .provider = .{ .value = "claude" },
        .adapter = .{ .value = "claude.print" },
    }, std.testing.allocator);
    defer types.freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqual(types.PromptKind.approval_requested, events[0].kind());
}

test "provider detector finds gemini approval prompts from recorded text" {
    const fixture = try readFixture("test/fixtures/providers/gemini_approval.txt");
    defer std.testing.allocator.free(fixture);
    const events = try @import("providers.zig").detect(.{
        .visible_text = fixture,
    }, .{
        .provider = .{ .value = "gemini" },
        .adapter = .{ .value = "gemini.headless" },
    }, std.testing.allocator);
    defer types.freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(@as(usize, 1), events.len);
    try std.testing.expectEqual(types.PromptKind.approval_requested, events[0].kind());
}

fn readFixture(path: []const u8) ![]u8 {
    return try std.fs.cwd().readFileAlloc(std.testing.allocator, path, 64 * 1024);
}
