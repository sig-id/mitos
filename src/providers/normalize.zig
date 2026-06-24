const std = @import("std");
const adapters = @import("../adapters/types.zig");

pub const NormalizedKind = enum {
    assistant_text,
    tool_use,
    tool_result,
    token_usage,
    status,
    final_result,
};

pub fn emitNormalizedJsonl(
    allocator: std.mem.Allocator,
    sink: adapters.AdapterEventSink,
    provider: adapters.ProviderId,
    bytes: []const u8,
) !void {
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;

        var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch continue;
        defer parsed.deinit();
        if (parsed.value != .object) continue;

        const object = &parsed.value.object;
        var normalized: std.ArrayList([]u8) = .empty;
        defer {
            for (normalized.items) |item| allocator.free(item);
            normalized.deinit(allocator);
        }

        try normalizeMany(allocator, provider.value, object, &normalized);
        for (normalized.items) |item| {
            try sink.emit(sink.context, .{ .structured = item });
        }
    }
}

pub fn extractFinalOutputAlloc(
    allocator: std.mem.Allocator,
    stdout: []const u8,
) !?[]u8 {
    var last_candidate: ?[]u8 = null;
    errdefer if (last_candidate) |candidate| allocator.free(candidate);

    var lines = std.mem.splitScalar(u8, stdout, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;

        var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch continue;
        defer parsed.deinit();
        if (parsed.value != .object) continue;

        const object = &parsed.value.object;
        if (finalTextFromObject(object)) |text| {
            try replaceCandidate(allocator, &last_candidate, text);
            continue;
        }
        if (messageText(object)) |text| {
            if (isFinalishType(object)) try replaceCandidate(allocator, &last_candidate, text);
            continue;
        }
        if (messagesLastAssistantText(object)) |text| {
            try replaceCandidate(allocator, &last_candidate, text);
            continue;
        }
    }

    if (last_candidate) |candidate| {
        last_candidate = null;
        return candidate;
    }
    return null;
}

fn normalizeMany(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
    normalized: *std.ArrayList([]u8),
) !void {
    if (isProvider(provider, "codex")) {
        try normalizeCodex(allocator, provider, object, normalized);
    } else if (isProvider(provider, "claude") or isProvider(provider, "claudecode") or isProvider(provider, "claude-code")) {
        try normalizeClaude(allocator, provider, object, normalized);
    } else if (isProvider(provider, "opencode")) {
        try normalizeOpenCode(allocator, provider, object, normalized);
    } else if (isProvider(provider, "pi")) {
        try normalizePi(allocator, provider, object, normalized);
    }

    if (normalized.items.len == 0) {
        if (try normalizeGenericOne(allocator, provider, object)) |event| {
            try normalized.append(allocator, event);
        }
    }
}

fn normalizeGenericOne(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
) !?[]u8 {
    const raw_type = stringField(object, "type") orelse "";

    if (tokenUsageObject(object)) |usage| {
        return try renderTokenUsage(allocator, provider, usage);
    }
    if (toolUseObject(object)) |tool| {
        return try renderToolUse(allocator, provider, tool);
    }
    if (toolResultObject(object)) |tool| {
        return try renderToolResult(allocator, provider, tool);
    }
    if (messageText(object)) |text| {
        if (text.len == 0) return null;
        const kind: NormalizedKind = if (isFinalishType(object)) .final_result else .assistant_text;
        return try renderText(allocator, provider, kind, text);
    }
    if (finalTextFromObject(object)) |text| {
        if (text.len == 0) return null;
        return try renderText(allocator, provider, .final_result, text);
    }
    if (messagesLastAssistantText(object)) |text| {
        if (text.len == 0) return null;
        return try renderText(allocator, provider, .final_result, text);
    }
    if (isStatusType(raw_type)) {
        return try renderStatus(allocator, provider, raw_type);
    }

    return null;
}

const Usage = struct {
    input_tokens: ?i64 = null,
    output_tokens: ?i64 = null,
    total_tokens: ?i64 = null,
    usd: ?f64 = null,
    model: ?[]const u8 = null,
};

const Tool = struct {
    id: ?[]const u8 = null,
    name: ?[]const u8 = null,
    result_text: ?[]const u8 = null,
    is_error: ?bool = null,
};

fn normalizeCodex(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
    normalized: *std.ArrayList([]u8),
) !void {
    const raw_type = stringField(object, "type") orelse "";
    if (std.mem.eql(u8, raw_type, "response.output_item.added") or
        std.mem.eql(u8, raw_type, "response.output_item.done") or
        std.mem.eql(u8, raw_type, "raw_response_item"))
    {
        if (object.get("item")) |item| {
            try appendCodexResponseItem(allocator, provider, item, normalized);
        }
        return;
    }

    if (std.mem.eql(u8, raw_type, "function_call") or
        std.mem.eql(u8, raw_type, "custom_tool_call") or
        std.mem.eql(u8, raw_type, "local_shell_call") or
        std.mem.eql(u8, raw_type, "dynamic_tool_call") or
        std.mem.eql(u8, raw_type, "tool_search_call"))
    {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = firstString(stringField(object, "call_id"), stringField(object, "id")),
            .name = firstString(stringField(object, "name"), codexToolNameForType(raw_type)),
        }));
        return;
    }

    if (std.mem.eql(u8, raw_type, "function_call_output") or
        std.mem.eql(u8, raw_type, "tool_call_output") or
        std.mem.eql(u8, raw_type, "tool_call_response"))
    {
        try appendToolResultFromObject(allocator, provider, object, normalized, .{
            .id = firstString(stringField(object, "call_id"), stringField(object, "id")),
            .name = firstString(stringField(object, "name"), codexToolNameForType(raw_type)),
            .text_fields = &.{ "output", "result", "content", "text" },
            .is_error = firstBool(boolField(object, "is_error"), boolField(object, "isError")),
        });
        return;
    }

    if (std.mem.eql(u8, raw_type, "exec_command_begin")) {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = stringField(object, "call_id"),
            .name = "exec_command",
        }));
        return;
    }

    if (std.mem.eql(u8, raw_type, "exec_command_end")) {
        try appendToolResultFromObject(allocator, provider, object, normalized, .{
            .id = stringField(object, "call_id"),
            .name = "exec_command",
            .text_fields = &.{ "output", "aggregated_output", "text" },
            .is_error = codexExecIsError(object),
        });
        return;
    }

    if (std.mem.eql(u8, raw_type, "mcp_tool_call_begin") or
        std.mem.eql(u8, raw_type, "collab_tool_call") or
        std.mem.eql(u8, raw_type, "tool_call_started"))
    {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = firstString(stringField(object, "call_id"), stringField(object, "id")),
            .name = firstString(stringField(object, "name"), stringField(object, "tool_name")),
        }));
        return;
    }

    if (std.mem.eql(u8, raw_type, "mcp_tool_call_end") or
        std.mem.eql(u8, raw_type, "tool_call_ended"))
    {
        try appendToolResultFromObject(allocator, provider, object, normalized, .{
            .id = firstString(stringField(object, "call_id"), stringField(object, "id")),
            .name = firstString(stringField(object, "name"), stringField(object, "tool_name")),
            .text_fields = &.{ "output", "result", "content", "text" },
            .is_error = firstBool(boolField(object, "is_error"), boolField(object, "isError")),
        });
        return;
    }
}

fn appendCodexResponseItem(
    allocator: std.mem.Allocator,
    provider: []const u8,
    item: std.json.Value,
    normalized: *std.ArrayList([]u8),
) !void {
    if (item != .object) return;
    const item_object = &item.object;
    const item_type = stringField(item_object, "type") orelse "";

    if (std.mem.eql(u8, item_type, "function_call") or
        std.mem.eql(u8, item_type, "custom_tool_call") or
        std.mem.eql(u8, item_type, "local_shell_call") or
        std.mem.eql(u8, item_type, "dynamic_tool_call") or
        std.mem.eql(u8, item_type, "tool_search_call"))
    {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = firstString(stringField(item_object, "call_id"), stringField(item_object, "id")),
            .name = firstString(stringField(item_object, "name"), codexToolNameForType(item_type)),
        }));
        return;
    }

    if (std.mem.eql(u8, item_type, "function_call_output") or
        std.mem.eql(u8, item_type, "tool_call_output") or
        std.mem.eql(u8, item_type, "tool_call_response"))
    {
        try appendToolResultFromObject(allocator, provider, item_object, normalized, .{
            .id = firstString(stringField(item_object, "call_id"), stringField(item_object, "id")),
            .name = firstString(stringField(item_object, "name"), codexToolNameForType(item_type)),
            .text_fields = &.{ "output", "result", "content", "text" },
            .is_error = firstBool(boolField(item_object, "is_error"), boolField(item_object, "isError")),
        });
        return;
    }
}

fn normalizeClaude(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
    normalized: *std.ArrayList([]u8),
) !void {
    if (object.get("message")) |message| {
        try appendClaudeMessage(allocator, provider, message, normalized);
    }
    if (object.get("content")) |content| {
        try appendClaudeContent(allocator, provider, content, normalized);
    }

    const raw_type = stringField(object, "type") orelse "";
    if (std.mem.eql(u8, raw_type, "tool_use")) {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = stringField(object, "id"),
            .name = stringField(object, "name"),
        }));
        return;
    }
    if (std.mem.eql(u8, raw_type, "tool_result")) {
        try appendToolResultFromObject(allocator, provider, object, normalized, .{
            .id = firstString(stringField(object, "tool_use_id"), stringField(object, "id")),
            .name = stringField(object, "name"),
            .text_fields = &.{ "content", "text", "result" },
            .is_error = firstBool(boolField(object, "is_error"), boolField(object, "isError")),
        });
    }
}

fn appendClaudeMessage(
    allocator: std.mem.Allocator,
    provider: []const u8,
    message: std.json.Value,
    normalized: *std.ArrayList([]u8),
) !void {
    if (message != .object) return;
    const message_object = &message.object;
    if (messageObjectToolResult(message_object)) |tool| {
        try appendToolResultFromObject(allocator, provider, message_object, normalized, .{
            .id = tool.id,
            .name = tool.name,
            .text_fields = &.{ "content", "text", "result" },
            .is_error = tool.is_error,
        });
    }
    if (message_object.get("content")) |content| {
        try appendClaudeContent(allocator, provider, content, normalized);
    }
}

fn appendClaudeContent(
    allocator: std.mem.Allocator,
    provider: []const u8,
    content: std.json.Value,
    normalized: *std.ArrayList([]u8),
) !void {
    if (content == .array) {
        for (content.array.items) |part| {
            try appendClaudeContentPart(allocator, provider, part, normalized);
        }
        return;
    }
    try appendClaudeContentPart(allocator, provider, content, normalized);
}

fn appendClaudeContentPart(
    allocator: std.mem.Allocator,
    provider: []const u8,
    part: std.json.Value,
    normalized: *std.ArrayList([]u8),
) !void {
    if (part != .object) return;
    const part_object = &part.object;
    const part_type = stringField(part_object, "type") orelse "";
    if (std.mem.eql(u8, part_type, "text")) {
        if (stringField(part_object, "text")) |text| {
            if (text.len != 0) try normalized.append(allocator, try renderText(allocator, provider, .assistant_text, text));
        }
        return;
    }
    if (std.mem.eql(u8, part_type, "tool_use")) {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = stringField(part_object, "id"),
            .name = stringField(part_object, "name"),
        }));
        return;
    }
    if (std.mem.eql(u8, part_type, "tool_result")) {
        try appendToolResultFromObject(allocator, provider, part_object, normalized, .{
            .id = firstString(stringField(part_object, "tool_use_id"), stringField(part_object, "id")),
            .name = stringField(part_object, "name"),
            .text_fields = &.{ "content", "text", "result" },
            .is_error = firstBool(boolField(part_object, "is_error"), boolField(part_object, "isError")),
        });
        return;
    }
}

fn normalizeOpenCode(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
    normalized: *std.ArrayList([]u8),
) !void {
    try appendToolExecutionEvent(allocator, provider, object, normalized);
    if (normalized.items.len != 0) return;

    if (object.get("tool")) |tool_value| {
        if (tool_value != .object) return;
        const tool = &tool_value.object;
        const raw_type = firstString(stringField(object, "type"), stringField(tool, "type")) orelse "";
        if (looksLikeToolUse(raw_type)) {
            try normalized.append(allocator, try renderToolUse(allocator, provider, .{
                .id = firstString(stringField(tool, "id"), firstString(stringField(tool, "call_id"), stringField(tool, "toolCallId"))),
                .name = firstString(stringField(tool, "name"), stringField(tool, "toolName")),
            }));
            return;
        }
        if (looksLikeToolResult(raw_type)) {
            try appendToolResultFromObject(allocator, provider, tool, normalized, .{
                .id = firstString(stringField(tool, "id"), firstString(stringField(tool, "call_id"), stringField(tool, "toolCallId"))),
                .name = firstString(stringField(tool, "name"), stringField(tool, "toolName")),
                .text_fields = &.{ "result", "output", "content", "text" },
                .is_error = firstBool(boolField(tool, "isError"), boolField(tool, "is_error")),
            });
            return;
        }
    }
}

fn normalizePi(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
    normalized: *std.ArrayList([]u8),
) !void {
    try appendToolExecutionEvent(allocator, provider, object, normalized);
    if (normalized.items.len != 0) return;

    const text_kind: ?NormalizedKind = if (isFinalishType(object)) .final_result else .assistant_text;
    if (object.get("message")) |message| {
        try appendPiMessage(allocator, provider, message, text_kind, normalized);
    }
    if (object.get("messages")) |messages| {
        if (messages == .array) {
            for (messages.array.items) |message| {
                try appendPiMessage(allocator, provider, message, text_kind, normalized);
            }
        }
    }
    if (object.get("toolResults")) |tool_results| {
        try appendPiToolResults(allocator, provider, tool_results, normalized);
    }
}

fn appendPiMessage(
    allocator: std.mem.Allocator,
    provider: []const u8,
    message: std.json.Value,
    text_kind: ?NormalizedKind,
    normalized: *std.ArrayList([]u8),
) !void {
    if (message != .object) return;
    const message_object = &message.object;
    if (messageObjectToolResult(message_object)) |tool| {
        try appendToolResultFromObject(allocator, provider, message_object, normalized, .{
            .id = tool.id,
            .name = tool.name,
            .text_fields = &.{ "content", "result", "text" },
            .is_error = tool.is_error,
        });
        return;
    }

    if (objectUsage(message_object)) |usage| {
        try normalized.append(allocator, try renderTokenUsage(allocator, provider, usage));
    }

    if (message_object.get("content")) |content| {
        try appendPiContent(allocator, provider, content, text_kind, normalized);
    }
}

fn appendPiContent(
    allocator: std.mem.Allocator,
    provider: []const u8,
    content: std.json.Value,
    text_kind: ?NormalizedKind,
    normalized: *std.ArrayList([]u8),
) !void {
    if (content == .array) {
        for (content.array.items) |part| {
            try appendPiContentPart(allocator, provider, part, text_kind, normalized);
        }
        return;
    }
    try appendPiContentPart(allocator, provider, content, text_kind, normalized);
}

fn appendPiContentPart(
    allocator: std.mem.Allocator,
    provider: []const u8,
    part: std.json.Value,
    text_kind: ?NormalizedKind,
    normalized: *std.ArrayList([]u8),
) !void {
    if (part != .object) return;
    const part_object = &part.object;
    const part_type = stringField(part_object, "type") orelse "";
    if (std.mem.eql(u8, part_type, "text")) {
        if (stringField(part_object, "text")) |text| {
            if (text.len != 0) {
                if (text_kind) |kind| try normalized.append(allocator, try renderText(allocator, provider, kind, text));
            }
        }
        return;
    }
    if (std.mem.eql(u8, part_type, "toolCall")) {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = stringField(part_object, "id"),
            .name = stringField(part_object, "name"),
        }));
        return;
    }
}

fn appendPiToolResults(
    allocator: std.mem.Allocator,
    provider: []const u8,
    tool_results: std.json.Value,
    normalized: *std.ArrayList([]u8),
) !void {
    if (tool_results != .array) return;
    for (tool_results.array.items) |result| {
        if (result != .object) continue;
        const result_object = &result.object;
        if (messageObjectToolResult(result_object)) |tool| {
            try appendToolResultFromObject(allocator, provider, result_object, normalized, .{
                .id = tool.id,
                .name = tool.name,
                .text_fields = &.{ "content", "result", "text" },
                .is_error = tool.is_error,
            });
        }
    }
}

fn appendToolExecutionEvent(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
    normalized: *std.ArrayList([]u8),
) !void {
    const raw_type = stringField(object, "type") orelse "";
    if (std.mem.eql(u8, raw_type, "tool_execution_start")) {
        try normalized.append(allocator, try renderToolUse(allocator, provider, .{
            .id = stringField(object, "toolCallId"),
            .name = stringField(object, "toolName"),
        }));
        return;
    }
    if (std.mem.eql(u8, raw_type, "tool_execution_end")) {
        try appendToolResultFromObject(allocator, provider, object, normalized, .{
            .id = stringField(object, "toolCallId"),
            .name = stringField(object, "toolName"),
            .text_fields = &.{ "result", "content", "text", "output" },
            .is_error = boolField(object, "isError"),
        });
        return;
    }
}

const ToolResultFields = struct {
    id: ?[]const u8 = null,
    name: ?[]const u8 = null,
    text_fields: []const []const u8,
    is_error: ?bool = null,
};

fn appendToolResultFromObject(
    allocator: std.mem.Allocator,
    provider: []const u8,
    object: *const std.json.ObjectMap,
    normalized: *std.ArrayList([]u8),
    fields: ToolResultFields,
) !void {
    var owned_text: ?[]u8 = null;
    defer if (owned_text) |text| allocator.free(text);

    const text = try firstTextFromFields(allocator, object, fields.text_fields, &owned_text);
    try normalized.append(allocator, try renderToolResult(allocator, provider, .{
        .id = fields.id,
        .name = fields.name,
        .result_text = text,
        .is_error = fields.is_error,
    }));
}

fn tokenUsageObject(object: *const std.json.ObjectMap) ?Usage {
    return objectUsage(object);
}

fn objectUsage(object: *const std.json.ObjectMap) ?Usage {
    var usage: Usage = .{
        .input_tokens = firstInt(firstInt(intField(object, "input_tokens"), intField(object, "inputTokens")), intField(object, "input")),
        .output_tokens = firstInt(firstInt(intField(object, "output_tokens"), intField(object, "outputTokens")), intField(object, "output")),
        .total_tokens = firstInt(firstInt(intField(object, "total_tokens"), intField(object, "totalTokens")), intField(object, "total")),
        .usd = firstFloat(floatField(object, "usd"), floatField(object, "cost_usd")),
        .model = stringField(object, "model"),
    };

    if (object.get("usage")) |value| {
        if (value == .object) {
            const nested = &value.object;
            usage.input_tokens = firstInt(usage.input_tokens, firstInt(firstInt(intField(nested, "input_tokens"), intField(nested, "inputTokens")), intField(nested, "input")));
            usage.output_tokens = firstInt(usage.output_tokens, firstInt(firstInt(intField(nested, "output_tokens"), intField(nested, "outputTokens")), intField(nested, "output")));
            usage.total_tokens = firstInt(usage.total_tokens, firstInt(firstInt(intField(nested, "total_tokens"), intField(nested, "totalTokens")), intField(nested, "total")));
            usage.usd = firstFloat(usage.usd, firstFloat(floatField(nested, "usd"), floatField(nested, "cost_usd")));
            usage.model = firstString(usage.model, stringField(nested, "model"));
            if (nested.get("cost")) |cost| {
                if (cost == .object) {
                    usage.usd = firstFloat(usage.usd, floatField(&cost.object, "total"));
                }
            }
        }
    }

    if (usage.input_tokens == null and usage.output_tokens == null and
        usage.total_tokens == null and usage.usd == null)
    {
        return null;
    }
    return usage;
}

fn toolUseObject(object: *const std.json.ObjectMap) ?Tool {
    const raw_type = stringField(object, "type") orelse "";
    const looks_like_tool = std.mem.indexOf(u8, raw_type, "tool_use") != null or
        std.mem.indexOf(u8, raw_type, "tool_call") != null or
        std.mem.indexOf(u8, raw_type, "tool_execution_start") != null;
    if (!looks_like_tool) return null;

    return .{
        .id = firstString(stringField(object, "id"), firstString(stringField(object, "toolCallId"), stringField(object, "call_id"))),
        .name = firstString(stringField(object, "name"), stringField(object, "toolName")),
    };
}

fn toolResultObject(object: *const std.json.ObjectMap) ?Tool {
    const raw_type = stringField(object, "type") orelse "";
    const looks_like_tool = std.mem.indexOf(u8, raw_type, "tool_result") != null or
        std.mem.indexOf(u8, raw_type, "tool_execution_end") != null;
    if (!looks_like_tool) return null;

    return .{
        .id = firstString(stringField(object, "id"), firstString(stringField(object, "toolCallId"), stringField(object, "call_id"))),
        .name = firstString(stringField(object, "name"), stringField(object, "toolName")),
        .result_text = firstString(stringField(object, "result"), firstString(stringField(object, "content"), stringField(object, "text"))),
        .is_error = firstBool(boolField(object, "isError"), boolField(object, "is_error")),
    };
}

fn messageText(object: *const std.json.ObjectMap) ?[]const u8 {
    if (assistantTextDelta(object)) |text| return text;
    if (messageObjectText(object, "message")) |text| return text;
    if (stringField(object, "assistant_text")) |text| return text;
    if (stringField(object, "text")) |text| return text;
    if (stringField(object, "content")) |text| return text;
    if (stringField(object, "message")) |text| return text;
    if (stringField(object, "delta")) |text| return text;
    return null;
}

fn assistantTextDelta(object: *const std.json.ObjectMap) ?[]const u8 {
    const event = object.get("assistantMessageEvent") orelse return null;
    if (event != .object) return null;
    const event_object = &event.object;
    if (stringField(event_object, "delta")) |delta| return delta;
    if (stringField(event_object, "text")) |text| return text;
    return null;
}

fn messageObjectText(object: *const std.json.ObjectMap, field_name: []const u8) ?[]const u8 {
    const message = object.get(field_name) orelse return null;
    if (message != .object) return null;
    return textFromMessageValue(message);
}

fn messagesLastAssistantText(object: *const std.json.ObjectMap) ?[]const u8 {
    const messages = object.get("messages") orelse return null;
    if (messages != .array) return null;

    var result: ?[]const u8 = null;
    for (messages.array.items) |message| {
        if (textFromMessageValue(message)) |text| {
            result = text;
        }
    }
    return result;
}

fn textFromMessageValue(message: std.json.Value) ?[]const u8 {
    if (message != .object) return null;
    const object = &message.object;
    if (stringField(object, "role")) |role| {
        if (!std.mem.eql(u8, role, "assistant")) return null;
    }
    if (stringField(object, "text")) |text| return text;
    if (stringField(object, "content")) |text| return text;

    const content = object.get("content") orelse return null;
    if (content != .array) return null;
    for (content.array.items) |part| {
        if (part != .object) continue;
        const part_object = &part.object;
        if (stringField(part_object, "text")) |text| return text;
        if (stringField(part_object, "content")) |text| return text;
    }
    return null;
}

fn finalTextFromObject(object: *const std.json.ObjectMap) ?[]const u8 {
    if (stringField(object, "final_output")) |text| return text;
    if (stringField(object, "final_result")) |text| return text;
    if (stringField(object, "last_message")) |text| return text;
    if (stringField(object, "result")) |text| return text;
    if (stringField(object, "response")) |text| return text;
    return null;
}

fn isFinalishType(object: *const std.json.ObjectMap) bool {
    const raw_type = stringField(object, "type") orelse return false;
    return std.mem.eql(u8, raw_type, "result") or
        std.mem.eql(u8, raw_type, "final_result") or
        std.mem.eql(u8, raw_type, "agent_end") or
        std.mem.eql(u8, raw_type, "turn_end");
}

fn isStatusType(raw_type: []const u8) bool {
    return std.mem.eql(u8, raw_type, "session") or
        std.mem.eql(u8, raw_type, "agent_start") or
        std.mem.eql(u8, raw_type, "agent_end") or
        std.mem.eql(u8, raw_type, "turn_start") or
        std.mem.eql(u8, raw_type, "turn_end") or
        std.mem.eql(u8, raw_type, "queue_update") or
        std.mem.eql(u8, raw_type, "compaction_start") or
        std.mem.eql(u8, raw_type, "compaction_end") or
        std.mem.eql(u8, raw_type, "auto_retry_start") or
        std.mem.eql(u8, raw_type, "auto_retry_end");
}

fn renderText(
    allocator: std.mem.Allocator,
    provider: []const u8,
    kind: NormalizedKind,
    text: []const u8,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "type", @tagName(kind));
    try field(&json, "provider", provider);
    try field(&json, "text", text);
    try json.endObject();
    return out.toOwnedSlice();
}

fn renderStatus(
    allocator: std.mem.Allocator,
    provider: []const u8,
    status: []const u8,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "type", "status");
    try field(&json, "provider", provider);
    try field(&json, "status", status);
    try json.endObject();
    return out.toOwnedSlice();
}

fn renderTokenUsage(
    allocator: std.mem.Allocator,
    provider: []const u8,
    usage: Usage,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "type", "token_usage");
    try field(&json, "provider", provider);
    try field(&json, "input_tokens", usage.input_tokens);
    try field(&json, "output_tokens", usage.output_tokens);
    try field(&json, "total_tokens", usage.total_tokens);
    try field(&json, "usd", usage.usd);
    try field(&json, "model", usage.model);
    try json.endObject();
    return out.toOwnedSlice();
}

fn renderToolUse(
    allocator: std.mem.Allocator,
    provider: []const u8,
    tool: Tool,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "type", "tool_use");
    try field(&json, "provider", provider);
    try field(&json, "id", tool.id);
    try field(&json, "name", tool.name);
    try json.endObject();
    return out.toOwnedSlice();
}

fn renderToolResult(
    allocator: std.mem.Allocator,
    provider: []const u8,
    tool: Tool,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "type", "tool_result");
    try field(&json, "provider", provider);
    try field(&json, "id", tool.id);
    try field(&json, "name", tool.name);
    try field(&json, "text", tool.result_text);
    try field(&json, "is_error", tool.is_error);
    try json.endObject();
    return out.toOwnedSlice();
}

fn field(json: *std.json.Stringify, name: []const u8, value: anytype) !void {
    try json.objectField(name);
    try json.write(value);
}

fn isProvider(provider: []const u8, expected: []const u8) bool {
    return std.ascii.eqlIgnoreCase(provider, expected);
}

fn looksLikeToolUse(raw_type: []const u8) bool {
    return std.mem.indexOf(u8, raw_type, "tool_use") != null or
        std.mem.indexOf(u8, raw_type, "tool_call") != null or
        std.mem.indexOf(u8, raw_type, "tool_execution_start") != null;
}

fn looksLikeToolResult(raw_type: []const u8) bool {
    return std.mem.indexOf(u8, raw_type, "tool_result") != null or
        std.mem.indexOf(u8, raw_type, "tool_execution_end") != null;
}

fn messageObjectToolResult(object: *const std.json.ObjectMap) ?Tool {
    if (stringField(object, "role")) |role| {
        if (!std.mem.eql(u8, role, "toolResult") and !std.mem.eql(u8, role, "tool")) return null;
    } else {
        const raw_type = stringField(object, "type") orelse return null;
        if (!std.mem.eql(u8, raw_type, "tool_result") and !std.mem.eql(u8, raw_type, "toolResult")) return null;
    }

    return .{
        .id = firstString(stringField(object, "toolCallId"), firstString(stringField(object, "tool_use_id"), stringField(object, "id"))),
        .name = firstString(stringField(object, "toolName"), stringField(object, "name")),
        .is_error = firstBool(boolField(object, "isError"), boolField(object, "is_error")),
    };
}

fn codexToolNameForType(raw_type: []const u8) ?[]const u8 {
    if (std.mem.eql(u8, raw_type, "local_shell_call")) return "local_shell";
    if (std.mem.eql(u8, raw_type, "tool_search_call")) return "tool_search";
    if (std.mem.eql(u8, raw_type, "dynamic_tool_call")) return "dynamic_tool";
    if (std.mem.eql(u8, raw_type, "custom_tool_call")) return "custom_tool";
    if (std.mem.eql(u8, raw_type, "function_call")) return "function";
    if (std.mem.eql(u8, raw_type, "function_call_output")) return "function";
    if (std.mem.eql(u8, raw_type, "tool_call_output")) return "tool";
    if (std.mem.eql(u8, raw_type, "tool_call_response")) return "tool";
    return null;
}

fn codexExecIsError(object: *const std.json.ObjectMap) ?bool {
    if (boolField(object, "is_error")) |value| return value;
    if (boolField(object, "isError")) |value| return value;
    if (intField(object, "exit_code")) |exit_code| return exit_code != 0;
    if (intField(object, "exitCode")) |exit_code| return exit_code != 0;
    return null;
}

fn firstTextFromFields(
    allocator: std.mem.Allocator,
    object: *const std.json.ObjectMap,
    names: []const []const u8,
    owned_text: *?[]u8,
) !?[]const u8 {
    for (names) |name| {
        const value = object.get(name) orelse continue;
        if (try textFromValueAlloc(allocator, value, owned_text)) |text| return text;
    }
    return null;
}

fn textFromValueAlloc(
    allocator: std.mem.Allocator,
    value: std.json.Value,
    owned_text: *?[]u8,
) !?[]const u8 {
    switch (value) {
        .string => |text| return text,
        .number_string => |text| return text,
        .array => |array| {
            var out: std.io.Writer.Allocating = .init(allocator);
            errdefer out.deinit();
            var wrote = false;
            for (array.items) |item| {
                if (item == .object) {
                    const item_object = &item.object;
                    if (stringField(item_object, "text")) |text| {
                        if (wrote) try out.writer.writeAll("\n");
                        try out.writer.writeAll(text);
                        wrote = true;
                        continue;
                    }
                    if (stringField(item_object, "content")) |text| {
                        if (wrote) try out.writer.writeAll("\n");
                        try out.writer.writeAll(text);
                        wrote = true;
                        continue;
                    }
                }
                const rendered = try renderValueAlloc(allocator, item);
                defer allocator.free(rendered);
                if (wrote) try out.writer.writeAll("\n");
                try out.writer.writeAll(rendered);
                wrote = true;
            }
            if (!wrote) return null;
            owned_text.* = try out.toOwnedSlice();
            return owned_text.*.?;
        },
        .object, .integer, .float, .bool, .null => {
            owned_text.* = try renderValueAlloc(allocator, value);
            return owned_text.*.?;
        },
    }
}

fn renderValueAlloc(allocator: std.mem.Allocator, value: std.json.Value) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.write(value);
    return out.toOwnedSlice();
}

fn stringField(object: *const std.json.ObjectMap, name: []const u8) ?[]const u8 {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .string => |text| text,
        else => null,
    };
}

fn boolField(object: *const std.json.ObjectMap, name: []const u8) ?bool {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .bool => |b| b,
        else => null,
    };
}

fn intField(object: *const std.json.ObjectMap, name: []const u8) ?i64 {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .integer => |n| n,
        .float => |n| @intFromFloat(n),
        else => null,
    };
}

fn floatField(object: *const std.json.ObjectMap, name: []const u8) ?f64 {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .integer => |n| @floatFromInt(n),
        .float => |n| n,
        else => null,
    };
}

fn firstInt(a: ?i64, b: ?i64) ?i64 {
    return if (a) |value| value else b;
}

fn firstFloat(a: ?f64, b: ?f64) ?f64 {
    return if (a) |value| value else b;
}

fn firstString(a: ?[]const u8, b: ?[]const u8) ?[]const u8 {
    return if (a) |value| value else b;
}

fn firstBool(a: ?bool, b: ?bool) ?bool {
    return if (a) |value| value else b;
}

fn replaceCandidate(
    allocator: std.mem.Allocator,
    candidate: *?[]u8,
    text: []const u8,
) !void {
    if (candidate.*) |owned| allocator.free(owned);
    candidate.* = try allocator.dupe(u8, text);
}

test "normalizes pi json message and final result events" {
    var recorder: Recorder = .{};
    try emitNormalizedJsonl(std.testing.allocator, recorder.sink(), .{ .value = "pi" },
        \\{"type":"message_update","message":{"role":"assistant","content":[]},"assistantMessageEvent":{"type":"text_delta","delta":"Hello"}}
        \\{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"Done"}]}]}
        \\
    );

    try std.testing.expectEqual(@as(usize, 2), recorder.count);
    try std.testing.expect(recorder.saw_assistant_text);
    try std.testing.expect(recorder.saw_final_result);
}

test "normalizes tool execution and token usage" {
    var recorder: Recorder = .{};
    try emitNormalizedJsonl(std.testing.allocator, recorder.sink(), .{ .value = "opencode" },
        \\{"type":"tool_execution_start","toolCallId":"1","toolName":"bash","args":{}}
        \\{"type":"tool_execution_end","toolCallId":"1","toolName":"bash","result":"ok","isError":false}
        \\{"type":"token_usage","usage":{"inputTokens":10,"outputTokens":4},"model":"x"}
        \\
    );

    try std.testing.expectEqual(@as(usize, 3), recorder.count);
    try std.testing.expect(recorder.saw_tool_use);
    try std.testing.expect(recorder.saw_tool_result);
    try std.testing.expect(recorder.saw_token_usage);
}

test "normalizes codex response tool call events" {
    var recorder: Recorder = .{};
    try emitNormalizedJsonl(std.testing.allocator, recorder.sink(), .{ .value = "codex" },
        \\{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"ls\"}"}}
        \\{"type":"response.output_item.done","item":{"type":"function_call_output","call_id":"call_1","output":"ok"}}
        \\
    );

    try std.testing.expectEqual(@as(usize, 2), recorder.count);
    try std.testing.expect(recorder.saw_tool_use);
    try std.testing.expect(recorder.saw_tool_result);
    try std.testing.expect(recorder.saw_shell_tool);
    try std.testing.expect(recorder.saw_tool_text_ok);
}

test "normalizes codex exec command lifecycle" {
    var recorder: Recorder = .{};
    try emitNormalizedJsonl(std.testing.allocator, recorder.sink(), .{ .value = "codex" },
        \\{"type":"exec_command_begin","call_id":"exec_1","command":"mix test"}
        \\{"type":"exec_command_end","call_id":"exec_1","output":"failed","exit_code":1}
        \\
    );

    try std.testing.expectEqual(@as(usize, 2), recorder.count);
    try std.testing.expect(recorder.saw_tool_use);
    try std.testing.expect(recorder.saw_tool_result);
    try std.testing.expect(recorder.saw_exec_tool);
    try std.testing.expect(recorder.saw_tool_error);
}

test "normalizes claude content block tool use and result" {
    var recorder: Recorder = .{};
    try emitNormalizedJsonl(std.testing.allocator, recorder.sink(), .{ .value = "claudecode" },
        \\{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll inspect it."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]}}
        \\{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok","is_error":false}]}}
        \\
    );

    try std.testing.expectEqual(@as(usize, 3), recorder.count);
    try std.testing.expect(recorder.saw_assistant_text);
    try std.testing.expect(recorder.saw_tool_use);
    try std.testing.expect(recorder.saw_tool_result);
    try std.testing.expect(recorder.saw_bash_tool);
}

test "normalizes pi session tool calls results and usage" {
    var recorder: Recorder = .{};
    try emitNormalizedJsonl(std.testing.allocator, recorder.sink(), .{ .value = "pi" },
        \\{"type":"turn_end","message":{"role":"assistant","content":[{"type":"toolCall","id":"tool_1","name":"read","arguments":{"path":"README.md"}},{"type":"text","text":"Done"}],"usage":{"input":12,"output":5,"totalTokens":17,"cost":{"total":0.01}}},"toolResults":[{"role":"toolResult","toolCallId":"tool_1","toolName":"read","content":"contents","isError":false}]}
        \\
    );

    try std.testing.expectEqual(@as(usize, 4), recorder.count);
    try std.testing.expect(recorder.saw_final_result);
    try std.testing.expect(recorder.saw_tool_use);
    try std.testing.expect(recorder.saw_tool_result);
    try std.testing.expect(recorder.saw_token_usage);
    try std.testing.expect(recorder.saw_read_tool);
}

test "extracts nested final result from provider streams" {
    const final = (try extractFinalOutputAlloc(std.testing.allocator,
        \\{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"final"}]}]}
        \\
    )).?;
    defer std.testing.allocator.free(final);

    try std.testing.expectEqualStrings("final", final);
}

const Recorder = struct {
    count: usize = 0,
    saw_assistant_text: bool = false,
    saw_final_result: bool = false,
    saw_tool_use: bool = false,
    saw_tool_result: bool = false,
    saw_token_usage: bool = false,
    saw_bash_tool: bool = false,
    saw_shell_tool: bool = false,
    saw_exec_tool: bool = false,
    saw_read_tool: bool = false,
    saw_tool_error: bool = false,
    saw_tool_text_ok: bool = false,

    fn sink(self: *Recorder) adapters.AdapterEventSink {
        return .{ .context = self, .emit = emit };
    }

    fn emit(context: *anyopaque, event: adapters.AdapterEvent) anyerror!void {
        const self: *Recorder = @ptrCast(@alignCast(context));
        switch (event) {
            .structured => |bytes| {
                self.count += 1;
                if (std.mem.indexOf(u8, bytes, "\"type\":\"assistant_text\"") != null) self.saw_assistant_text = true;
                if (std.mem.indexOf(u8, bytes, "\"type\":\"final_result\"") != null) self.saw_final_result = true;
                if (std.mem.indexOf(u8, bytes, "\"type\":\"tool_use\"") != null) self.saw_tool_use = true;
                if (std.mem.indexOf(u8, bytes, "\"type\":\"tool_result\"") != null) self.saw_tool_result = true;
                if (std.mem.indexOf(u8, bytes, "\"type\":\"token_usage\"") != null) self.saw_token_usage = true;
                if (std.mem.indexOf(u8, bytes, "\"name\":\"Bash\"") != null) self.saw_bash_tool = true;
                if (std.mem.indexOf(u8, bytes, "\"name\":\"shell\"") != null) self.saw_shell_tool = true;
                if (std.mem.indexOf(u8, bytes, "\"name\":\"exec_command\"") != null) self.saw_exec_tool = true;
                if (std.mem.indexOf(u8, bytes, "\"name\":\"read\"") != null) self.saw_read_tool = true;
                if (std.mem.indexOf(u8, bytes, "\"is_error\":true") != null) self.saw_tool_error = true;
                if (std.mem.indexOf(u8, bytes, "\"text\":\"ok\"") != null) self.saw_tool_text_ok = true;
            },
            else => {},
        }
    }
};
