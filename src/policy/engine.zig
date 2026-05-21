const std = @import("std");
const config = @import("../config/types.zig");
const runtime = @import("../runtime/types.zig");
const policy = @import("types.zig");

pub const RuleEffect = enum {
    allow,
    ask,
    block,
};

pub const Rule = struct {
    id: policy.RuleId,
    effect: RuleEffect,
    action_kind: ?policy.ActionKind = null,
    path_prefix: ?[]const u8 = null,
    command_prefix: ?[]const u8 = null,
    network_destination: ?[]const u8 = null,
    reason: []const u8 = "",

    pub fn matches(self: Rule, action: policy.Action) bool {
        if (self.action_kind) |kind| {
            if (kind != action.kind()) return false;
        }

        if (self.path_prefix) |prefix| {
            const path = actionPath(action) orelse return false;
            if (!std.mem.startsWith(u8, path, prefix)) return false;
        }

        if (self.command_prefix) |prefix| {
            const command = switch (action) {
                .run_command => |command_action| command_action,
                else => return false,
            };
            if (!commandMatches(command, prefix)) return false;
        }

        if (self.network_destination) |expected| {
            const destination = actionNetworkDestination(action) orelse return false;
            if (!std.mem.eql(u8, destination, expected)) return false;
        }

        return true;
    }
};

pub const PolicyConfig = struct {
    default_effect: RuleEffect = .ask,
    default_reason: []const u8 = "default policy",
    rules: []const Rule = &.{},
};

pub const OwnedPolicyConfig = struct {
    allocator: std.mem.Allocator,
    config: PolicyConfig,

    pub fn deinit(self: *OwnedPolicyConfig) void {
        self.allocator.free(self.config.default_reason);
        for (self.config.rules) |rule| {
            deinitOwnedRule(self.allocator, rule);
        }
        if (self.config.rules.len > 0) self.allocator.free(self.config.rules);
        self.* = undefined;
    }
};

pub const AuditedDecision = struct {
    action_kind: policy.ActionKind,
    decision: policy.PolicyDecision,
    rule_id: ?policy.RuleId,
    reason: []const u8,
    matched_default: bool,

    pub fn decisionName(self: AuditedDecision) []const u8 {
        return self.decision.tagName();
    }
};

pub const DeterministicPolicy = struct {
    config: PolicyConfig = .{},

    pub fn asPolicyEngine(self: *DeterministicPolicy) policy.PolicyEngine {
        return .{
            .context = self,
            .evaluate = evaluateBridge,
        };
    }

    pub fn evaluate(
        self: *DeterministicPolicy,
        action: policy.Action,
        context: policy.PolicyContext,
    ) policy.PolicyDecision {
        return self.evaluateAudited(action, context).decision;
    }

    pub fn evaluateAudited(
        self: *DeterministicPolicy,
        action: policy.Action,
        context: policy.PolicyContext,
    ) AuditedDecision {
        _ = context;

        if (self.firstMatchingRule(.block, action)) |rule| {
            return audited(action, rule.effect, rule.id, rule.reason, false);
        }
        if (self.firstMatchingRule(.ask, action)) |rule| {
            return audited(action, rule.effect, rule.id, rule.reason, false);
        }
        if (self.firstMatchingRule(.allow, action)) |rule| {
            return audited(action, rule.effect, rule.id, rule.reason, false);
        }

        return audited(
            action,
            self.config.default_effect,
            .{ .value = "default" },
            self.config.default_reason,
            true,
        );
    }

    fn firstMatchingRule(
        self: *DeterministicPolicy,
        effect: RuleEffect,
        action: policy.Action,
    ) ?Rule {
        for (self.config.rules) |rule| {
            if (rule.effect == effect and rule.matches(action)) return rule;
        }
        return null;
    }
};

pub fn fromProjectPolicy(
    allocator: std.mem.Allocator,
    project_policy: config.PolicyConfig,
) !OwnedPolicyConfig {
    const total = project_policy.allow.len + project_policy.ask.len + project_policy.block.len;
    var rules: std.ArrayList(Rule) = try .initCapacity(allocator, total);
    errdefer {
        for (rules.items) |rule| {
            deinitOwnedRule(allocator, rule);
        }
        rules.deinit(allocator);
    }
    const default_reason = try allocator.dupe(u8, "project policy default");
    errdefer allocator.free(default_reason);

    try appendProjectRules(allocator, &rules, .allow, project_policy.allow);
    try appendProjectRules(allocator, &rules, .ask, project_policy.ask);
    try appendProjectRules(allocator, &rules, .block, project_policy.block);

    return .{
        .allocator = allocator,
        .config = .{
            .default_effect = effectFromDisposition(project_policy.default),
            .default_reason = default_reason,
            .rules = try rules.toOwnedSlice(allocator),
        },
    };
}

fn evaluateBridge(
    engine: *policy.PolicyEngine,
    action: policy.Action,
    context: policy.PolicyContext,
) policy.PolicyDecision {
    const self: *DeterministicPolicy = @ptrCast(@alignCast(engine.context));
    return self.evaluate(action, context);
}

fn appendProjectRules(
    allocator: std.mem.Allocator,
    rules: *std.ArrayList(Rule),
    effect: RuleEffect,
    project_rules: []const config.PolicyRule,
) !void {
    for (project_rules, 0..) |project_rule, local_index| {
        const rule = try ownedRuleFromProjectRule(
            allocator,
            effect,
            local_index,
            project_rule,
        );
        rules.append(allocator, rule) catch |err| {
            deinitOwnedRule(allocator, rule);
            return err;
        };
    }
}

fn ownedRuleFromProjectRule(
    allocator: std.mem.Allocator,
    effect: RuleEffect,
    local_index: usize,
    project_rule: config.PolicyRule,
) !Rule {
    const id = try std.fmt.allocPrint(
        allocator,
        "{s}-{d}",
        .{ @tagName(effect), local_index },
    );
    errdefer allocator.free(id);

    const reason = try allocator.dupe(u8, "project policy rule");
    errdefer allocator.free(reason);

    var rule: Rule = .{
        .id = .{ .value = id },
        .effect = effect,
        .reason = reason,
    };

    switch (project_rule.kind) {
        .command => {
            rule.action_kind = .run_command;
            rule.command_prefix = try allocator.dupe(u8, project_rule.value);
        },
        .path => {
            rule.path_prefix = try allocator.dupe(u8, project_rule.value);
        },
        .action => {
            try applyProjectAction(allocator, &rule, project_rule.value);
        },
    }
    return rule;
}

fn deinitOwnedRule(allocator: std.mem.Allocator, rule: Rule) void {
    allocator.free(rule.id.value);
    if (rule.path_prefix) |value| allocator.free(value);
    if (rule.command_prefix) |value| allocator.free(value);
    if (rule.network_destination) |value| allocator.free(value);
    allocator.free(rule.reason);
}

fn applyProjectAction(allocator: std.mem.Allocator, rule: *Rule, action: []const u8) !void {
    if (std.mem.eql(u8, action, "read_workspace")) {
        rule.action_kind = .read_path;
        rule.path_prefix = try allocator.dupe(u8, "/workspace");
    } else if (std.mem.eql(u8, action, "write_workspace")) {
        rule.action_kind = .write_path;
        rule.path_prefix = try allocator.dupe(u8, "/workspace");
    } else if (std.mem.eql(u8, action, "run_tests")) {
        rule.action_kind = .run_command;
        rule.command_prefix = try allocator.dupe(u8, "zig build test*");
    } else if (std.mem.eql(u8, action, "install_package")) {
        rule.action_kind = .install_package;
    } else if (std.mem.eql(u8, action, "network_access")) {
        rule.action_kind = .network_access;
    } else if (std.mem.eql(u8, action, "git_push")) {
        rule.action_kind = .git_push;
    } else if (std.mem.eql(u8, action, "write_outside_workspace")) {
        rule.action_kind = .write_path;
    } else {
        return error.InvalidProjectPolicyRule;
    }
}

fn effectFromDisposition(disposition: config.PolicyDisposition) RuleEffect {
    return switch (disposition) {
        .allow => .allow,
        .ask => .ask,
        .block => .block,
    };
}

fn audited(
    action: policy.Action,
    effect: RuleEffect,
    rule_id: policy.RuleId,
    reason: []const u8,
    matched_default: bool,
) AuditedDecision {
    const decision: policy.PolicyDecision = switch (effect) {
        .allow => .{ .allow = .{ .rule_id = rule_id } },
        .ask => .{ .ask = .{
            .rule_id = if (matched_default) null else rule_id,
            .reason = reason,
        } },
        .block => .{ .block = .{
            .rule_id = if (matched_default) null else rule_id,
            .reason = reason,
        } },
    };

    return .{
        .action_kind = action.kind(),
        .decision = decision,
        .rule_id = if (matched_default) null else rule_id,
        .reason = reason,
        .matched_default = matched_default,
    };
}

fn actionPath(action: policy.Action) ?[]const u8 {
    return switch (action) {
        .read_path => |path| path,
        .write_path => |path| path,
        .delete_path => |path| path,
        else => null,
    };
}

fn actionNetworkDestination(action: policy.Action) ?[]const u8 {
    return switch (action) {
        .network_access => |access| access.destination,
        else => null,
    };
}

fn commandMatches(command: policy.CommandAction, pattern: []const u8) bool {
    const normalized = if (std.mem.endsWith(u8, pattern, "*"))
        std.mem.trimRight(u8, pattern[0 .. pattern.len - 1], " \t")
    else
        pattern;
    if (normalized.len == 0) return true;

    var pattern_index: usize = 0;
    for (command.argv, 0..) |arg, arg_index| {
        if (arg_index > 0) {
            if (pattern_index >= normalized.len) return true;
            if (normalized[pattern_index] != ' ') return false;
            pattern_index += 1;
            while (pattern_index < normalized.len and normalized[pattern_index] == ' ') {
                pattern_index += 1;
            }
        }

        if (pattern_index >= normalized.len) return true;
        const remaining = normalized[pattern_index..];
        if (arg.len > remaining.len) {
            return std.mem.startsWith(u8, arg, remaining);
        }
        if (!std.mem.eql(u8, remaining[0..arg.len], arg)) return false;
        pattern_index += arg.len;
    }

    return pattern_index == normalized.len;
}

test "policy evaluation is deterministic and block rules win" {
    const run_id = try runtime.RunId.init("01HXYZ");
    const rules = [_]Rule{
        .{
            .id = try policy.RuleId.init("allow-zig"),
            .effect = .allow,
            .action_kind = .run_command,
            .command_prefix = "zig",
        },
        .{
            .id = try policy.RuleId.init("block-zig-build"),
            .effect = .block,
            .action_kind = .run_command,
            .command_prefix = "zig",
            .reason = "zig commands blocked in this test",
        },
    };
    var engine: DeterministicPolicy = .{
        .config = .{
            .default_effect = .ask,
            .rules = &rules,
        },
    };
    const action: policy.Action = .{ .run_command = .{
        .argv = &.{ "zig", "build", "test" },
        .cwd = "/workspace",
    } };
    const context: policy.PolicyContext = .{
        .run_id = run_id,
        .cwd = "/workspace",
        .timestamp_ms = 1,
    };

    const decision = engine.evaluateAudited(action, context);
    try std.testing.expectEqualStrings("block", decision.decisionName());
    try std.testing.expectEqualStrings("block-zig-build", decision.rule_id.?.value);
}

test "default decisions remain auditable through the policy engine interface" {
    const run_id = try runtime.RunId.init("01HXYZ");
    var deterministic: DeterministicPolicy = .{
        .config = .{ .default_effect = .ask, .default_reason = "manual review" },
    };
    var engine = deterministic.asPolicyEngine();

    const decision = engine.evaluate(&engine, .{ .git_push = .{
        .remote = "origin",
        .branch = "main",
    } }, .{
        .run_id = run_id,
        .cwd = "/workspace",
        .timestamp_ms = 1,
    });

    try std.testing.expectEqualStrings("ask", decision.tagName());
}

test "command rules match argv command lines with trailing wildcard" {
    const rule: Rule = .{
        .id = try policy.RuleId.init("git-diff"),
        .effect = .allow,
        .action_kind = .run_command,
        .command_prefix = "git diff*",
    };

    try std.testing.expect(rule.matches(.{ .run_command = .{
        .argv = &.{ "git", "diff", "--stat" },
        .cwd = "/workspace",
    } }));
    try std.testing.expect(!rule.matches(.{ .run_command = .{
        .argv = &.{ "git", "status" },
        .cwd = "/workspace",
    } }));
}

test "project policy config converts into deterministic runtime rules" {
    const project_rules = [_]config.PolicyRule{
        .{ .kind = .command, .value = "git diff*" },
    };
    var owned = try fromProjectPolicy(std.testing.allocator, .{
        .default = .ask,
        .allow = &project_rules,
        .ask = &.{},
        .block = &.{},
    });
    defer owned.deinit();

    var engine: DeterministicPolicy = .{ .config = owned.config };
    const run_id = try runtime.RunId.init("01HXYZ");
    const decision = engine.evaluateAudited(.{ .run_command = .{
        .argv = &.{ "git", "diff", "--stat" },
        .cwd = "/workspace",
    } }, .{
        .run_id = run_id,
        .cwd = "/workspace",
        .timestamp_ms = 1,
    });

    try std.testing.expectEqualStrings("allow", decision.decisionName());
    try std.testing.expectEqualStrings("allow-0", decision.rule_id.?.value);
}

test "project policy conversion owns rule match strings" {
    const allocator = std.testing.allocator;
    const command = try allocator.dupe(u8, "git diff*");
    defer allocator.free(command);
    const project_rules = [_]config.PolicyRule{
        .{ .kind = .command, .value = command },
    };

    var owned = try fromProjectPolicy(allocator, .{
        .default = .ask,
        .allow = &project_rules,
        .ask = &.{},
        .block = &.{},
    });
    defer owned.deinit();

    @memset(command, 'x');

    var engine: DeterministicPolicy = .{ .config = owned.config };
    const run_id = try runtime.RunId.init("01HXYZ");
    const decision = engine.evaluateAudited(.{ .run_command = .{
        .argv = &.{ "git", "diff", "--stat" },
        .cwd = "/workspace",
    } }, .{
        .run_id = run_id,
        .cwd = "/workspace",
        .timestamp_ms = 1,
    });

    try std.testing.expectEqualStrings("allow", decision.decisionName());
}

test "unknown project policy action is rejected instead of matching every action" {
    const project_rules = [_]config.PolicyRule{
        .{ .kind = .action, .value = "unknown_action" },
    };

    try std.testing.expectError(error.InvalidProjectPolicyRule, fromProjectPolicy(std.testing.allocator, .{
        .default = .ask,
        .allow = &project_rules,
        .ask = &.{},
        .block = &.{},
    }));
}
