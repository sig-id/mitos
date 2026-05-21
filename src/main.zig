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
const codex_modes = [_]AdapterMode{.exec_json};
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

    const mode_order = modeOrder(provider, options.mode);
    if (mode_order.len == 0) {
        try stderr.print("error: no headless CLI adapter is configured for provider '{s}'\n", .{provider});
        try stderr.writeAll("Run 'mitos providers' to see supported providers.\n");
        return 2;
    }

    var catalog: mitos.providers.catalog.DefaultAdapters = undefined;
    catalog.init();
    const selection = catalog.registry().selectByName(
        allocator,
        provider,
        mode_order,
    ) catch |err| {
        try stderr.print("error: could not select adapter for provider '{s}': {s}\n", .{
            provider,
            @errorName(err),
        });
        try stderr.writeAll("Provider CLIs must be installed and authenticated on PATH.\n");
        return 1;
    };
    defer selection.deinit(allocator);

    const result = mitos.runtime.orchestrator.runAdapter(allocator, std.fs.cwd(), selection.adapter, .{
        .task = .{ .text = task_text },
        .run_id = run_id,
        .workspace_host_path = options.workspace,
    }) catch |err| {
        try stderr.print("error: run failed before artifacts were finalized: {s}\n", .{@errorName(err)});
        return 1;
    };
    defer result.deinit(allocator);

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
        .headless_json, .exec_json => mode,
        else => {
            try stderr.print("error: mode '{s}' is registered but not wired to this CLI yet\n", .{value});
            return error.InvalidArguments;
        },
    };
}

fn modeOrder(provider: []const u8, requested: ?AdapterMode) []const AdapterMode {
    if (requested) |mode| {
        return switch (mode) {
            .headless_json => &[_]AdapterMode{.headless_json},
            .exec_json => &[_]AdapterMode{.exec_json},
            else => &no_modes,
        };
    }

    if (std.mem.eql(u8, provider, "fake")) return &fake_modes;
    if (std.mem.eql(u8, provider, "codex")) return &codex_modes;
    if (std.mem.eql(u8, provider, "claude")) return &claude_modes;
    if (std.mem.eql(u8, provider, "gemini")) return &gemini_modes;
    return &no_modes;
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
        \\  run        Run a task through a built-in headless adapter.
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
        \\  --mode <mode>         Adapter mode: exec_json or headless_json.
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
        \\  codex   exec_json                 runs `codex exec --json`
        \\  claude  headless_json             runs `claude --print --output-format stream-json`
        \\  gemini  headless_json             runs `gemini --prompt ... --output-format stream-json`
        \\
        \\Registered but not wired to this CLI yet:
        \\  pty adapters require terminal-core attachment and runtime prompt routing.
        \\
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

test "run option parser rejects unwired pty mode" {
    var err: std.io.Writer.Allocating = .init(std.testing.allocator);
    defer err.deinit();

    const args = [_][]const u8{ "--provider", "codex", "--task", "hello", "--mode", "pty" };
    try std.testing.expectError(error.InvalidArguments, parseRunOptions(&args, &err.writer));
}
