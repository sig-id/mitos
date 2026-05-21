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

        const token_redacted = try redactLikelySecrets(allocator, current);
        allocator.free(current);
        current = token_redacted;

        return current;
    }
};

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
    if (std.mem.startsWith(u8, token, "ghp_")) return true;
    if (std.mem.startsWith(u8, token, "github_pat_")) return true;
    if (std.mem.startsWith(u8, token, "xoxb-")) return true;
    if (std.mem.startsWith(u8, token, "AKIA") and token.len >= 16) return true;
    return false;
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
        "token sk-test-1234567890 and ghp_abcdefghijklmnopqrstuvwxyz",
    );
    defer allocator.free(output);

    try std.testing.expectEqualStrings("token [REDACTED] and [REDACTED]", output);
}
