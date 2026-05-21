const std = @import("std");
const mitos = @import("mitos");

const AdapterMode = mitos.adapters.AdapterMode;

const RunOptions = struct {
    provider: ?[]const u8 = null,
    task: ?[]const u8 = null,
    task_file: ?[]const u8 = null,
    run_id: ?[]const u8 = null,
    workspace: []const u8 = ".",
    mode: ?AdapterMode = null,
    help: bool = false,
};

const fake_modes = [_]AdapterMode{ .exec_json, .headless_json };
const codex_modes = [_]AdapterMode{ .exec_json, .pty };
const claude_modes = [_]AdapterMode{.headless_json};
const gemini_modes = [_]AdapterMode{.headless_json};
const no_modes = [_]AdapterMode{};

pub fn main() !void {
    const allocator = std.heap.page_allocator;
    const args = try std.process.argsAlloc(allocator);
    defer std.process.argsFree(allocator, args);

    var stdout_buffer: [4096]u8 = undefined;
    var stdout_writer = std.fs.File.stdout().writer(&stdout_buffer);
    const stdout = &stdout_writer.interface;

    var stderr_buffer: [4096]u8 = undefined;
    var stderr_writer = std.fs.File.stderr().writer(&stderr_buffer);
    const stderr = &stderr_writer.interface;

    const exit_code = runCli(allocator, args, stdout, stderr) catch |err| {
        try stderr.print("error: {s}\n", .{@errorName(err)});
        try stderr.writeAll("Try 'mitos --help'.\n");
        try stdout.flush();
        try stderr.flush();
        std.process.exit(1);
    };

    try stdout.flush();
    try stderr.flush();
    if (exit_code != 0) std.process.exit(exit_code);
}

fn runCli(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdout: anytype,
    stderr: anytype,
) !u8 {
    if (args.len <= 1) {
        try printHelp(stdout);
        return 0;
    }

    const command = args[1];
    if (isHelp(command)) {
        try printHelp(stdout);
        return 0;
    }
    if (std.mem.eql(u8, command, "--version") or
        std.mem.eql(u8, command, "-V") or
        std.mem.eql(u8, command, "version"))
    {
        try printVersion(stdout);
        return 0;
    }
    if (std.mem.eql(u8, command, "providers")) {
        try printProviders(stdout);
        return 0;
    }
    if (std.mem.eql(u8, command, "run")) {
        return try runCommand(allocator, args[2..], stdout, stderr);
    }

    try stderr.print("error: unknown command '{s}'\n", .{command});
    try stderr.writeAll("Try 'mitos --help'.\n");
    return 2;
}

fn runCommand(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdout: anytype,
    stderr: anytype,
) !u8 {
    const options = parseRunOptions(args, stderr) catch return 2;
    if (options.help) {
        try printRunHelp(stdout);
        return 0;
    }

    const provider = options.provider orelse {
        try stderr.writeAll("error: run requires --provider <name>\n");
        try stderr.writeAll("Try 'mitos run --help'.\n");
        return 2;
    };

    if ((options.task == null and options.task_file == null) or
        (options.task != null and options.task_file != null))
    {
        try stderr.writeAll("error: run requires exactly one of --task <text> or --task-file <path>\n");
        try stderr.writeAll("Try 'mitos run --help'.\n");
        return 2;
    }

    var task_file_contents: ?[]u8 = null;
    defer if (task_file_contents) |bytes| allocator.free(bytes);

    const task_text = if (options.task) |task|
        task
    else blk: {
        const path = options.task_file.?;
        task_file_contents = try std.fs.cwd().readFileAlloc(allocator, path, 1024 * 1024);
        break :blk task_file_contents.?;
    };

    if (std.mem.trim(u8, task_text, " \t\r\n").len == 0) {
        try stderr.writeAll("error: task text is empty\n");
        return 2;
    }

    var generated_run_id: ?[]u8 = null;
    defer if (generated_run_id) |run_id| allocator.free(run_id);

    const run_id = options.run_id orelse blk: {
        generated_run_id = try std.fmt.allocPrint(
            allocator,
            "run-{d}",
            .{std.time.milliTimestamp()},
        );
        break :blk generated_run_id.?;
    };

    var loaded_config = try loadProjectConfigIfPresent(allocator);
    defer if (loaded_config) |*loaded| loaded.deinit();
    const project_config = if (loaded_config) |loaded| loaded.value else null;
    const configured_provider = configuredProvider(project_config, provider);
    const effective_project_config = if (configured_provider != null) project_config else null;

    const mode_order = modeOrder(provider, options.mode, configured_provider);
    if (mode_order.len == 0) {
        try stderr.print("error: no CLI adapter is configured for provider '{s}'\n", .{provider});
        try stderr.writeAll("Run 'mitos providers' to see supported providers.\n");
        return 2;
    }

    var catalog: mitos.providers.catalog.DefaultAdapters = undefined;
    catalog.init();
    const registry = catalog.registry();
    const skip_host_detection = if (effective_project_config) |cfg|
        cfg.sandbox.backend == .nucleus
    else
        false;
    const selection = (if (skip_host_detection)
        registry.selectByNameNoDetect(provider, mode_order)
    else
        registry.selectByName(allocator, provider, mode_order)) catch |err| {
        try stderr.print("error: could not select adapter for provider '{s}': {s}\n", .{
            provider,
            @errorName(err),
        });
        if (skip_host_detection) {
            try stderr.writeAll("Check the configured provider adapter_order in .agent-runtime.yaml.\n");
        } else {
            try stderr.writeAll("Provider CLIs must be installed and authenticated on PATH.\n");
        }
        return 1;
    };
    defer selection.deinit(allocator);

    const run_id_value = try mitos.runtime.RunId.init(run_id);
    var absolute_workspace: ?[]u8 = null;
    defer if (absolute_workspace) |path| allocator.free(path);
    const workspace_root = if (effective_project_config != null) blk: {
        absolute_workspace = try std.fs.cwd().realpathAlloc(allocator, options.workspace);
        break :blk absolute_workspace.?;
    } else options.workspace;

    var prepared_workspace: ?mitos.workspace.PreparedWorkspace = null;
    defer if (prepared_workspace) |*prepared| prepared.deinit(allocator);

    const workspace_host_path = if (effective_project_config) |cfg| blk: {
        if (cfg.workspace.mode == .git_worktree) {
            prepared_workspace = try mitos.workspace.prepareGitWorktree(
                allocator,
                workspace_root,
                run_id_value,
                cfg.workspace,
            );
            break :blk prepared_workspace.?.plan.host_path;
        }
        break :blk workspace_root;
    } else workspace_root;

    var owned_policy_config: ?mitos.policy.engine.OwnedPolicyConfig = null;
    defer if (owned_policy_config) |*owned| owned.deinit();
    const runtime_policy = if (effective_project_config) |cfg| blk: {
        owned_policy_config = try mitos.policy.engine.fromProjectPolicy(allocator, cfg.policy);
        break :blk owned_policy_config.?.config;
    } else mitos.policy.engine.PolicyConfig{};

    var terminal: ?mitos.terminal_ghostty.ManagedTerminal = null;
    defer if (terminal) |*managed| managed.deinit();
    var terminal_core: ?mitos.terminal.TerminalCore = null;
    if (selection.mode == .pty) {
        terminal = try mitos.terminal_ghostty.ManagedTerminal.init(allocator, .{});
        if (terminal) |*managed| terminal_core = managed.asCore();
    }

    var provider_command: ?mitos.providers.headless.HeadlessCommand = null;
    defer if (provider_command) |command| command.deinit(allocator);
    var nucleus_cli: mitos.nucleus.NucleusCliTransport = .{ .allocator = allocator };
    var nucleus_backend: mitos.nucleus.NucleusBackend = undefined;
    var use_nucleus = false;
    defer if (use_nucleus) nucleus_backend.deinit();
    var sandbox_mounts: [3]mitos.sandbox.MountSpec = undefined;
    var sandbox_launch: mitos.sandbox.SandboxLaunchConfig = undefined;
    var sandbox_options: ?mitos.runtime.orchestrator.AdapterSandboxOptions = null;
    const workspace_container_path: ?[]const u8 = if (effective_project_config) |cfg| blk: {
        if (cfg.sandbox.backend != .nucleus) break :blk null;
        if (selection.mode == .pty) {
            try stderr.writeAll("error: nucleus sandbox execution is only wired for headless provider modes\n");
            return 2;
        }

        provider_command = try buildProviderCommand(
            allocator,
            selection.adapter.id.value,
            .{ .text = task_text },
            cfg.sandbox.workdir,
        );
        sandbox_mounts = .{
            .{
                .host_path = workspace_host_path,
                .container_path = cfg.workspace.container_mount,
                .access = .read_write_execute,
            },
            .{
                .container_path = cfg.sandbox.home,
                .access = .read_write,
                .type = .tmpfs,
            },
            .{
                .host_path = workspace_root,
                .container_path = "/context",
                .access = .read_only,
                .optional = true,
            },
        };
        sandbox_launch = .{
            .run_id = run_id_value,
            .backend = .nucleus,
            .mode = cfg.sandbox.mode,
            .runtime = cfg.sandbox.runtime,
            .command = .{
                .argv = provider_command.?.argv,
                .cwd = cfg.sandbox.workdir,
            },
            .mounts = &sandbox_mounts,
            .resources = cfg.sandbox.resources,
            .network = cfg.sandbox.network,
            .toolchain_rootfs = cfg.sandbox.toolchain_rootfs,
        };
        nucleus_backend = mitos.nucleus.NucleusBackend.init(allocator, nucleus_cli.transport());
        use_nucleus = true;
        sandbox_options = .{
            .backend = &nucleus_backend.backend,
            .launch = sandbox_launch,
        };
        break :blk cfg.workspace.container_mount;
    } else null;

    const result = mitos.runtime.orchestrator.runAdapter(allocator, std.fs.cwd(), selection.adapter, .{
        .task = .{ .text = task_text },
        .run_id = run_id,
        .workspace_host_path = workspace_host_path,
        .workspace_container_path = workspace_container_path,
        .policy_config = runtime_policy,
        .sandbox_backend = sandbox_options,
        .require_sandbox_execution = use_nucleus,
        .terminal_core = if (terminal_core) |*core| core else null,
    }) catch |err| {
        try stderr.print("error: run failed before artifacts were finalized: {s}\n", .{@errorName(err)});
        return 1;
    };
    defer result.deinit(allocator);

    persistWorkspaceArtifacts(
        allocator,
        result.relative_path,
        workspace_host_path,
        &prepared_workspace,
        mitos.redaction.Redactor{},
    ) catch |err| {
        try stderr.print("warning: workspace artifacts were not finalized: {s}\n", .{@errorName(err)});
    };

    try stdout.print("run_id: {s}\n", .{result.run_id.value});
    try stdout.print("provider: {s}\n", .{provider});
    try stdout.print("adapter: {s}\n", .{selection.adapter.id.value});
    try stdout.print("adapter_status: {s}\n", .{@tagName(result.adapter_status)});
    try stdout.print("final_state: {s}\n", .{@tagName(result.final_state)});
    try stdout.print("artifacts: {s}\n", .{result.relative_path});
    try stdout.print("report: {s}/{s}\n", .{ result.relative_path, result.report_path });

    return if (result.final_state == .completed) 0 else 1;
}

fn parseRunOptions(args: []const []const u8, stderr: anytype) !RunOptions {
    var options: RunOptions = .{};
    var i: usize = 0;
    while (i < args.len) : (i += 1) {
        const arg = args[i];
        if (isHelp(arg)) {
            options.help = true;
        } else if (std.mem.eql(u8, arg, "--provider")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --provider requires a value\n");
                return error.InvalidArguments;
            }
            options.provider = args[i];
        } else if (stripPrefix(arg, "--provider=")) |value| {
            options.provider = value;
        } else if (std.mem.eql(u8, arg, "--task")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --task requires a value\n");
                return error.InvalidArguments;
            }
            options.task = args[i];
        } else if (stripPrefix(arg, "--task=")) |value| {
            options.task = value;
        } else if (std.mem.eql(u8, arg, "--task-file")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --task-file requires a value\n");
                return error.InvalidArguments;
            }
            options.task_file = args[i];
        } else if (stripPrefix(arg, "--task-file=")) |value| {
            options.task_file = value;
        } else if (std.mem.eql(u8, arg, "--run-id")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --run-id requires a value\n");
                return error.InvalidArguments;
            }
            options.run_id = args[i];
        } else if (stripPrefix(arg, "--run-id=")) |value| {
            options.run_id = value;
        } else if (std.mem.eql(u8, arg, "--workspace")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --workspace requires a value\n");
                return error.InvalidArguments;
            }
            options.workspace = args[i];
        } else if (stripPrefix(arg, "--workspace=")) |value| {
            options.workspace = value;
        } else if (std.mem.eql(u8, arg, "--mode")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --mode requires a value\n");
                return error.InvalidArguments;
            }
            options.mode = try parseHeadlessMode(args[i], stderr);
        } else if (stripPrefix(arg, "--mode=")) |value| {
            options.mode = try parseHeadlessMode(value, stderr);
        } else {
            try stderr.print("error: unknown run option '{s}'\n", .{arg});
            return error.InvalidArguments;
        }
    }

    return options;
}

fn parseHeadlessMode(value: []const u8, stderr: anytype) !AdapterMode {
    const mode = AdapterMode.fromName(value) orelse {
        try stderr.print("error: unknown adapter mode '{s}'\n", .{value});
        return error.InvalidArguments;
    };
    return switch (mode) {
        .headless_json, .exec_json, .pty => mode,
        else => {
            try stderr.print("error: mode '{s}' is registered but not wired to this CLI yet\n", .{value});
            return error.InvalidArguments;
        },
    };
}

fn modeOrder(
    provider: []const u8,
    requested: ?AdapterMode,
    configured_provider: ?mitos.config.ProviderConfig,
) []const AdapterMode {
    if (requested) |mode| {
        return switch (mode) {
            .headless_json => &[_]AdapterMode{.headless_json},
            .exec_json => &[_]AdapterMode{.exec_json},
            .pty => &[_]AdapterMode{.pty},
            else => &no_modes,
        };
    }

    if (configured_provider) |configured| return configured.adapter_order;

    if (std.mem.eql(u8, provider, "fake")) return &fake_modes;
    if (std.mem.eql(u8, provider, "codex")) return &codex_modes;
    if (std.mem.eql(u8, provider, "claude")) return &claude_modes;
    if (std.mem.eql(u8, provider, "gemini")) return &gemini_modes;
    return &no_modes;
}

fn configuredProvider(
    project_config: ?mitos.config.ProjectConfig,
    provider: []const u8,
) ?mitos.config.ProviderConfig {
    const cfg = project_config orelse return null;
    for (cfg.providers) |configured| {
        if (std.mem.eql(u8, configured.name, provider)) return configured;
    }
    return null;
}

fn loadProjectConfigIfPresent(allocator: std.mem.Allocator) !?mitos.config.LoadedProjectConfig {
    return mitos.config.loadProjectConfig(allocator, std.fs.cwd()) catch |err| switch (err) {
        error.FileNotFound => null,
        else => err,
    };
}

fn buildProviderCommand(
    allocator: std.mem.Allocator,
    adapter_id: []const u8,
    task: mitos.adapters.Task,
    cwd: []const u8,
) !mitos.providers.headless.HeadlessCommand {
    if (std.mem.eql(u8, adapter_id, "codex.exec")) {
        return mitos.providers.codex.buildExecCommand(allocator, task, cwd);
    }
    if (std.mem.eql(u8, adapter_id, "claude.print")) {
        return mitos.providers.claude.buildPrintCommand(allocator, task, cwd);
    }
    if (std.mem.eql(u8, adapter_id, "gemini.headless")) {
        return mitos.providers.gemini.buildHeadlessCommand(allocator, task, cwd);
    }
    return error.UnsupportedSandboxAdapter;
}

fn persistWorkspaceArtifacts(
    allocator: std.mem.Allocator,
    run_relative_path: []const u8,
    workspace_path: []const u8,
    prepared_workspace: *?mitos.workspace.PreparedWorkspace,
    redactor: mitos.redaction.Redactor,
) !void {
    var run_dir = try std.fs.cwd().openDir(run_relative_path, .{});
    defer run_dir.close();

    const snapshot = if (prepared_workspace.*) |*prepared|
        try mitos.workspace.finalizeGitWorktree(allocator, prepared, true)
    else
        try mitos.workspace.captureWorkspaceSnapshot(allocator, workspace_path);
    defer snapshot.deinit(allocator);

    const redacted_diff = try redactor.redactAlloc(allocator, .report, snapshot.diff_patch);
    defer allocator.free(redacted_diff);
    try run_dir.writeFile(.{
        .sub_path = mitos.trace.defaultArtifactPath(.diff_patch),
        .data = redacted_diff,
    });

    const files_json = try renderChangedFilesJson(allocator, snapshot.changed_files);
    defer allocator.free(files_json);
    const redacted_files = try redactor.redactAlloc(allocator, .report, files_json);
    defer allocator.free(redacted_files);
    try run_dir.writeFile(.{
        .sub_path = mitos.trace.defaultArtifactPath(.files_changed),
        .data = redacted_files,
    });

    _ = try mitos.report.generate(allocator, run_dir, redactor);
}

fn renderChangedFilesJson(
    allocator: std.mem.Allocator,
    changes: []const mitos.workspace.FileChange,
) ![]u8 {
    var out: std.io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();

    var json: std.json.Stringify = .{
        .writer = &out.writer,
        .options = .{ .whitespace = .indent_2 },
    };
    try json.beginArray();
    for (changes) |change| {
        try json.beginObject();
        try json.objectField("status");
        try json.write(change.status);
        try json.objectField("path");
        try json.write(change.path);
        try json.endObject();
    }
    try json.endArray();
    try out.writer.writeByte('\n');
    return out.toOwnedSlice();
}

fn printVersion(writer: anytype) !void {
    try writer.print("mitos {s}\n", .{mitos.version});
}

fn printHelp(writer: anytype) !void {
    try writer.print(
        \\mitos {s}
        \\
        \\Usage:
        \\  mitos run --provider <name> --task <text> [--run-id <id>] [--workspace <path>]
        \\  mitos run --provider <name> --task-file <path> [--mode <mode>]
        \\  mitos providers
        \\  mitos version
        \\
        \\Commands:
        \\  run        Run a task through a built-in adapter.
        \\  providers  List built-in providers and currently wired adapter modes.
        \\  version    Print the mitos version.
        \\
        \\Examples:
        \\  mitos run --provider fake --task "Summarize this repo"
        \\  mitos run --provider codex --task "Fix failing tests"
        \\
        \\Artifacts are written under .runs/<run-id>/.
        \\
    , .{mitos.version});
}

fn printRunHelp(writer: anytype) !void {
    try writer.writeAll(
        \\Usage:
        \\  mitos run --provider <name> --task <text> [options]
        \\  mitos run --provider <name> --task-file <path> [options]
        \\
        \\Options:
        \\  --provider <name>     Provider to run: fake, codex, claude, gemini.
        \\  --task <text>         Task text to pass to the provider.
        \\  --task-file <path>    Read task text from a file.
        \\  --run-id <id>         Artifact directory name under .runs/.
        \\  --workspace <path>    Workspace path used as provider cwd. Defaults to ".".
        \\  --mode <mode>         Adapter mode: exec_json, headless_json, or pty.
        \\  -h, --help            Show this help.
        \\
        \\Provider CLIs must already be installed and authenticated for real providers.
        \\
    );
}

fn printProviders(writer: anytype) !void {
    try writer.writeAll(
        \\Built-in providers:
        \\  fake    exec_json, headless_json  local deterministic test adapter
        \\  codex   exec_json, pty            runs `codex exec --json` or interactive `codex`
        \\  claude  headless_json             runs `claude --print --output-format stream-json`
        \\  gemini  headless_json             runs `gemini --prompt ... --output-format stream-json`
    );
}

fn isHelp(arg: []const u8) bool {
    return std.mem.eql(u8, arg, "--help") or
        std.mem.eql(u8, arg, "-h") or
        std.mem.eql(u8, arg, "help");
}

fn stripPrefix(value: []const u8, prefix: []const u8) ?[]const u8 {
    if (!std.mem.startsWith(u8, value, prefix)) return null;
    return value[prefix.len..];
}

test "cli module imports library metadata" {
    try std.testing.expect(mitos.version.len > 0);
}

test "cli help describes run command" {
    var out: std.io.Writer.Allocating = .init(std.testing.allocator);
    defer out.deinit();

    try printHelp(&out.writer);
    const text = try out.toOwnedSlice();
    defer std.testing.allocator.free(text);

    try std.testing.expect(std.mem.containsAtLeast(u8, text, 1, "mitos run --provider"));
    try std.testing.expect(std.mem.containsAtLeast(u8, text, 1, "Artifacts are written"));
}

test "run option parser accepts task and provider" {
    var err: std.io.Writer.Allocating = .init(std.testing.allocator);
    defer err.deinit();

    const args = [_][]const u8{
        "--provider",
        "fake",
        "--task",
        "hello",
        "--run-id=run-test",
        "--workspace",
        "/tmp/work",
    };
    const options = try parseRunOptions(&args, &err.writer);

    try std.testing.expectEqualStrings("fake", options.provider.?);
    try std.testing.expectEqualStrings("hello", options.task.?);
    try std.testing.expectEqualStrings("run-test", options.run_id.?);
    try std.testing.expectEqualStrings("/tmp/work", options.workspace);
}

test "run option parser accepts pty mode" {
    var err: std.io.Writer.Allocating = .init(std.testing.allocator);
    defer err.deinit();

    const args = [_][]const u8{ "--provider", "codex", "--task", "hello", "--mode", "pty" };
    const options = try parseRunOptions(&args, &err.writer);
    try std.testing.expectEqual(AdapterMode.pty, options.mode.?);
}

test "mode order honors project provider config before built-in defaults" {
    const cfg: mitos.config.ProjectConfig = .{
        .version = mitos.config.supported_version,
        .workspace = .{ .base_branch = "main" },
        .sandbox = .{},
        .providers = &.{
            .{ .name = "codex", .adapter_order = &.{ .pty, .exec_json } },
        },
        .policy = .{},
        .watcher = .{},
    };

    const order = modeOrder("codex", null, configuredProvider(cfg, "codex"));
    try std.testing.expectEqual(AdapterMode.pty, order[0]);
    try std.testing.expectEqual(AdapterMode.exec_json, order[1]);
}
