//! Per-provider keymaps: which bytes realize a steering interaction in each
//! provider's TUI. These mappings encode observed dialog behavior:
//!
//!   claude  permission dialogs are numbered, `1` is always the affirmative
//!           option; Esc dismisses the dialog (= "no") and interrupts a
//!           running turn. Free text goes through the input box; bracketed
//!           paste keeps multi-line text from submitting per-line.
//!   codex   approval modals accept `y`/`n` shortcuts; Esc interrupts.
//!   gemini  confirmation dialogs are radio lists whose default is the
//!           affirmative, so Enter approves; Esc cancels/interrupts.
//!   generic plain y/n line protocols; Ctrl-C interrupts.
const std = @import("std");
const types = @import("types.zig");

const esc = "\x1b";
const ctrl_c = "\x03";
const paste_begin = "\x1b[200~";
const paste_end = "\x1b[201~";

pub const claude = types.SteeringAdapter{
    .provider = .{ .value = "claude" },
    .encode = encodeClaude,
};

pub const codex = types.SteeringAdapter{
    .provider = .{ .value = "codex" },
    .encode = encodeCodex,
};

pub const gemini = types.SteeringAdapter{
    .provider = .{ .value = "gemini" },
    .encode = encodeGemini,
};

pub const generic = types.SteeringAdapter{
    .provider = .{ .value = "generic-pty" },
    .encode = encodeGeneric,
};

fn encodeClaude(interaction: types.Interaction, allocator: std.mem.Allocator) types.EncodeError![]u8 {
    return switch (interaction) {
        .approve => literalAlloc(allocator, "1"),
        .deny => literalAlloc(allocator, esc),
        .interrupt => literalAlloc(allocator, esc),
        .answer, .prompt => |text| submitTextAlloc(allocator, text, .bracketed),
        .select_option => |option| optionDigitAlloc(allocator, option, .bare),
    };
}

fn encodeCodex(interaction: types.Interaction, allocator: std.mem.Allocator) types.EncodeError![]u8 {
    return switch (interaction) {
        .approve => literalAlloc(allocator, "y"),
        .deny => literalAlloc(allocator, "n"),
        .interrupt => literalAlloc(allocator, esc),
        .answer, .prompt => |text| submitTextAlloc(allocator, text, .bracketed),
        .select_option => |option| optionDigitAlloc(allocator, option, .bare),
    };
}

fn encodeGemini(interaction: types.Interaction, allocator: std.mem.Allocator) types.EncodeError![]u8 {
    return switch (interaction) {
        .approve => literalAlloc(allocator, "\r"),
        .deny => literalAlloc(allocator, esc),
        .interrupt => literalAlloc(allocator, esc),
        .answer, .prompt => |text| submitTextAlloc(allocator, text, .bracketed),
        .select_option => |option| optionDigitAlloc(allocator, option, .submitted),
    };
}

fn encodeGeneric(interaction: types.Interaction, allocator: std.mem.Allocator) types.EncodeError![]u8 {
    return switch (interaction) {
        .approve => literalAlloc(allocator, "y\r"),
        .deny => literalAlloc(allocator, "n\r"),
        .interrupt => literalAlloc(allocator, ctrl_c),
        .answer, .prompt => |text| submitTextAlloc(allocator, text, .plain),
        .select_option => |option| optionDigitAlloc(allocator, option, .submitted),
    };
}

const TextMode = enum { bracketed, plain };
const OptionMode = enum { bare, submitted };

fn literalAlloc(allocator: std.mem.Allocator, bytes: []const u8) types.EncodeError![]u8 {
    return allocator.dupe(u8, bytes) catch return error.OutOfMemory;
}

fn submitTextAlloc(
    allocator: std.mem.Allocator,
    text: []const u8,
    mode: TextMode,
) types.EncodeError![]u8 {
    if (std.mem.trim(u8, text, " \t\r\n").len == 0) return error.EmptyText;
    return switch (mode) {
        .bracketed => std.mem.concat(allocator, u8, &.{ paste_begin, text, paste_end, "\r" }) catch
            return error.OutOfMemory,
        .plain => std.mem.concat(allocator, u8, &.{ text, "\r" }) catch
            return error.OutOfMemory,
    };
}

fn optionDigitAlloc(
    allocator: std.mem.Allocator,
    option: u8,
    mode: OptionMode,
) types.EncodeError![]u8 {
    if (option == 0 or option > 9) return error.InvalidOption;
    const digit: u8 = '0' + option;
    return switch (mode) {
        .bare => literalAlloc(allocator, &.{digit}),
        .submitted => literalAlloc(allocator, &.{ digit, '\r' }),
    };
}

test "claude approval maps to first numbered option and esc rejects" {
    const yes = try claude.encode(.approve, std.testing.allocator);
    defer std.testing.allocator.free(yes);
    const no = try claude.encode(.deny, std.testing.allocator);
    defer std.testing.allocator.free(no);

    try std.testing.expectEqualStrings("1", yes);
    try std.testing.expectEqualStrings(esc, no);
}

test "claude prompts go through bracketed paste so newlines do not submit early" {
    const bytes = try claude.encode(.{ .prompt = "fix the test\nthen rerun" }, std.testing.allocator);
    defer std.testing.allocator.free(bytes);

    try std.testing.expectEqualStrings(
        paste_begin ++ "fix the test\nthen rerun" ++ paste_end ++ "\r",
        bytes,
    );
}

test "claude answers reuse the prompt path" {
    const bytes = try claude.encode(.{ .answer = "use port 8080" }, std.testing.allocator);
    defer std.testing.allocator.free(bytes);

    try std.testing.expect(std.mem.startsWith(u8, bytes, paste_begin));
    try std.testing.expect(std.mem.endsWith(u8, bytes, paste_end ++ "\r"));
}

test "codex approval uses y/n shortcuts" {
    const yes = try codex.encode(.approve, std.testing.allocator);
    defer std.testing.allocator.free(yes);
    const no = try codex.encode(.deny, std.testing.allocator);
    defer std.testing.allocator.free(no);

    try std.testing.expectEqualStrings("y", yes);
    try std.testing.expectEqualStrings("n", no);
}

test "gemini approves with enter on the default radio option" {
    const yes = try gemini.encode(.approve, std.testing.allocator);
    defer std.testing.allocator.free(yes);

    try std.testing.expectEqualStrings("\r", yes);
}

test "generic line protocol submits y/n and plain text" {
    const yes = try generic.encode(.approve, std.testing.allocator);
    defer std.testing.allocator.free(yes);
    const text = try generic.encode(.{ .answer = "hello" }, std.testing.allocator);
    defer std.testing.allocator.free(text);
    const stop = try generic.encode(.interrupt, std.testing.allocator);
    defer std.testing.allocator.free(stop);

    try std.testing.expectEqualStrings("y\r", yes);
    try std.testing.expectEqualStrings("hello\r", text);
    try std.testing.expectEqualStrings(ctrl_c, stop);
}

test "select_option validates the 1-9 range" {
    const pick = try claude.encode(.{ .select_option = 2 }, std.testing.allocator);
    defer std.testing.allocator.free(pick);
    try std.testing.expectEqualStrings("2", pick);

    try std.testing.expectError(
        error.InvalidOption,
        claude.encode(.{ .select_option = 0 }, std.testing.allocator),
    );
    try std.testing.expectError(
        error.InvalidOption,
        generic.encode(.{ .select_option = 12 }, std.testing.allocator),
    );
}

test "empty steering text is rejected before touching the pty" {
    try std.testing.expectError(
        error.EmptyText,
        claude.encode(.{ .prompt = "  \n" }, std.testing.allocator),
    );
}
