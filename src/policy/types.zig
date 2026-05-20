const std = @import("std");
const runtime = @import("../runtime/types.zig");
const terminal = @import("../terminal/core.zig");

pub const IdentifierError = error{
    EmptyIdentifier,
};

pub const RuleId = struct {
    value: []const u8,

    pub fn init(value: []const u8) IdentifierError!RuleId {
        if (value.len == 0) return error.EmptyIdentifier;
        return .{ .value = value };
    }
};

pub const ApprovalId = struct {
    value: []const u8,

    pub fn init(value: []const u8) IdentifierError!ApprovalId {
        if (value.len == 0) return error.EmptyIdentifier;
        return .{ .value = value };
    }
};

pub const RiskLevel = enum {
    low,
    medium,
    high,
    critical,
};

pub const ActionKind = enum {
    read_path,
    write_path,
    delete_path,
    run_command,
    network_access,
    install_package,
    git_push,
    read_secret,
    terminal_input,
};

pub const CommandAction = struct {
    argv: []const []const u8,
    cwd: []const u8,
};

pub const NetworkAccess = struct {
    destination: ?[]const u8 = null,
};

pub const InstallPackage = struct {
    manager: []const u8,
    package: []const u8,
};

pub const GitPush = struct {
    remote: []const u8,
    branch: []const u8,
};

pub const ReadSecret = struct {
    path_or_env: []const u8,
};

pub const TerminalInput = struct {
    bytes: []const u8,
    reason: []const u8,
};

pub const Action = union(ActionKind) {
    read_path: []const u8,
    write_path: []const u8,
    delete_path: []const u8,
    run_command: CommandAction,
    network_access: NetworkAccess,
    install_package: InstallPackage,
    git_push: GitPush,
    read_secret: ReadSecret,
    terminal_input: TerminalInput,

    pub fn kind(self: Action) ActionKind {
        return std.meta.activeTag(self);
    }
};

pub const AllowDecision = struct {
    rule_id: RuleId,
};

pub const AskDecision = struct {
    rule_id: ?RuleId = null,
    reason: []const u8,
};

pub const BlockDecision = struct {
    rule_id: ?RuleId = null,
    reason: []const u8,
};

pub const PolicyDecision = union(enum) {
    allow: AllowDecision,
    ask: AskDecision,
    block: BlockDecision,

    pub fn tagName(self: PolicyDecision) []const u8 {
        return @tagName(std.meta.activeTag(self));
    }
};

pub const PolicyContext = struct {
    run_id: runtime.RunId,
    cwd: []const u8,
    timestamp_ms: runtime.TimestampMs,
};

pub const PolicyEngine = struct {
    context: *anyopaque,
    evaluate: *const fn (*PolicyEngine, Action, PolicyContext) PolicyDecision,
};

pub const ApprovalSource = enum {
    provider_prompt,
    command_parser,
    filesystem_observer,
    model_classifier,
    runtime,
    user,
};

pub const ApprovalRequest = struct {
    id: ApprovalId,
    run_id: runtime.RunId,
    action: Action,
    source: ApprovalSource,
    risk: RiskLevel,
    terminal_context: ?*const terminal.ScreenSnapshot = null,
    matched_rule: ?RuleId = null,
    suggested_decision: ?PolicyDecision = null,
};

pub const ApprovalDecision = union(enum) {
    allow_once,
    allow_for_run,
    add_project_rule: RuleId,
    deny,
    terminate,
};

test "actions expose their semantic kind" {
    const action: Action = .{ .run_command = .{
        .argv = &.{ "zig", "build", "test" },
        .cwd = "/workspace",
    } };

    try std.testing.expectEqual(ActionKind.run_command, action.kind());
}

test "policy decisions expose stable tag names" {
    const rule_id = try RuleId.init("allow-tests");
    const decision: PolicyDecision = .{ .allow = .{ .rule_id = rule_id } };

    try std.testing.expectEqualStrings("allow", decision.tagName());
}
