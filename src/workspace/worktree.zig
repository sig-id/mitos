const std = @import("std");
const config = @import("../config/types.zig");
const runtime = @import("../runtime/types.zig");

pub const default_worktree_dir = ".agent-worktrees";

pub const WorkspaceError = error{
    EmptyRepoRoot,
    EmptyBaseBranch,
    UnsafeRunId,
    GitFailed,
};

pub const WorktreePlan = struct {
    run_id: runtime.RunId,
    repo_root: []const u8,
    worktree_root: []const u8,
    host_path: []const u8,
    container_mount: []const u8,
    base_branch: []const u8,
    clean_before_run: bool,

    pub fn deinit(self: WorktreePlan, allocator: std.mem.Allocator) void {
        allocator.free(self.repo_root);
        allocator.free(self.worktree_root);
        allocator.free(self.host_path);
        allocator.free(self.container_mount);
        allocator.free(self.base_branch);
    }
};

pub const FileChange = struct {
    status: []const u8,
    path: []const u8,
};

pub const WorkspaceSnapshot = struct {
    diff_patch: []u8,
    changed_files: []const FileChange,

    pub fn deinit(self: WorkspaceSnapshot, allocator: std.mem.Allocator) void {
        allocator.free(self.diff_patch);
        for (self.changed_files) |change| {
            allocator.free(change.status);
            allocator.free(change.path);
        }
        allocator.free(self.changed_files);
    }
};

pub const PreparedWorkspace = struct {
    plan: WorktreePlan,
    pre_run: WorkspaceSnapshot,
    created_worktree: bool,

    pub fn deinit(self: *PreparedWorkspace, allocator: std.mem.Allocator) void {
        self.pre_run.deinit(allocator);
        self.plan.deinit(allocator);
        self.* = undefined;
    }
};

pub fn planGitWorktree(
    allocator: std.mem.Allocator,
    repo_root: []const u8,
    run_id: runtime.RunId,
    workspace: config.WorkspaceConfig,
) !WorktreePlan {
    if (repo_root.len == 0) return error.EmptyRepoRoot;
    if (workspace.base_branch.len == 0) return error.EmptyBaseBranch;
    try validateRunIdPathSegment(run_id.value);

    const worktree_root = try std.fs.path.join(allocator, &.{ repo_root, default_worktree_dir });
    errdefer allocator.free(worktree_root);
    const host_path = try std.fs.path.join(allocator, &.{ worktree_root, run_id.value });
    errdefer allocator.free(host_path);

    return .{
        .run_id = run_id,
        .repo_root = try allocator.dupe(u8, repo_root),
        .worktree_root = worktree_root,
        .host_path = host_path,
        .container_mount = try allocator.dupe(u8, workspace.container_mount),
        .base_branch = try allocator.dupe(u8, workspace.base_branch),
        .clean_before_run = workspace.clean_before_run,
    };
}

pub fn prepareGitWorktree(
    allocator: std.mem.Allocator,
    repo_root: []const u8,
    run_id: runtime.RunId,
    workspace: config.WorkspaceConfig,
) !PreparedWorkspace {
    var plan = try planGitWorktree(allocator, repo_root, run_id, workspace);
    errdefer plan.deinit(allocator);

    try createGitWorktree(allocator, plan);

    const pre_run = try captureWorkspaceSnapshot(allocator, plan.host_path);
    errdefer pre_run.deinit(allocator);

    return .{
        .plan = plan,
        .pre_run = pre_run,
        .created_worktree = true,
    };
}

pub fn finalizeGitWorktree(
    allocator: std.mem.Allocator,
    prepared: *const PreparedWorkspace,
    cleanup: bool,
) !WorkspaceSnapshot {
    const post_run = try captureWorkspaceSnapshot(allocator, prepared.plan.host_path);
    errdefer post_run.deinit(allocator);

    if (cleanup and prepared.created_worktree) try cleanupGitWorktree(allocator, prepared.plan);
    return post_run;
}

pub fn createGitWorktree(allocator: std.mem.Allocator, plan: WorktreePlan) !void {
    try std.fs.cwd().makePath(plan.worktree_root);
    try runGitDiscardingOutput(allocator, plan.repo_root, &.{
        "git",
        "-C",
        plan.repo_root,
        "worktree",
        "add",
        "--detach",
        plan.host_path,
        plan.base_branch,
    });
}

pub fn cleanupGitWorktree(allocator: std.mem.Allocator, plan: WorktreePlan) !void {
    try runGitDiscardingOutput(allocator, plan.repo_root, &.{
        "git",
        "-C",
        plan.repo_root,
        "worktree",
        "remove",
        "--force",
        plan.host_path,
    });
}

pub fn captureDiff(allocator: std.mem.Allocator, worktree_path: []const u8) ![]u8 {
    const result = try std.process.Child.run(.{
        .allocator = allocator,
        .argv = &.{ "git", "-C", worktree_path, "diff", "--binary", "--" },
        .max_output_bytes = 16 * 1024 * 1024,
    });
    defer allocator.free(result.stderr);
    errdefer allocator.free(result.stdout);
    try expectExitedZero(result.term);
    return result.stdout;
}

pub fn captureChangedFiles(allocator: std.mem.Allocator, worktree_path: []const u8) ![]const FileChange {
    const result = try std.process.Child.run(.{
        .allocator = allocator,
        .argv = &.{ "git", "-C", worktree_path, "status", "--porcelain=v1" },
        .max_output_bytes = 4 * 1024 * 1024,
    });
    defer allocator.free(result.stderr);
    defer allocator.free(result.stdout);
    try expectExitedZero(result.term);

    var changes: std.ArrayList(FileChange) = .empty;
    var lines = std.mem.splitScalar(u8, result.stdout, '\n');
    while (lines.next()) |line| {
        if (line.len == 0) continue;
        const status_end = @min(line.len, 2);
        const path_start: usize = if (line.len > 3) 3 else status_end;
        try changes.append(allocator, .{
            .status = try allocator.dupe(u8, std.mem.trim(u8, line[0..status_end], " ")),
            .path = try allocator.dupe(u8, std.mem.trim(u8, line[path_start..], " ")),
        });
    }
    return changes.toOwnedSlice(allocator);
}

pub fn captureWorkspaceSnapshot(allocator: std.mem.Allocator, worktree_path: []const u8) !WorkspaceSnapshot {
    const diff = try captureDiff(allocator, worktree_path);
    errdefer allocator.free(diff);
    const files = try captureChangedFiles(allocator, worktree_path);
    errdefer {
        for (files) |change| {
            allocator.free(change.status);
            allocator.free(change.path);
        }
        allocator.free(files);
    }
    return .{
        .diff_patch = diff,
        .changed_files = files,
    };
}

fn runGitDiscardingOutput(allocator: std.mem.Allocator, cwd: []const u8, argv: []const []const u8) !void {
    const result = try std.process.Child.run(.{
        .allocator = allocator,
        .argv = argv,
        .cwd = cwd,
        .max_output_bytes = 1024 * 1024,
    });
    defer allocator.free(result.stdout);
    defer allocator.free(result.stderr);
    try expectExitedZero(result.term);
}

fn expectExitedZero(term: std.process.Child.Term) WorkspaceError!void {
    switch (term) {
        .Exited => |code| if (code == 0) return else return error.GitFailed,
        else => return error.GitFailed,
    }
}

fn validateRunIdPathSegment(value: []const u8) WorkspaceError!void {
    if (value.len == 0 or std.mem.eql(u8, value, ".") or std.mem.eql(u8, value, "..")) return error.UnsafeRunId;
    for (value) |byte| {
        switch (byte) {
            '/', '\\', 0 => return error.UnsafeRunId,
            else => {},
        }
    }
}

test "git worktree plan uses stable per-run workspace path" {
    const allocator = std.testing.allocator;
    const run_id = try runtime.RunId.init("01HXYZ");
    const workspace: config.WorkspaceConfig = .{
        .base_branch = "main",
        .container_mount = "/workspace",
        .clean_before_run = true,
    };

    const plan = try planGitWorktree(allocator, "/repo", run_id, workspace);
    defer plan.deinit(allocator);

    try std.testing.expectEqualStrings("/repo/.agent-worktrees/01HXYZ", plan.host_path);
    try std.testing.expectEqualStrings("/workspace", plan.container_mount);
    try std.testing.expectEqualStrings("main", plan.base_branch);
    try std.testing.expect(plan.clean_before_run);
}

test "git worktree plan rejects path-like run ids" {
    const allocator = std.testing.allocator;
    const run_id = try runtime.RunId.init("../escape");
    const workspace: config.WorkspaceConfig = .{ .base_branch = "main" };

    try std.testing.expectError(error.UnsafeRunId, planGitWorktree(allocator, "/repo", run_id, workspace));
}

test "git worktree lifecycle captures pre and post run snapshots" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.makeDir("repo");
    var repo_dir = try tmp.dir.openDir("repo", .{});
    defer repo_dir.close();
    try repo_dir.writeFile(.{ .sub_path = "README.md", .data = "base\n" });

    const cwd = try std.fs.cwd().realpathAlloc(allocator, ".");
    defer allocator.free(cwd);
    const repo_path = try std.fs.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "repo" });
    defer allocator.free(repo_path);

    try runGitDiscardingOutput(allocator, repo_path, &.{ "git", "-C", repo_path, "init" });
    try runGitDiscardingOutput(allocator, repo_path, &.{ "git", "-C", repo_path, "config", "user.email", "agent@example.invalid" });
    try runGitDiscardingOutput(allocator, repo_path, &.{ "git", "-C", repo_path, "config", "user.name", "Agent Runtime" });
    try runGitDiscardingOutput(allocator, repo_path, &.{ "git", "-C", repo_path, "add", "README.md" });
    try runGitDiscardingOutput(allocator, repo_path, &.{ "git", "-C", repo_path, "commit", "-m", "initial" });

    const run_id = try runtime.RunId.init("01HWORK");
    const workspace_cfg: config.WorkspaceConfig = .{
        .base_branch = "HEAD",
        .container_mount = "/workspace",
        .clean_before_run = true,
    };
    var prepared = try prepareGitWorktree(allocator, repo_path, run_id, workspace_cfg);
    defer prepared.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 0), prepared.pre_run.changed_files.len);

    var worktree_dir = try std.fs.openDirAbsolute(prepared.plan.host_path, .{});
    defer worktree_dir.close();
    try worktree_dir.writeFile(.{ .sub_path = "README.md", .data = "changed\n" });

    const post = try finalizeGitWorktree(allocator, &prepared, true);
    defer post.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), post.changed_files.len);
    try std.testing.expectEqualStrings("M", post.changed_files[0].status);
    try std.testing.expectEqualStrings("README.md", post.changed_files[0].path);
    try std.testing.expect(std.mem.indexOf(u8, post.diff_patch, "-base") != null);
    try std.testing.expect(std.mem.indexOf(u8, post.diff_patch, "+changed") != null);
}
