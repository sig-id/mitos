const std = @import("std");

pub const RedactionScope = enum {
    terminal_text,
    provider_event,
    report,
};

pub const ScopeSet = packed struct {
    terminal_text: bool = true,
    provider_event: bool = true,
    report: bool = true,

    pub fn includes(self: ScopeSet, scope: RedactionScope) bool {
        return switch (scope) {
            .terminal_text => self.terminal_text,
            .provider_event => self.provider_event,
            .report => self.report,
        };
    }
};

pub const RedactionRule = struct {
    id: []const u8,
    pattern: []const u8,
    replacement: []const u8 = "[REDACTED]",
    scopes: ScopeSet = .{},
};

pub const Redactor = struct {
    rules: []const RedactionRule = &.{},

    pub fn redactAlloc(
        self: Redactor,
        allocator: std.mem.Allocator,
        scope: RedactionScope,
        input: []const u8,
    ) ![]u8 {
        var current = try allocator.dupe(u8, input);
        errdefer allocator.free(current);

        for (self.rules) |rule| {
            if (rule.pattern.len == 0 or !rule.scopes.includes(scope)) continue;

            const next = try std.mem.replaceOwned(
                u8,
                allocator,
                current,
                rule.pattern,
                rule.replacement,
            );
            allocator.free(current);
            current = next;
        }

        const assignment_redacted = try redactSecretAssignments(allocator, current);
        allocator.free(current);
        current = assignment_redacted;

        const token_redacted = try redactLikelySecrets(allocator, current);
        allocator.free(current);
        current = token_redacted;

        return current;
    }
};

fn redactSecretAssignments(allocator: std.mem.Allocator, input: []const u8) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();

    var index: usize = 0;
    while (index < input.len) {
        if (input[index] == '"' or input[index] == '\'') {
            if (try redactQuotedSecretAssignment(allocator, &out, input, &index)) {
                continue;
            }
        }

        if (!isKeyChar(input[index])) {
            try out.writer.writeByte(input[index]);
            index += 1;
            continue;
        }

        const key_start = index;
        var key_end = index;
        while (key_end < input.len and isKeyChar(input[key_end])) : (key_end += 1) {}

        var sep_index = key_end;
        while (sep_index < input.len and isInlineSpace(input[sep_index])) : (sep_index += 1) {}

        if (sep_index < input.len and isAssignmentSeparator(input[sep_index]) and isSensitiveKey(input[key_start..key_end])) {
            var value_start = sep_index + 1;
            while (value_start < input.len and isInlineSpace(input[value_start])) : (value_start += 1) {}

            const key = input[key_start..key_end];
            const value_end = secretAssignmentValueEnd(input, value_start, key);
            if (value_end > value_start) {
                try out.writer.writeAll(input[key_start..value_start]);
                try out.writer.writeAll("[REDACTED]");
                index = value_end;
                continue;
            }
        }

        try out.writer.writeAll(input[key_start..key_end]);
        index = key_end;
    }

    return out.toOwnedSlice();
}

fn redactQuotedSecretAssignment(
    allocator: std.mem.Allocator,
    out: *std.io.Writer.Allocating,
    input: []const u8,
    index: *usize,
) !bool {
    _ = allocator;
    const quote = input[index.*];
    const key_start = index.* + 1;
    var key_end = key_start;
    while (key_end < input.len and isKeyChar(input[key_end])) : (key_end += 1) {}

    if (key_end == key_start or key_end >= input.len or input[key_end] != quote) return false;

    const key = input[key_start..key_end];
    if (!isSensitiveKey(key)) return false;

    var sep_index = key_end + 1;
    while (sep_index < input.len and isInlineSpace(input[sep_index])) : (sep_index += 1) {}
    if (sep_index >= input.len or !isAssignmentSeparator(input[sep_index])) return false;

    var value_start = sep_index + 1;
    while (value_start < input.len and isInlineSpace(input[value_start])) : (value_start += 1) {}

    const value_end = secretAssignmentValueEnd(input, value_start, key);
    if (value_end <= value_start) return false;

    try out.writer.writeAll(input[index.*..value_start]);
    try out.writer.writeAll("[REDACTED]");
    index.* = value_end;
    return true;
}

fn redactLikelySecrets(allocator: std.mem.Allocator, input: []const u8) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();

    var index: usize = 0;
    while (index < input.len) {
        if (!isTokenChar(input[index])) {
            try out.writer.writeByte(input[index]);
            index += 1;
            continue;
        }

        const start = index;
        while (index < input.len and isTokenChar(input[index])) : (index += 1) {}
        const token = input[start..index];
        if (isLikelySecretToken(token)) {
            try out.writer.writeAll("[REDACTED]");
        } else {
            try out.writer.writeAll(token);
        }
    }

    return out.toOwnedSlice();
}

fn isLikelySecretToken(token: []const u8) bool {
    if (token.len < 12) return false;
    if (std.mem.startsWith(u8, token, "sk-")) return true;
    if (std.mem.startsWith(u8, token, "sk_live_")) return true;
    if (std.mem.startsWith(u8, token, "sk_test_")) return true;
    if (std.mem.startsWith(u8, token, "rk_live_")) return true;
    if (std.mem.startsWith(u8, token, "ghp_")) return true;
    if (std.mem.startsWith(u8, token, "gho_")) return true;
    if (std.mem.startsWith(u8, token, "ghu_")) return true;
    if (std.mem.startsWith(u8, token, "ghs_")) return true;
    if (std.mem.startsWith(u8, token, "ghr_")) return true;
    if (std.mem.startsWith(u8, token, "github_pat_")) return true;
    if (std.mem.startsWith(u8, token, "xoxb-")) return true;
    if (std.mem.startsWith(u8, token, "xoxp-")) return true;
    if (std.mem.startsWith(u8, token, "xoxa-")) return true;
    if (std.mem.startsWith(u8, token, "xoxc-")) return true;
    if (std.mem.startsWith(u8, token, "xapp-")) return true;
    if (std.mem.startsWith(u8, token, "glpat-")) return true;
    if (std.mem.startsWith(u8, token, "ya29.")) return true;
    if (std.mem.startsWith(u8, token, "AKIA") and token.len >= 16) return true;
    return false;
}

fn isSensitiveKey(key: []const u8) bool {
    return asciiContainsIgnoreCase(key, "api_key") or
        asciiContainsIgnoreCase(key, "apikey") or
        asciiContainsIgnoreCase(key, "authorization") or
        asciiContainsIgnoreCase(key, "bearer") or
        asciiContainsIgnoreCase(key, "client_secret") or
        asciiContainsIgnoreCase(key, "credential") or
        asciiContainsIgnoreCase(key, "password") or
        asciiContainsIgnoreCase(key, "passwd") or
        asciiContainsIgnoreCase(key, "private_key") or
        asciiContainsIgnoreCase(key, "refresh_token") or
        asciiContainsIgnoreCase(key, "secret") or
        asciiContainsIgnoreCase(key, "token");
}

fn asciiContainsIgnoreCase(haystack: []const u8, needle: []const u8) bool {
    if (needle.len == 0 or needle.len > haystack.len) return false;

    var start: usize = 0;
    while (start + needle.len <= haystack.len) : (start += 1) {
        var matched = true;
        for (needle, 0..) |needle_char, offset| {
            if (std.ascii.toLower(haystack[start + offset]) != std.ascii.toLower(needle_char)) {
                matched = false;
                break;
            }
        }
        if (matched) return true;
    }

    return false;
}

fn secretAssignmentValueEnd(input: []const u8, start: usize, key: []const u8) usize {
    if (start >= input.len) return start;

    if (input[start] == '"' or input[start] == '\'') {
        const quote = input[start];
        var index = start + 1;
        while (index < input.len) : (index += 1) {
            if (input[index] == quote) return index + 1;
        }
        return input.len;
    }

    var index = start;
    while (index < input.len and !isSecretValueTerminator(input[index], key)) : (index += 1) {}
    return index;
}

fn isSecretValueTerminator(byte: u8, key: []const u8) bool {
    if (isAuthorizationKey(key)) {
        return byte == '\n' or byte == '\r' or byte == ',' or byte == ';';
    }

    return std.ascii.isWhitespace(byte) or byte == ',' or byte == ';';
}

fn isAuthorizationKey(key: []const u8) bool {
    return asciiContainsIgnoreCase(key, "authorization") or asciiContainsIgnoreCase(key, "bearer");
}

fn isInlineSpace(byte: u8) bool {
    return byte == ' ' or byte == '\t';
}

fn isAssignmentSeparator(byte: u8) bool {
    return byte == '=' or byte == ':';
}

fn isKeyChar(byte: u8) bool {
    return std.ascii.isAlphanumeric(byte) or byte == '_' or byte == '-';
}

fn isTokenChar(byte: u8) bool {
    return std.ascii.isAlphanumeric(byte) or byte == '_' or byte == '-' or byte == '.';
}

test "redactor applies ordered literal replacements by scope" {
    const allocator = std.testing.allocator;
    const rules = [_]RedactionRule{
        .{ .id = "api-key", .pattern = "sk-test-123", .replacement = "[KEY]" },
        .{
            .id = "terminal-only",
            .pattern = "terminal-secret",
            .replacement = "[TERM]",
            .scopes = .{ .provider_event = false, .report = false },
        },
    };
    const redactor: Redactor = .{ .rules = &rules };

    const provider = try redactor.redactAlloc(
        allocator,
        .provider_event,
        "using sk-test-123 and terminal-secret",
    );
    defer allocator.free(provider);
    try std.testing.expectEqualStrings("using [KEY] and terminal-secret", provider);

    const terminal = try redactor.redactAlloc(
        allocator,
        .terminal_text,
        "using sk-test-123 and terminal-secret",
    );
    defer allocator.free(terminal);
    try std.testing.expectEqualStrings("using [KEY] and [TERM]", terminal);
}

test "redactor masks common secret-looking tokens without explicit rules" {
    const allocator = std.testing.allocator;
    const redactor: Redactor = .{};

    const output = try redactor.redactAlloc(
        allocator,
        .report,
        "token sk-test-1234567890 and ghp_abcdefghijklmnopqrstuvwxyz and glpat-abcdefghijklmnop and xoxp-abcdefghijklmnop",
    );
    defer allocator.free(output);

    try std.testing.expectEqualStrings(
        "token [REDACTED] and [REDACTED] and [REDACTED] and [REDACTED]",
        output,
    );
}

test "redactor masks sensitive assignment values without explicit rules" {
    const allocator = std.testing.allocator;
    const redactor: Redactor = .{};

    const output = try redactor.redactAlloc(
        allocator,
        .terminal_text,
        "API_KEY=plainsecret password: hunter2 normal=value token = \"quoted secret\"",
    );
    defer allocator.free(output);

    try std.testing.expectEqualStrings(
        "API_KEY=[REDACTED] password: [REDACTED] normal=value token = [REDACTED]",
        output,
    );
}

test "redactor masks quoted sensitive fields and full authorization headers" {
    const allocator = std.testing.allocator;
    const redactor: Redactor = .{};

    const output = try redactor.redactAlloc(
        allocator,
        .report,
        "{\"api_key\":\"plainsecret\",\"nested\":{\"client_secret\":\"quoted secret\",\"keep\":\"visible\"}}\nAuthorization: Bearer plain-secret-token\n",
    );
    defer allocator.free(output);

    try std.testing.expect(!std.mem.containsAtLeast(u8, output, 1, "plainsecret"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, output, 1, "quoted secret"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, output, 1, "plain-secret-token"));
    try std.testing.expect(std.mem.containsAtLeast(u8, output, 3, "[REDACTED]"));
    try std.testing.expect(std.mem.containsAtLeast(u8, output, 1, "\"keep\":\"visible\""));
}
