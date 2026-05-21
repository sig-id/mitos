const std = @import("std");
const adapters = @import("../adapters/types.zig");
const policy = @import("../policy/types.zig");
const runtime = @import("../runtime/types.zig");

pub const PromptKind = enum {
    approval_requested,
    question_asked,
    final_answer_candidate,
    idle_detected,
    error_detected,
};

pub const PromptInput = struct {
    visible_text: []const u8,
    scrollback_text: []const u8 = "",
    raw_window: []const u8 = "",
};

pub const PromptContext = struct {
    provider: adapters.ProviderId,
    adapter: adapters.AdapterId,
    provider_version: ?[]const u8 = null,
    timestamp_ms: runtime.TimestampMs = 0,
};

pub const ApprovalPrompt = struct {
    text: []const u8,
    risk: policy.RiskLevel,
    action: ?policy.Action = null,
};

pub const ContextQuestion = struct {
    text: []const u8,
};

pub const PromptEvent = union(PromptKind) {
    approval_requested: ApprovalPrompt,
    question_asked: ContextQuestion,
    final_answer_candidate: []const u8,
    idle_detected,
    error_detected: []const u8,

    pub fn kind(self: PromptEvent) PromptKind {
        return std.meta.activeTag(self);
    }
};

pub const ProviderPromptDetector = struct {
    provider: adapters.ProviderId,
    context: *anyopaque,
    detect: *const fn (
        *ProviderPromptDetector,
        PromptInput,
        PromptContext,
        std.mem.Allocator,
    ) anyerror![]PromptEvent,
};

pub fn freeEvents(allocator: std.mem.Allocator, events: []PromptEvent) void {
    if (events.len > 0) allocator.free(events);
}

pub fn toRuntimeEvent(event: PromptEvent) runtime.RuntimeEvent {
    return switch (event) {
        .approval_requested => |prompt| .{
            .prompt_detected = .{
                .prompt_kind = "approval_requested",
                .text = prompt.text,
                .risk = @tagName(prompt.risk),
            },
        },
        .question_asked => |question| .{
            .prompt_detected = .{
                .prompt_kind = "question_asked",
                .text = question.text,
                .risk = null,
            },
        },
        .final_answer_candidate => |text| .{
            .prompt_detected = .{
                .prompt_kind = "final_answer_candidate",
                .text = text,
                .risk = null,
            },
        },
        .idle_detected => .{
            .prompt_detected = .{
                .prompt_kind = "idle_detected",
                .text = "",
                .risk = null,
            },
        },
        .error_detected => |text| .{
            .prompt_detected = .{
                .prompt_kind = "error_detected",
                .text = text,
                .risk = "high",
            },
        },
    };
}

test "prompt events expose semantic kind only" {
    const event: PromptEvent = .{ .final_answer_candidate = "done" };

    try std.testing.expectEqual(PromptKind.final_answer_candidate, event.kind());
}

test "prompt events map to runtime prompt events" {
    const event = toRuntimeEvent(.{ .approval_requested = .{
        .text = "Run npm install? [y/N]",
        .risk = .medium,
    } });

    try std.testing.expectEqualStrings("approval_requested", event.prompt_detected.prompt_kind);
    try std.testing.expectEqualStrings("medium", event.prompt_detected.risk.?);
}
