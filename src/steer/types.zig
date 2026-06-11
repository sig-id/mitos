//! Steering interactions: the semantic actions a controller can take against
//! an interactive agent running under a PTY. The brain decides *what* to do
//! (approve, answer a question, send a new prompt); a per-provider keymap
//! decides *which bytes* realize that intent in the provider's TUI.
//!
//! State extraction is deliberately not handled here: for providers that
//! write structured session logs (claude), progress is read from those logs,
//! not scraped from the screen. The PTY is the control channel only.
const std = @import("std");
const adapters = @import("../adapters/types.zig");

pub const InteractionKind = enum {
    approve,
    deny,
    answer,
    prompt,
    interrupt,
    select_option,
};

pub const Interaction = union(InteractionKind) {
    /// Accept the pending approval dialog (tool/permission request).
    approve,
    /// Reject/dismiss the pending approval dialog.
    deny,
    /// Reply to a question the agent asked, through its input box.
    answer: []const u8,
    /// Send a new instruction/prompt to the agent's input box.
    prompt: []const u8,
    /// Interrupt whatever the agent is currently doing.
    interrupt,
    /// Pick a numbered option in a multi-choice dialog (1-9).
    select_option: u8,

    pub fn kind(self: Interaction) InteractionKind {
        return std.meta.activeTag(self);
    }
};

pub const EncodeError = error{
    OutOfMemory,
    EmptyText,
    InvalidOption,
};

/// One per provider: turns a semantic interaction into the byte sequence the
/// provider's TUI expects on its PTY. Returned bytes are owned by the caller.
pub const SteeringAdapter = struct {
    provider: adapters.ProviderId,
    encode: *const fn (Interaction, std.mem.Allocator) EncodeError![]u8,
};

/// Parse the wire form of an interaction (control-protocol `steer` command).
/// `text` backs answer/prompt; `option` backs select_option.
pub fn parseInteraction(name: []const u8, text: []const u8, option: ?u64) ?Interaction {
    if (std.ascii.eqlIgnoreCase(name, "approve")) return .approve;
    if (std.ascii.eqlIgnoreCase(name, "deny") or std.ascii.eqlIgnoreCase(name, "reject")) return .deny;
    if (std.ascii.eqlIgnoreCase(name, "answer")) return .{ .answer = text };
    if (std.ascii.eqlIgnoreCase(name, "prompt")) return .{ .prompt = text };
    if (std.ascii.eqlIgnoreCase(name, "interrupt")) return .interrupt;
    if (std.ascii.eqlIgnoreCase(name, "select_option") or std.ascii.eqlIgnoreCase(name, "select")) {
        const value = option orelse return null;
        if (value == 0 or value > 9) return null;
        return .{ .select_option = @intCast(value) };
    }
    return null;
}

test "interactions expose their semantic kind" {
    const event: Interaction = .{ .prompt = "continue" };
    try std.testing.expectEqual(InteractionKind.prompt, event.kind());
}

test "parseInteraction maps wire names to interactions" {
    try std.testing.expectEqual(InteractionKind.approve, parseInteraction("approve", "", null).?.kind());
    try std.testing.expectEqual(InteractionKind.deny, parseInteraction("reject", "", null).?.kind());
    try std.testing.expectEqualStrings("why?", parseInteraction("answer", "why?", null).?.answer);
    try std.testing.expectEqual(@as(u8, 2), parseInteraction("select_option", "", 2).?.select_option);
    try std.testing.expect(parseInteraction("select_option", "", 0) == null);
    try std.testing.expect(parseInteraction("select_option", "", null) == null);
    try std.testing.expect(parseInteraction("dance", "", null) == null);
}
