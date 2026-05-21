const std = @import("std");
const policy = @import("../policy/types.zig");
const runtime = @import("../runtime/types.zig");
const trace = @import("../trace/types.zig");

pub const ApprovalRecord = struct {
    id: policy.ApprovalId,
    run_id: runtime.RunId,
    request: policy.ApprovalRequest,
    decision: ?policy.ApprovalDecision = null,
};

pub const MemoryApprovalStore = struct {
    records: []const ApprovalRecord = &.{},

    pub fn count(self: MemoryApprovalStore) usize {
        return self.records.len;
    }
};

pub const FileApprovalStore = struct {
    allocator: std.mem.Allocator,
    run_dir: std.fs.Dir,

    pub fn persistRequest(
        self: *FileApprovalStore,
        request: policy.ApprovalRequest,
        requested_at_ms: runtime.TimestampMs,
    ) !void {
        const line = try renderRequest(self.allocator, request, requested_at_ms);
        defer self.allocator.free(line);
        try self.appendLine(line);
    }

    pub fn recordDecision(
        self: *FileApprovalStore,
        approval_id: policy.ApprovalId,
        decision: policy.ApprovalDecision,
        decided_by: []const u8,
        decided_at_ms: runtime.TimestampMs,
    ) !void {
        const line = try renderDecision(
            self.allocator,
            approval_id,
            decision,
            decided_by,
            decided_at_ms,
        );
        defer self.allocator.free(line);
        try self.appendLine(line);
    }

    pub fn recordDecisionAndRoute(
        self: *FileApprovalStore,
        approval_id: policy.ApprovalId,
        decision: policy.ApprovalDecision,
        decided_by: []const u8,
        decided_at_ms: runtime.TimestampMs,
    ) !DecisionRoute {
        try self.recordDecision(approval_id, decision, decided_by, decided_at_ms);
        return routeDecision(decision);
    }

    fn appendLine(self: *FileApprovalStore, line: []const u8) !void {
        var file = try self.run_dir.createFile(trace.defaultArtifactPath(.approvals), .{
            .truncate = false,
        });
        defer file.close();
        try file.seekFromEnd(0);
        try file.writeAll(line);
        if (line.len == 0 or line[line.len - 1] != '\n') {
            try file.writeAll("\n");
        }
    }
};

pub const ApprovalSummary = struct {
    requested: usize = 0,
    decided: usize = 0,
    pending: usize = 0,
};

pub const DecisionRoute = enum {
    allow_action,
    deny_action,
    terminate_run,
    update_project_policy,
};

pub fn approvalDecisionName(decision: policy.ApprovalDecision) []const u8 {
    return @tagName(std.meta.activeTag(decision));
}

pub fn routeDecision(decision: policy.ApprovalDecision) DecisionRoute {
    return switch (decision) {
        .allow_once, .allow_for_run => .allow_action,
        .deny => .deny_action,
        .terminate => .terminate_run,
        .add_project_rule => .update_project_policy,
    };
}

pub fn summarize(
    allocator: std.mem.Allocator,
    run_dir: std.fs.Dir,
) !ApprovalSummary {
    const bytes = run_dir.readFileAlloc(
        allocator,
        trace.defaultArtifactPath(.approvals),
        16 * 1024 * 1024,
    ) catch |err| switch (err) {
        error.FileNotFound => return .{},
        else => return err,
    };
    defer allocator.free(bytes);

    var pending_ids: std.ArrayList([]u8) = .empty;
    defer {
        for (pending_ids.items) |id| allocator.free(id);
        pending_ids.deinit(allocator);
    }

    var summary: ApprovalSummary = .{};
    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len == 0) continue;

        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
        defer parsed.deinit();

        switch (parsed.value) {
            .object => |*object| {
                const record_type = stringField(object, "type") orelse continue;
                const approval_id = stringField(object, "approval_id") orelse continue;
                if (std.mem.eql(u8, record_type, "approval_requested")) {
                    summary.requested += 1;
                    try pending_ids.append(allocator, try allocator.dupe(u8, approval_id));
                } else if (std.mem.eql(u8, record_type, "approval_decision")) {
                    summary.decided += 1;
                    removePendingId(allocator, &pending_ids, approval_id);
                }
            },
            else => {},
        }
    }
    summary.pending = pending_ids.items.len;
    return summary;
}

fn renderRequest(
    allocator: std.mem.Allocator,
    request: policy.ApprovalRequest,
    requested_at_ms: runtime.TimestampMs,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();

    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "type", "approval_requested");
    try field(&json, "approval_id", request.id.value);
    try field(&json, "run_id", request.run_id.value);
    try field(&json, "requested_at_ms", requested_at_ms);
    try field(&json, "action_kind", @tagName(request.action.kind()));
    try field(&json, "source", @tagName(request.source));
    try field(&json, "risk", @tagName(request.risk));
    try field(&json, "matched_rule", if (request.matched_rule) |rule| rule.value else null);
    try field(&json, "suggested_decision", if (request.suggested_decision) |decision| decision.tagName() else null);
    try json.endObject();
    try out.writer.writeByte('\n');
    return out.toOwnedSlice();
}

fn renderDecision(
    allocator: std.mem.Allocator,
    approval_id: policy.ApprovalId,
    decision: policy.ApprovalDecision,
    decided_by: []const u8,
    decided_at_ms: runtime.TimestampMs,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();

    var json: std.json.Stringify = .{ .writer = &out.writer };
    try json.beginObject();
    try field(&json, "type", "approval_decision");
    try field(&json, "approval_id", approval_id.value);
    try field(&json, "decision", approvalDecisionName(decision));
    try field(&json, "decided_by", decided_by);
    try field(&json, "decided_at_ms", decided_at_ms);
    switch (decision) {
        .add_project_rule => |rule| try field(&json, "rule_id", rule.value),
        else => {},
    }
    try json.endObject();
    try out.writer.writeByte('\n');
    return out.toOwnedSlice();
}

fn field(json: *std.json.Stringify, name: []const u8, value: anytype) !void {
    try json.objectField(name);
    try json.write(value);
}

fn stringField(object: *const std.json.ObjectMap, name: []const u8) ?[]const u8 {
    const value = object.get(name) orelse return null;
    return switch (value) {
        .string => |text| text,
        else => null,
    };
}

fn removePendingId(
    allocator: std.mem.Allocator,
    pending_ids: *std.ArrayList([]u8),
    approval_id: []const u8,
) void {
    for (pending_ids.items, 0..) |candidate, index| {
        if (std.mem.eql(u8, candidate, approval_id)) {
            allocator.free(candidate);
            _ = pending_ids.orderedRemove(index);
            return;
        }
    }
}

test "memory approval store counts persisted records" {
    const run_id = try runtime.RunId.init("run-approval");
    const approval_id = try policy.ApprovalId.init("approval-1");
    const request: policy.ApprovalRequest = .{
        .id = approval_id,
        .run_id = run_id,
        .action = .{ .terminal_input = .{ .bytes = "n\n", .reason = "deny prompt" } },
        .source = .provider_prompt,
        .risk = .medium,
    };
    const records = [_]ApprovalRecord{.{
        .id = approval_id,
        .run_id = run_id,
        .request = request,
    }};

    const store: MemoryApprovalStore = .{ .records = &records };
    try std.testing.expectEqual(@as(usize, 1), store.count());
}

test "approval store appends request and decision records" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(.{ .sub_path = trace.defaultArtifactPath(.approvals), .data = "" });

    const run_id = try runtime.RunId.init("01HXYZ");
    const approval_id = try policy.ApprovalId.init("approval-1");
    var store: FileApprovalStore = .{
        .allocator = allocator,
        .run_dir = tmp.dir,
    };
    try store.persistRequest(.{
        .id = approval_id,
        .run_id = run_id,
        .action = .{ .network_access = .{ .destination = "api.example.com" } },
        .source = .runtime,
        .risk = .high,
    }, 10);
    const route = try store.recordDecisionAndRoute(approval_id, .deny, "user", 11);
    try std.testing.expectEqual(DecisionRoute.deny_action, route);

    const approvals = try tmp.dir.readFileAlloc(
        allocator,
        trace.defaultArtifactPath(.approvals),
        4096,
    );
    defer allocator.free(approvals);

    try std.testing.expect(std.mem.containsAtLeast(u8, approvals, 1, "\"type\":\"approval_requested\""));
    try std.testing.expect(std.mem.containsAtLeast(u8, approvals, 1, "\"decision\":\"deny\""));

    const summary = try summarize(allocator, tmp.dir);
    try std.testing.expectEqual(@as(usize, 1), summary.requested);
    try std.testing.expectEqual(@as(usize, 1), summary.decided);
    try std.testing.expectEqual(@as(usize, 0), summary.pending);
}

test "approval summary reconstructs pending requests from append-only log" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(.{
        .sub_path = trace.defaultArtifactPath(.approvals),
        .data =
        \\{"type":"approval_requested","approval_id":"approval-1"}
        \\{"type":"approval_requested","approval_id":"approval-2"}
        \\{"type":"approval_decision","approval_id":"approval-1","decision":"deny"}
        \\
        ,
    });

    const summary = try summarize(allocator, tmp.dir);
    try std.testing.expectEqual(@as(usize, 2), summary.requested);
    try std.testing.expectEqual(@as(usize, 1), summary.decided);
    try std.testing.expectEqual(@as(usize, 1), summary.pending);
}

test "approval decisions map to runtime routes" {
    try std.testing.expectEqual(DecisionRoute.allow_action, routeDecision(.allow_once));
    try std.testing.expectEqual(DecisionRoute.allow_action, routeDecision(.allow_for_run));
    try std.testing.expectEqual(DecisionRoute.deny_action, routeDecision(.deny));
    try std.testing.expectEqual(DecisionRoute.terminate_run, routeDecision(.terminate));
    try std.testing.expectEqual(
        DecisionRoute.update_project_policy,
        routeDecision(.{ .add_project_rule = try policy.RuleId.init("new-rule") }),
    );
}
