const std = @import("std");
const adapters = @import("../adapters/types.zig");

pub const types = @import("types.zig");
pub const keymaps = @import("keymaps.zig");

pub const Interaction = types.Interaction;
pub const InteractionKind = types.InteractionKind;
pub const EncodeError = types.EncodeError;
pub const SteeringAdapter = types.SteeringAdapter;
pub const parseInteraction = types.parseInteraction;

/// Resolve the steering keymap for a provider. Unknown providers fall back to
/// the generic y/n line-protocol keymap so steering never goes unanswered.
pub fn forProvider(provider: adapters.ProviderId) SteeringAdapter {
    if (std.mem.eql(u8, provider.value, "claude")) return keymaps.claude;
    if (std.mem.eql(u8, provider.value, "claudecode")) return keymaps.claude;
    if (std.mem.eql(u8, provider.value, "codex")) return keymaps.codex;
    if (std.mem.eql(u8, provider.value, "gemini")) return keymaps.gemini;
    return keymaps.generic;
}

test "provider lookup returns dedicated keymaps with generic fallback" {
    try std.testing.expectEqualStrings("claude", forProvider(.{ .value = "claude" }).provider.value);
    try std.testing.expectEqualStrings("claude", forProvider(.{ .value = "claudecode" }).provider.value);
    try std.testing.expectEqualStrings("codex", forProvider(.{ .value = "codex" }).provider.value);
    try std.testing.expectEqualStrings("gemini", forProvider(.{ .value = "gemini" }).provider.value);
    try std.testing.expectEqualStrings("generic-pty", forProvider(.{ .value = "fake" }).provider.value);
}

test {
    std.testing.refAllDecls(@This());
}
