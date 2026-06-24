const std = @import("std");
const adapters = @import("../adapters/types.zig");
const detectors = @import("detectors.zig");
const types = @import("types.zig");

pub const ProviderPromptRules = struct {
    provider: adapters.ProviderId,
    rules: detectors.DetectorRules,
};

const fake_rules = ProviderPromptRules{
    .provider = .{ .value = "fake" },
    .rules = .{
        .final_markers = &.{"FAKE_FINAL:"},
        .approval_markers = &.{ "FAKE_APPROVAL:", "? [y/N]" },
        .error_markers = &.{ "FAKE_ERROR:", "Error:" },
    },
};

const codex_rules = ProviderPromptRules{
    .provider = .{ .value = "codex" },
    .rules = .{
        .final_markers = &.{ "Final answer:", "FINAL:" },
        .approval_markers = &.{ "? [y/N]", "Do you want to proceed?", "Allow command?" },
        .error_markers = &.{ "codex error:", "Error:", "error:" },
    },
};

const claude_rules = ProviderPromptRules{
    .provider = .{ .value = "claude" },
    .rules = .{
        .final_markers = &.{ "Final answer:", "Done." },
        .approval_markers = &.{ "Do you want to proceed?", "Allow this command?", "? [y/N]" },
        .error_markers = &.{ "Claude encountered an error", "Error:" },
    },
};

const claudecode_rules = ProviderPromptRules{
    .provider = .{ .value = "claudecode" },
    .rules = claude_rules.rules,
};

const gemini_rules = ProviderPromptRules{
    .provider = .{ .value = "gemini" },
    .rules = .{
        .final_markers = &.{ "Final answer:", "Response:" },
        .approval_markers = &.{ "Allow command?", "Proceed?", "? [y/N]" },
        .error_markers = &.{ "Gemini error:", "Error:" },
    },
};

const opencode_rules = ProviderPromptRules{
    .provider = .{ .value = "opencode" },
    .rules = .{
        .final_markers = &.{ "Final answer:", "Response:", "Done." },
        .approval_markers = &.{ "Allow command?", "Proceed?", "? [y/N]", "permission" },
        .error_markers = &.{ "OpenCode error:", "Error:", "error:" },
    },
};

const pi_rules = ProviderPromptRules{
    .provider = .{ .value = "pi" },
    .rules = .{
        .final_markers = &.{ "Final answer:", "Response:", "Done." },
        .approval_markers = &.{ "Allow command?", "Proceed?", "? [y/N]", "approve" },
        .error_markers = &.{ "Pi error:", "Error:", "error:" },
    },
};

pub fn rulesForProvider(provider: adapters.ProviderId) detectors.DetectorRules {
    if (providerEql(provider, fake_rules.provider)) return fake_rules.rules;
    if (providerEql(provider, codex_rules.provider)) return codex_rules.rules;
    if (providerEql(provider, claude_rules.provider)) return claude_rules.rules;
    if (providerEql(provider, claudecode_rules.provider)) return claudecode_rules.rules;
    if (providerEql(provider, gemini_rules.provider)) return gemini_rules.rules;
    if (providerEql(provider, opencode_rules.provider)) return opencode_rules.rules;
    if (providerEql(provider, pi_rules.provider)) return pi_rules.rules;
    return .{};
}

pub fn detect(
    input: types.PromptInput,
    context: types.PromptContext,
    allocator: std.mem.Allocator,
) ![]types.PromptEvent {
    return detectors.detectWithRules(allocator, input, rulesForProvider(context.provider));
}

fn providerEql(a: adapters.ProviderId, b: adapters.ProviderId) bool {
    return std.mem.eql(u8, a.value, b.value);
}

test "provider-specific detector uses fake final marker" {
    const events = try detect(
        .{ .visible_text = "FAKE_FINAL: finished\n" },
        .{
            .provider = .{ .value = "fake" },
            .adapter = .{ .value = "fake.headless" },
        },
        std.testing.allocator,
    );
    defer types.freeEvents(std.testing.allocator, events);

    try std.testing.expectEqual(types.PromptKind.final_answer_candidate, events[0].kind());
}
