const std = @import("std");
const adapters = @import("../adapters/types.zig");
const sandbox = @import("../sandbox/backend.zig");

pub const config_file_name = ".agent-runtime.yaml";
pub const supported_version: u32 = 1;

pub const ConfigError = error{
    EmptyConfig,
    InvalidConfig,
    InvalidVersion,
    InvalidEnumValue,
    InvalidSandboxProfile,
    MissingField,
};

pub const WorkspaceMode = enum {
    git_worktree,
};

pub const CredentialsPassThrough = enum {
    explicit_only,
    none,
    inherit,
};

pub const PolicyDisposition = enum {
    allow,
    ask,
    block,
};

pub const PolicyRuleKind = enum {
    action,
    command,
    path,
};

pub const WatcherMode = enum {
    local_only,
    remote_approved_context,
};

pub const WorkspaceConfig = struct {
    mode: WorkspaceMode = .git_worktree,
    base_branch: []const u8,
    clean_before_run: bool = true,
    container_mount: []const u8 = "/workspace",
};

pub const CredentialsConfig = struct {
    pass_through: CredentialsPassThrough = .explicit_only,
};

pub const SandboxConfig = struct {
    backend: sandbox.BackendId = .nucleus,
    mode: sandbox.SandboxMode = .strict_agent,
    runtime: sandbox.SandboxRuntime = .gvisor,
    network: sandbox.NetworkMode = .none,
    workdir: []const u8 = "/workspace",
    home: []const u8 = "/home/agent",
    resources: sandbox.ResourceLimits = .{
        .memory = "4G",
        .cpus = 4,
        .pids = 1024,
    },
    toolchain_rootfs: []const u8 = "agent-zig-node",
    credentials: CredentialsConfig = .{},
};

pub const ProviderConfig = struct {
    name: []const u8,
    adapter_order: []const adapters.AdapterMode,
};

pub const PolicyRule = struct {
    kind: PolicyRuleKind,
    value: []const u8,
};

pub const PolicyConfig = struct {
    default: PolicyDisposition = .ask,
    allow: []const PolicyRule = &.{},
    ask: []const PolicyRule = &.{},
    block: []const PolicyRule = &.{},
};

pub const WatcherConfig = struct {
    enabled: bool = false,
    mode: WatcherMode = .local_only,
    model: ?[]const u8 = null,
};

pub const ProjectConfig = struct {
    version: u32,
    workspace: WorkspaceConfig,
    sandbox: SandboxConfig,
    providers: []const ProviderConfig,
    policy: PolicyConfig,
    watcher: WatcherConfig,

    pub fn validate(self: ProjectConfig) ConfigError!void {
        if (self.version != supported_version) return error.InvalidVersion;
        if (self.workspace.base_branch.len == 0) return error.MissingField;
        if (!std.mem.eql(u8, self.workspace.container_mount, "/workspace")) return error.InvalidConfig;

        if (self.sandbox.resources.memory.len == 0) return error.MissingField;
        if (self.sandbox.resources.cpus == 0 or self.sandbox.resources.pids == 0) return error.InvalidConfig;
        if (self.sandbox.toolchain_rootfs.len == 0) return error.MissingField;

        switch (self.sandbox.backend) {
            .none => if (self.sandbox.mode != .none or self.sandbox.runtime != .none) return error.InvalidSandboxProfile,
            .host_process => if (self.sandbox.mode != .host_process) return error.InvalidSandboxProfile,
            .nucleus => {
                if (self.sandbox.mode != .strict_agent) return error.InvalidSandboxProfile;
                if (!std.mem.eql(u8, self.sandbox.workdir, "/workspace")) return error.InvalidSandboxProfile;
                if (!std.mem.eql(u8, self.sandbox.home, "/home/agent")) return error.InvalidSandboxProfile;
                if (self.sandbox.runtime == .none) return error.InvalidSandboxProfile;
            },
            .remote, .vm => {},
        }

        if (self.providers.len == 0) return error.MissingField;
        for (self.providers) |provider| {
            if (provider.name.len == 0 or provider.adapter_order.len == 0) return error.MissingField;
        }
    }
};

pub const LoadedProjectConfig = struct {
    arena: std.heap.ArenaAllocator,
    value: ProjectConfig,

    pub fn deinit(self: *LoadedProjectConfig) void {
        self.arena.deinit();
        self.* = undefined;
    }
};

const YamlSection = enum {
    root,
    workspace,
    sandbox,
    sandbox_resources,
    sandbox_credentials,
    providers,
    policy,
    watcher,
};

pub fn parseProjectConfig(allocator: std.mem.Allocator, bytes: []const u8) !LoadedProjectConfig {
    const trimmed = std.mem.trim(u8, bytes, " \t\r\n");
    if (trimmed.len == 0) return error.EmptyConfig;

    var loaded: LoadedProjectConfig = .{
        .arena = std.heap.ArenaAllocator.init(allocator),
        .value = undefined,
    };
    errdefer loaded.arena.deinit();

    const arena = loaded.arena.allocator();
    loaded.value = switch (trimmed[0]) {
        '{' => try parseJsonConfig(arena, trimmed),
        else => try parseYamlSubset(arena, trimmed),
    };
    try loaded.value.validate();
    return loaded;
}

pub fn loadProjectConfig(
    allocator: std.mem.Allocator,
    dir: std.fs.Dir,
) !LoadedProjectConfig {
    const bytes = try dir.readFileAlloc(allocator, config_file_name, 1024 * 1024);
    defer allocator.free(bytes);
    return parseProjectConfig(allocator, bytes);
}

fn parseJsonConfig(arena: std.mem.Allocator, bytes: []const u8) !ProjectConfig {
    const root = try std.json.parseFromSliceLeaky(std.json.Value, arena, bytes, .{});
    const workspace_value = try jsonRequired(root, "workspace");
    const sandbox_value = try jsonRequired(root, "sandbox");
    const providers_value = try jsonRequired(root, "providers");
    const policy_value = try jsonRequired(root, "policy");
    const watcher_value = try jsonRequired(root, "watcher");

    return .{
        .version = @intCast(try jsonInteger(try jsonRequired(root, "version"))),
        .workspace = .{
            .mode = try parseWorkspaceMode(try jsonString(try jsonRequired(workspace_value, "mode"))),
            .base_branch = try dupString(arena, try jsonString(try jsonRequired(workspace_value, "base_branch"))),
            .clean_before_run = try jsonBool(try jsonRequired(workspace_value, "clean_before_run")),
            .container_mount = try dupString(arena, try jsonString(try jsonRequired(workspace_value, "container_mount"))),
        },
        .sandbox = .{
            .backend = try parseBackendId(try jsonString(try jsonRequired(sandbox_value, "backend"))),
            .mode = try parseSandboxMode(try jsonString(try jsonRequired(sandbox_value, "mode"))),
            .runtime = try parseSandboxRuntime(try jsonString(try jsonRequired(sandbox_value, "runtime"))),
            .network = try parseNetworkMode(try jsonString(try jsonRequired(sandbox_value, "network"))),
            .workdir = try dupString(arena, try jsonString(try jsonRequired(sandbox_value, "workdir"))),
            .home = try dupString(arena, try jsonString(try jsonRequired(sandbox_value, "home"))),
            .resources = try parseJsonResources(arena, try jsonRequired(sandbox_value, "resources")),
            .toolchain_rootfs = try dupString(arena, try jsonString(try jsonRequired(sandbox_value, "toolchain_rootfs"))),
            .credentials = .{
                .pass_through = try parseCredentialsPassThrough(try jsonString(try jsonRequired(try jsonRequired(sandbox_value, "credentials"), "pass_through"))),
            },
        },
        .providers = try parseJsonProviders(arena, providers_value),
        .policy = .{
            .default = try parsePolicyDisposition(try jsonString(try jsonRequired(policy_value, "default"))),
            .allow = try parseJsonPolicyRules(arena, try jsonRequired(policy_value, "allow")),
            .ask = try parseJsonPolicyRules(arena, try jsonRequired(policy_value, "ask")),
            .block = try parseJsonPolicyRules(arena, try jsonRequired(policy_value, "block")),
        },
        .watcher = try parseJsonWatcher(arena, watcher_value),
    };
}

fn parseYamlSubset(arena: std.mem.Allocator, bytes: []const u8) !ProjectConfig {
    var providers: std.ArrayList(ProviderConfig) = .empty;
    var allow: std.ArrayList(PolicyRule) = .empty;
    var ask: std.ArrayList(PolicyRule) = .empty;
    var block: std.ArrayList(PolicyRule) = .empty;

    var cfg: ProjectConfig = .{
        .version = 0,
        .workspace = .{ .base_branch = "" },
        .sandbox = .{},
        .providers = &.{},
        .policy = .{},
        .watcher = .{},
    };

    var section: YamlSection = .root;
    var active_rules: ?PolicyDisposition = null;

    var lines = std.mem.splitScalar(u8, bytes, '\n');
    while (lines.next()) |raw_line| {
        const without_comment = stripYamlComment(raw_line);
        const line = std.mem.trimRight(u8, without_comment, " \t\r");
        if (std.mem.trim(u8, line, " \t").len == 0) continue;

        const indent = leadingSpaces(line);
        const item = std.mem.trim(u8, line, " \t\r");

        if (indent == 0) {
            active_rules = null;
            const pair = try splitYamlKeyValue(item);
            if (pair.value.len == 0) {
                section = parseTopLevelSection(pair.key) orelse return error.InvalidConfig;
            } else if (std.mem.eql(u8, pair.key, "version")) {
                cfg.version = try std.fmt.parseInt(u32, scalar(pair.value), 10);
                section = .root;
            } else {
                return error.InvalidConfig;
            }
            continue;
        }

        switch (section) {
            .workspace => {
                if (indent != 2) return error.InvalidConfig;
                const pair = try splitYamlKeyValue(item);
                try parseWorkspaceField(arena, &cfg.workspace, pair.key, scalar(pair.value));
            },
            .sandbox => {
                const pair = try splitYamlKeyValue(item);
                if (indent == 2 and pair.value.len == 0 and std.mem.eql(u8, pair.key, "resources")) {
                    section = .sandbox_resources;
                    continue;
                }
                if (indent == 2 and pair.value.len == 0 and std.mem.eql(u8, pair.key, "credentials")) {
                    section = .sandbox_credentials;
                    continue;
                }
                if (indent != 2) return error.InvalidConfig;
                try parseSandboxField(arena, &cfg.sandbox, pair.key, scalar(pair.value));
            },
            .sandbox_resources => {
                if (indent == 2) {
                    section = .sandbox;
                    const pair = try splitYamlKeyValue(item);
                    if (pair.value.len == 0 and std.mem.eql(u8, pair.key, "credentials")) {
                        section = .sandbox_credentials;
                        continue;
                    }
                    try parseSandboxField(arena, &cfg.sandbox, pair.key, scalar(pair.value));
                    continue;
                }
                if (indent != 4) return error.InvalidConfig;
                const pair = try splitYamlKeyValue(item);
                const value = scalar(pair.value);
                if (std.mem.eql(u8, pair.key, "memory")) cfg.sandbox.resources.memory = try dupString(arena, value) else if (std.mem.eql(u8, pair.key, "cpus")) cfg.sandbox.resources.cpus = try std.fmt.parseInt(u16, value, 10) else if (std.mem.eql(u8, pair.key, "pids")) cfg.sandbox.resources.pids = try std.fmt.parseInt(u32, value, 10) else return error.InvalidConfig;
            },
            .sandbox_credentials => {
                if (indent == 2) {
                    section = .sandbox;
                    return error.InvalidConfig;
                }
                if (indent != 4) return error.InvalidConfig;
                const pair = try splitYamlKeyValue(item);
                if (!std.mem.eql(u8, pair.key, "pass_through")) return error.InvalidConfig;
                cfg.sandbox.credentials.pass_through = try parseCredentialsPassThrough(scalar(pair.value));
            },
            .providers => {
                if (indent == 2) {
                    const pair = try splitYamlKeyValue(item);
                    if (pair.value.len != 0) return error.InvalidConfig;
                    try providers.append(arena, .{
                        .name = try dupString(arena, pair.key),
                        .adapter_order = &.{},
                    });
                } else if (indent == 4) {
                    if (providers.items.len == 0) return error.InvalidConfig;
                    const pair = try splitYamlKeyValue(item);
                    if (!std.mem.eql(u8, pair.key, "adapter_order")) return error.InvalidConfig;
                    providers.items[providers.items.len - 1].adapter_order = try parseAdapterOrderList(arena, pair.value);
                } else return error.InvalidConfig;
            },
            .policy => {
                if (indent == 2) {
                    const pair = try splitYamlKeyValue(item);
                    if (std.mem.eql(u8, pair.key, "default")) {
                        cfg.policy.default = try parsePolicyDisposition(scalar(pair.value));
                        active_rules = null;
                    } else if (std.mem.eql(u8, pair.key, "allow")) {
                        active_rules = .allow;
                    } else if (std.mem.eql(u8, pair.key, "ask")) {
                        active_rules = .ask;
                    } else if (std.mem.eql(u8, pair.key, "block")) {
                        active_rules = .block;
                    } else return error.InvalidConfig;
                } else if (indent == 4) {
                    if (!std.mem.startsWith(u8, item, "- ")) return error.InvalidConfig;
                    const pair = try splitYamlKeyValue(std.mem.trim(u8, item[2..], " \t"));
                    const rule: PolicyRule = .{
                        .kind = try parsePolicyRuleKind(pair.key),
                        .value = try dupString(arena, scalar(pair.value)),
                    };
                    switch (active_rules orelse return error.InvalidConfig) {
                        .allow => try allow.append(arena, rule),
                        .ask => try ask.append(arena, rule),
                        .block => try block.append(arena, rule),
                    }
                } else return error.InvalidConfig;
            },
            .watcher => {
                if (indent != 2) return error.InvalidConfig;
                const pair = try splitYamlKeyValue(item);
                const value = scalar(pair.value);
                if (std.mem.eql(u8, pair.key, "enabled")) cfg.watcher.enabled = try parseBool(value) else if (std.mem.eql(u8, pair.key, "mode")) cfg.watcher.mode = try parseWatcherMode(value) else if (std.mem.eql(u8, pair.key, "model")) cfg.watcher.model = try dupString(arena, value) else return error.InvalidConfig;
            },
            .root => return error.InvalidConfig,
        }
    }

    cfg.providers = try providers.toOwnedSlice(arena);
    cfg.policy.allow = try allow.toOwnedSlice(arena);
    cfg.policy.ask = try ask.toOwnedSlice(arena);
    cfg.policy.block = try block.toOwnedSlice(arena);
    return cfg;
}

fn parseJsonResources(arena: std.mem.Allocator, value: std.json.Value) !sandbox.ResourceLimits {
    return .{
        .memory = try dupString(arena, try jsonString(try jsonRequired(value, "memory"))),
        .cpus = @intCast(try jsonInteger(try jsonRequired(value, "cpus"))),
        .pids = @intCast(try jsonInteger(try jsonRequired(value, "pids"))),
    };
}

fn parseJsonProviders(arena: std.mem.Allocator, value: std.json.Value) ![]const ProviderConfig {
    const object = switch (value) {
        .object => |object| object,
        else => return error.InvalidConfig,
    };
    var result: std.ArrayList(ProviderConfig) = .empty;
    var iterator = object.iterator();
    while (iterator.next()) |entry| {
        const provider_value = entry.value_ptr.*;
        const order = try parseJsonAdapterOrder(arena, try jsonRequired(provider_value, "adapter_order"));
        try result.append(arena, .{
            .name = try dupString(arena, entry.key_ptr.*),
            .adapter_order = order,
        });
    }
    return result.toOwnedSlice(arena);
}

fn parseJsonAdapterOrder(arena: std.mem.Allocator, value: std.json.Value) ![]const adapters.AdapterMode {
    const array = switch (value) {
        .array => |array| array,
        else => return error.InvalidConfig,
    };
    const result = try arena.alloc(adapters.AdapterMode, array.items.len);
    for (array.items, 0..) |item, index| {
        result[index] = try parseAdapterMode(try jsonString(item));
    }
    return result;
}

fn parseJsonPolicyRules(arena: std.mem.Allocator, value: std.json.Value) ![]const PolicyRule {
    const array = switch (value) {
        .array => |array| array,
        else => return error.InvalidConfig,
    };
    var result: std.ArrayList(PolicyRule) = .empty;
    for (array.items) |item| {
        const object = switch (item) {
            .object => |object| object,
            else => return error.InvalidConfig,
        };
        if (object.count() != 1) return error.InvalidConfig;
        var iterator = object.iterator();
        const entry = iterator.next() orelse return error.InvalidConfig;
        try result.append(arena, .{
            .kind = try parsePolicyRuleKind(entry.key_ptr.*),
            .value = try dupString(arena, try jsonString(entry.value_ptr.*)),
        });
    }
    return result.toOwnedSlice(arena);
}

fn parseJsonWatcher(arena: std.mem.Allocator, value: std.json.Value) !WatcherConfig {
    var watcher: WatcherConfig = .{};
    if (jsonOptional(value, "enabled")) |enabled| watcher.enabled = try jsonBool(enabled);
    watcher.mode = try parseWatcherMode(try jsonString(try jsonRequired(value, "mode")));
    if (jsonOptional(value, "model")) |model| watcher.model = try dupString(arena, try jsonString(model));
    return watcher;
}

fn parseWorkspaceField(arena: std.mem.Allocator, workspace: *WorkspaceConfig, key: []const u8, value: []const u8) !void {
    if (std.mem.eql(u8, key, "mode")) {
        workspace.mode = try parseWorkspaceMode(value);
    } else if (std.mem.eql(u8, key, "base_branch")) {
        workspace.base_branch = try dupString(arena, value);
    } else if (std.mem.eql(u8, key, "clean_before_run")) {
        workspace.clean_before_run = try parseBool(value);
    } else if (std.mem.eql(u8, key, "container_mount")) {
        workspace.container_mount = try dupString(arena, value);
    } else {
        return error.InvalidConfig;
    }
}

fn parseSandboxField(arena: std.mem.Allocator, sandbox_config: *SandboxConfig, key: []const u8, value: []const u8) !void {
    if (std.mem.eql(u8, key, "backend")) {
        sandbox_config.backend = try parseBackendId(value);
    } else if (std.mem.eql(u8, key, "mode")) {
        sandbox_config.mode = try parseSandboxMode(value);
    } else if (std.mem.eql(u8, key, "runtime")) {
        sandbox_config.runtime = try parseSandboxRuntime(value);
    } else if (std.mem.eql(u8, key, "network")) {
        sandbox_config.network = try parseNetworkMode(value);
    } else if (std.mem.eql(u8, key, "workdir")) {
        sandbox_config.workdir = try dupString(arena, value);
    } else if (std.mem.eql(u8, key, "home")) {
        sandbox_config.home = try dupString(arena, value);
    } else if (std.mem.eql(u8, key, "toolchain_rootfs")) {
        sandbox_config.toolchain_rootfs = try dupString(arena, value);
    } else {
        return error.InvalidConfig;
    }
}

fn jsonRequired(value: std.json.Value, key: []const u8) !std.json.Value {
    return jsonOptional(value, key) orelse error.MissingField;
}

fn jsonOptional(value: std.json.Value, key: []const u8) ?std.json.Value {
    return switch (value) {
        .object => |object| object.get(key),
        else => null,
    };
}

fn jsonString(value: std.json.Value) ![]const u8 {
    return switch (value) {
        .string => |string_value| string_value,
        else => error.InvalidConfig,
    };
}

fn jsonBool(value: std.json.Value) !bool {
    return switch (value) {
        .bool => |bool_value| bool_value,
        else => error.InvalidConfig,
    };
}

fn jsonInteger(value: std.json.Value) !i64 {
    return switch (value) {
        .integer => |integer| integer,
        else => error.InvalidConfig,
    };
}

fn parseTopLevelSection(key: []const u8) ?YamlSection {
    if (std.mem.eql(u8, key, "workspace")) return .workspace;
    if (std.mem.eql(u8, key, "sandbox")) return .sandbox;
    if (std.mem.eql(u8, key, "providers")) return .providers;
    if (std.mem.eql(u8, key, "policy")) return .policy;
    if (std.mem.eql(u8, key, "watcher")) return .watcher;
    return null;
}

fn parseWorkspaceMode(value: []const u8) ConfigError!WorkspaceMode {
    if (std.mem.eql(u8, value, "git-worktree")) return .git_worktree;
    return error.InvalidEnumValue;
}

pub fn parseBackendId(value: []const u8) ConfigError!sandbox.BackendId {
    if (std.mem.eql(u8, value, "none")) return .none;
    if (std.mem.eql(u8, value, "host-process")) return .host_process;
    if (std.mem.eql(u8, value, "nucleus")) return .nucleus;
    if (std.mem.eql(u8, value, "remote")) return .remote;
    if (std.mem.eql(u8, value, "vm")) return .vm;
    return error.InvalidEnumValue;
}

pub fn parseSandboxMode(value: []const u8) ConfigError!sandbox.SandboxMode {
    if (std.mem.eql(u8, value, "none")) return .none;
    if (std.mem.eql(u8, value, "host-process")) return .host_process;
    if (std.mem.eql(u8, value, "strict-agent")) return .strict_agent;
    return error.InvalidEnumValue;
}

pub fn parseSandboxRuntime(value: []const u8) ConfigError!sandbox.SandboxRuntime {
    if (std.mem.eql(u8, value, "none")) return .none;
    if (std.mem.eql(u8, value, "native")) return .native;
    if (std.mem.eql(u8, value, "gvisor")) return .gvisor;
    return error.InvalidEnumValue;
}

pub fn parseNetworkMode(value: []const u8) ConfigError!sandbox.NetworkMode {
    if (std.mem.eql(u8, value, "none")) return .none;
    if (std.mem.eql(u8, value, "host")) return .host;
    if (std.mem.eql(u8, value, "bridge")) return .bridge;
    return error.InvalidEnumValue;
}

fn parseCredentialsPassThrough(value: []const u8) ConfigError!CredentialsPassThrough {
    if (std.mem.eql(u8, value, "explicit_only")) return .explicit_only;
    if (std.mem.eql(u8, value, "none")) return .none;
    if (std.mem.eql(u8, value, "inherit")) return .inherit;
    return error.InvalidEnumValue;
}

fn parsePolicyDisposition(value: []const u8) ConfigError!PolicyDisposition {
    if (std.mem.eql(u8, value, "allow")) return .allow;
    if (std.mem.eql(u8, value, "ask")) return .ask;
    if (std.mem.eql(u8, value, "block")) return .block;
    return error.InvalidEnumValue;
}

fn parsePolicyRuleKind(value: []const u8) ConfigError!PolicyRuleKind {
    if (std.mem.eql(u8, value, "action")) return .action;
    if (std.mem.eql(u8, value, "command")) return .command;
    if (std.mem.eql(u8, value, "path")) return .path;
    return error.InvalidEnumValue;
}

fn parseWatcherMode(value: []const u8) ConfigError!WatcherMode {
    if (std.mem.eql(u8, value, "local_only")) return .local_only;
    if (std.mem.eql(u8, value, "remote_approved_context")) return .remote_approved_context;
    return error.InvalidEnumValue;
}

fn parseAdapterMode(value: []const u8) ConfigError!adapters.AdapterMode {
    if (std.mem.eql(u8, value, "headless_json")) return .headless_json;
    if (std.mem.eql(u8, value, "exec_json")) return .exec_json;
    if (std.mem.eql(u8, value, "pty")) return .pty;
    return error.InvalidEnumValue;
}

fn parseAdapterOrderList(arena: std.mem.Allocator, value: []const u8) ![]const adapters.AdapterMode {
    const trimmed = std.mem.trim(u8, value, " \t");
    if (trimmed.len < 2 or trimmed[0] != '[' or trimmed[trimmed.len - 1] != ']') return error.InvalidConfig;
    const inner = std.mem.trim(u8, trimmed[1 .. trimmed.len - 1], " \t");
    if (inner.len == 0) return error.InvalidConfig;

    var result: std.ArrayList(adapters.AdapterMode) = .empty;
    var parts = std.mem.splitScalar(u8, inner, ',');
    while (parts.next()) |part| {
        try result.append(arena, try parseAdapterMode(scalar(part)));
    }
    return result.toOwnedSlice(arena);
}

fn splitYamlKeyValue(line: []const u8) !struct { key: []const u8, value: []const u8 } {
    const index = std.mem.indexOfScalar(u8, line, ':') orelse return error.InvalidConfig;
    const key = std.mem.trim(u8, line[0..index], " \t");
    if (key.len == 0) return error.InvalidConfig;
    return .{
        .key = key,
        .value = std.mem.trim(u8, line[index + 1 ..], " \t"),
    };
}

fn scalar(value: []const u8) []const u8 {
    const trimmed = std.mem.trim(u8, value, " \t");
    if (trimmed.len >= 2) {
        const first = trimmed[0];
        const last = trimmed[trimmed.len - 1];
        if ((first == '"' and last == '"') or (first == '\'' and last == '\'')) return trimmed[1 .. trimmed.len - 1];
    }
    return trimmed;
}

fn stripYamlComment(line: []const u8) []const u8 {
    var quoted: ?u8 = null;
    for (line, 0..) |byte, index| {
        if (quoted) |quote| {
            if (byte == quote) quoted = null;
            continue;
        }
        if (byte == '"' or byte == '\'') {
            quoted = byte;
        } else if (byte == '#') {
            return line[0..index];
        }
    }
    return line;
}

fn leadingSpaces(line: []const u8) usize {
    var count: usize = 0;
    while (count < line.len and line[count] == ' ') : (count += 1) {}
    return count;
}

fn parseBool(value: []const u8) !bool {
    if (std.mem.eql(u8, value, "true")) return true;
    if (std.mem.eql(u8, value, "false")) return false;
    return error.InvalidConfig;
}

fn dupString(arena: std.mem.Allocator, value: []const u8) ![]const u8 {
    return arena.dupe(u8, value);
}

test "project config parser accepts product yaml subset" {
    const yaml =
        \\version: 1
        \\workspace:
        \\  mode: git-worktree
        \\  base_branch: main
        \\  clean_before_run: true
        \\  container_mount: /workspace
        \\
        \\sandbox:
        \\  backend: nucleus
        \\  mode: strict-agent
        \\  runtime: gvisor
        \\  network: none
        \\  workdir: /workspace
        \\  home: /home/agent
        \\  resources:
        \\    memory: 4G
        \\    cpus: 4
        \\    pids: 1024
        \\  toolchain_rootfs: agent-zig-node
        \\  credentials:
        \\    pass_through: explicit_only
        \\
        \\providers:
        \\  claude:
        \\    adapter_order: [headless_json, pty]
        \\  codex:
        \\    adapter_order: [exec_json, pty]
        \\
        \\policy:
        \\  default: ask
        \\  allow:
        \\    - action: read_workspace
        \\    - command: "git diff*"
        \\  ask:
        \\    - action: network_access
        \\  block:
        \\    - path: "~/.ssh/**"
        \\
        \\watcher:
        \\  enabled: true
        \\  mode: local_only
        \\  model: ollama:qwen3-coder
    ;

    var loaded = try parseProjectConfig(std.testing.allocator, yaml);
    defer loaded.deinit();

    try std.testing.expectEqual(supported_version, loaded.value.version);
    try std.testing.expectEqualStrings("main", loaded.value.workspace.base_branch);
    try std.testing.expectEqual(sandbox.BackendId.nucleus, loaded.value.sandbox.backend);
    try std.testing.expectEqual(@as(u16, 4), loaded.value.sandbox.resources.cpus);
    try std.testing.expectEqual(@as(usize, 2), loaded.value.providers.len);
    try std.testing.expectEqual(adapters.AdapterMode.headless_json, loaded.value.providers[0].adapter_order[0]);
    try std.testing.expectEqual(PolicyRuleKind.path, loaded.value.policy.block[0].kind);
    try std.testing.expect(loaded.value.watcher.enabled);
}

test "project config parser accepts schema-shaped json" {
    const json =
        \\{
        \\  "version": 1,
        \\  "workspace": {
        \\    "mode": "git-worktree",
        \\    "base_branch": "main",
        \\    "clean_before_run": true,
        \\    "container_mount": "/workspace"
        \\  },
        \\  "sandbox": {
        \\    "backend": "nucleus",
        \\    "mode": "strict-agent",
        \\    "runtime": "gvisor",
        \\    "network": "none",
        \\    "workdir": "/workspace",
        \\    "home": "/home/agent",
        \\    "resources": { "memory": "4G", "cpus": 4, "pids": 1024 },
        \\    "toolchain_rootfs": "agent-zig-node",
        \\    "credentials": { "pass_through": "explicit_only" }
        \\  },
        \\  "providers": {
        \\    "codex": { "adapter_order": ["exec_json", "pty"] }
        \\  },
        \\  "policy": {
        \\    "default": "ask",
        \\    "allow": [{ "action": "read_workspace" }],
        \\    "ask": [{ "action": "network_access" }],
        \\    "block": [{ "path": "~/.ssh/**" }]
        \\  },
        \\  "watcher": {
        \\    "enabled": false,
        \\    "mode": "local_only"
        \\  }
        \\}
    ;

    var loaded = try parseProjectConfig(std.testing.allocator, json);
    defer loaded.deinit();

    try std.testing.expectEqual(sandbox.SandboxRuntime.gvisor, loaded.value.sandbox.runtime);
    try std.testing.expectEqual(adapters.AdapterMode.exec_json, loaded.value.providers[0].adapter_order[0]);
    try std.testing.expectEqual(PolicyDisposition.ask, loaded.value.policy.default);
}

test "project config loader reads default project file" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(.{
        .sub_path = config_file_name,
        .data =
        \\version: 1
        \\workspace:
        \\  mode: git-worktree
        \\  base_branch: main
        \\  clean_before_run: true
        \\  container_mount: /workspace
        \\sandbox:
        \\  backend: nucleus
        \\  mode: strict-agent
        \\  runtime: gvisor
        \\  network: none
        \\  workdir: /workspace
        \\  home: /home/agent
        \\  resources:
        \\    memory: 4G
        \\    cpus: 4
        \\    pids: 1024
        \\  toolchain_rootfs: agent-zig-node
        \\  credentials:
        \\    pass_through: explicit_only
        \\providers:
        \\  fake:
        \\    adapter_order: [exec_json]
        \\policy:
        \\  default: ask
        \\  allow:
        \\    - action: read_workspace
        \\  ask:
        \\    - action: network_access
        \\  block:
        \\    - path: "~/.ssh/**"
        \\watcher:
        \\  enabled: false
        \\  mode: local_only
        \\
        ,
    });

    var loaded = try loadProjectConfig(std.testing.allocator, tmp.dir);
    defer loaded.deinit();

    try std.testing.expectEqualStrings("main", loaded.value.workspace.base_branch);
    try std.testing.expectEqualStrings("fake", loaded.value.providers[0].name);
}

test "config validation rejects mismatched nucleus profile" {
    var cfg: ProjectConfig = .{
        .version = supported_version,
        .workspace = .{ .base_branch = "main" },
        .sandbox = .{ .backend = .nucleus, .mode = .host_process },
        .providers = &.{.{ .name = "codex", .adapter_order = &.{.pty} }},
        .policy = .{},
        .watcher = .{},
    };

    try std.testing.expectError(error.InvalidSandboxProfile, cfg.validate());
}
