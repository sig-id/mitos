const std = @import("std");
const mitos = @import("mitos");

const TransportName = mitos.transport.TransportName;

const RunOptions = struct {
    provider: ?[]const u8 = null,
    task: ?[]const u8 = null,
    task_file: ?[]const u8 = null,
    run_id: ?[]const u8 = null,
    workspace: []const u8 = ".",
    transport_name: ?TransportName = null,
    env: []const []const u8 = &.{},
    help: bool = false,
};

const fake_transports = [_]TransportName{.stdio_json};
const codex_transports = [_]TransportName{.stdio_json};
const claude_transports = [_]TransportName{.stdio_json};
const gemini_transports = [_]TransportName{.stdio_json};
const opencode_transports = [_]TransportName{.stdio_json};
const pi_transports = [_]TransportName{.stdio_json};
const no_transports = [_]TransportName{};

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
    defer {
        // Free the env slice if it was dynamically allocated
        if (options.env.len > 0) {
            const default_empty: []const []const u8 = &.{};
            if (options.env.ptr != default_empty.ptr) {
                std.heap.page_allocator.free(options.env);
            }
        }
    }
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
        task_file_contents = try readTaskFileNoSymlink(allocator, std.fs.cwd(), path);
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

    const configured_order = transportOrder(provider, options.transport_name, configured_provider);
    const transport_order = stdioTransportOrder(configured_order) orelse {
        try stderr.print("error: no stdio-json transport is configured for provider '{s}'\n", .{provider});
        try stderr.writeAll("Run 'mitos providers' to see supported providers.\n");
        return 2;
    };
    if (transport_order.len == 0) {
        try stderr.print("error: no transport is configured for provider '{s}'\n", .{provider});
        try stderr.writeAll("Run 'mitos providers' to see supported providers.\n");
        return 2;
    }

    const run_id_value = mitos.runtime.RunId.init(run_id) catch |err| switch (err) {
        error.EmptyRunId, error.UnsafeRunId => {
            try stderr.writeAll("error: --run-id must be a safe trace identifier\n");
            return 2;
        },
    };

    var env_vars: std.ArrayList(mitos.adapters.EnvVar) = .empty;
    for (options.env) |env_str| {
        const eq_idx = std.mem.indexOfScalar(u8, env_str, '=') orelse {
            try stderr.print("error: invalid env var '{s}' (missing =)\n", .{env_str});
            return 2;
        };
        const name = try allocator.dupe(u8, env_str[0..eq_idx]);
        const value = try allocator.dupe(u8, env_str[eq_idx + 1 ..]);
        try env_vars.append(allocator, .{ .name = name, .value = value });
    }
    defer {
        for (env_vars.items) |*env| {
            allocator.free(env.name);
            allocator.free(env.value);
        }
        env_vars.deinit(allocator);
    }

    const resolved = mitos.providers.launch.resolve(allocator, .{
        .provider = provider,
        .task = task_text,
        .transport_order = transport_order,
        .cwd = options.workspace,
    }) catch |err| {
        try stderr.print("error: could not resolve stdio-json launch for provider '{s}': {s}\n", .{
            provider,
            @errorName(err),
        });
        return 1;
    };
    defer resolved.deinit(allocator);

    const started_at_ms = std.time.milliTimestamp();
    const trace_redactor = mitos.redaction.Redactor{};
    const redacted_task_text = try redactProviderEventAlloc(allocator, trace_redactor, task_text);
    defer allocator.free(redacted_task_text);

    var trace = try mitos.trace.file_store.FileTrace.create(allocator, std.fs.cwd(), run_id_value, .{
        .created_at_ms = started_at_ms,
        .provider = provider,
        .task = redacted_task_text,
    });
    defer trace.deinit();

    _ = try trace.appendEvent(started_at_ms, .{ .run_created = .{ .task = redacted_task_text, .provider = provider } });
    _ = try trace.appendEvent(started_at_ms, .{ .run_state_changed = .{
        .from = .created,
        .to = .starting_adapter,
        .reason = "stdio-json launch resolved",
    } });
    _ = try trace.appendEvent(started_at_ms, .{ .adapter_started = .{
        .provider = resolved.provider.value,
        .adapter = resolved.adapter.value,
        .mode = resolved.transport_name.name(),
    } });
    _ = try trace.appendEvent(started_at_ms, .{ .run_state_changed = .{
        .from = .starting_adapter,
        .to = .running,
        .reason = "provider command spawned",
    } });
    const redacted_argv = try redactStringListAlloc(
        allocator,
        trace_redactor,
        resolved.argv,
    );
    defer freeStringList(allocator, redacted_argv);

    const redacted_cwd = try redactProviderEventAlloc(
        allocator,
        trace_redactor,
        resolved.cwd orelse options.workspace,
    );
    defer allocator.free(redacted_cwd);

    _ = try trace.appendEvent(started_at_ms, .{ .process_spawned = .{
        .argv = redacted_argv,
        .cwd = redacted_cwd,
        .pid = null,
    } });

    const command: mitos.providers.headless.HeadlessCommand = .{
        .argv = resolved.argv,
        .cwd = resolved.cwd orelse options.workspace,
        .environment = env_vars.items,
    };

    const result = mitos.providers.headless.execute(allocator, command, .{}) catch |err| {
        const failed_at_ms = std.time.milliTimestamp();
        _ = try trace.appendEvent(failed_at_ms, .{ .error_reported = .{
            .message = @errorName(err),
            .component = "stdio-json",
        } });
        _ = try trace.appendEvent(failed_at_ms, .{ .run_state_changed = .{
            .from = .running,
            .to = .failed,
            .reason = "provider command failed to launch",
        } });
        _ = try mitos.report.generate(allocator, trace.run_dir, mitos.redaction.Redactor{});
        try stderr.print("error: run failed before artifacts were finalized: {s}\n", .{@errorName(err)});
        return 1;
    };
    defer result.deinit(allocator);

    if (result.stdout.len > 0) {
        const redacted_stdout = try redactProviderEventAlloc(allocator, trace_redactor, result.stdout);
        defer allocator.free(redacted_stdout);

        _ = try trace.appendEvent(std.time.milliTimestamp(), .{ .adapter_output = .{
            .stream = .stdout,
            .bytes = redacted_stdout,
        } });
        var sink_context = TraceAdapterSink{
            .trace = &trace,
            .allocator = allocator,
            .redactor = trace_redactor,
        };
        try mitos.providers.headless.emitNormalizedJsonl(
            allocator,
            sink_context.sink(),
            resolved.provider,
            result.stdout,
        );
    }
    if (result.stderr.len > 0) {
        const redacted_stderr = try redactProviderEventAlloc(allocator, trace_redactor, result.stderr);
        defer allocator.free(redacted_stderr);

        _ = try trace.appendEvent(std.time.milliTimestamp(), .{ .adapter_output = .{
            .stream = .stderr,
            .bytes = redacted_stderr,
        } });
    }

    const finished_at_ms = std.time.milliTimestamp();
    _ = try trace.appendEvent(finished_at_ms, .{ .process_exited = .{
        .pid = null,
        .exit_code = result.exit_code,
        .signal = result.signal,
    } });

    const final_output = try mitos.providers.headless.extractFinalOutputAlloc(allocator, result.stdout);
    defer allocator.free(final_output);
    const redacted_final_output = try redactProviderEventAlloc(allocator, trace_redactor, final_output);
    defer allocator.free(redacted_final_output);

    _ = try trace.writeArtifact(.final_output, redacted_final_output);
    if (final_output.len > 0) {
        _ = try trace.appendEvent(finished_at_ms, .{ .final_output = .{ .text = redacted_final_output } });
    }

    const adapter_status = result.status();
    const final_state: mitos.runtime.RunState = if (adapter_status == .completed) .completed else .failed;
    _ = try trace.appendEvent(finished_at_ms, .{ .run_state_changed = .{
        .from = .running,
        .to = final_state,
        .reason = if (final_state == .completed) "stdio-json provider completed" else "stdio-json provider failed",
    } });

    _ = try mitos.report.generate(allocator, trace.run_dir, mitos.redaction.Redactor{});

    const relative_path = try std.fs.path.join(allocator, &.{ ".runs", run_id });
    defer allocator.free(relative_path);

    try stdout.print("run_id: {s}\n", .{run_id_value.value});
    try stdout.print("provider: {s}\n", .{provider});
    try stdout.print("adapter: {s}\n", .{resolved.adapter.value});
    try stdout.print("adapter_status: {s}\n", .{@tagName(adapter_status)});
    try stdout.print("final_state: {s}\n", .{@tagName(final_state)});
    try stdout.print("artifacts: {s}\n", .{relative_path});
    try stdout.print("report: {s}/{s}\n", .{ relative_path, mitos.trace.defaultArtifactPath(.report) });

    return if (final_state == .completed) 0 else 1;
}

fn parseRunOptions(args: []const []const u8, stderr: anytype) !RunOptions {
    var options: RunOptions = .{};
    var env_list = std.ArrayList([]const u8).empty;
    defer env_list.deinit(std.heap.page_allocator);

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
            options.transport_name = try parseTransportFlag(args[i], stderr);
        } else if (stripPrefix(arg, "--mode=")) |value| {
            options.transport_name = try parseTransportFlag(value, stderr);
        } else if (std.mem.eql(u8, arg, "--transport")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --transport requires a value\n");
                return error.InvalidArguments;
            }
            options.transport_name = try parseTransportFlag(args[i], stderr);
        } else if (stripPrefix(arg, "--transport=")) |value| {
            options.transport_name = try parseTransportFlag(value, stderr);
        } else if (std.mem.eql(u8, arg, "--env")) {
            i += 1;
            if (i >= args.len) {
                try stderr.writeAll("error: --env requires a value\n");
                return error.InvalidArguments;
            }
            try env_list.append(std.heap.page_allocator, args[i]);
        } else if (stripPrefix(arg, "--env=")) |value| {
            try env_list.append(std.heap.page_allocator, value);
        } else {
            try stderr.print("error: unknown run option '{s}'\n", .{arg});
            return error.InvalidArguments;
        }
    }

    // Convert env_list to a slice that will live beyond this function
    if (env_list.items.len > 0) {
        const duped = try std.heap.page_allocator.dupe([]const u8, env_list.items);
        options.env = duped;
    }

    return options;
}

fn parseTransportFlag(value: []const u8, stderr: anytype) !TransportName {
    const transport_name = TransportName.fromName(value) orelse {
        try stderr.print("error: unknown transport '{s}'\n", .{value});
        return error.InvalidArguments;
    };
    return switch (transport_name) {
        .stdio_json => transport_name,
        .pty => {
            try stderr.writeAll("error: pty transport is owned by mitos-term; `mitos run` only supports stdio-json\n");
            return error.InvalidArguments;
        },
        else => {
            try stderr.print("error: transport '{s}' is registered but not wired to this CLI yet\n", .{value});
            return error.InvalidArguments;
        },
    };
}

fn transportOrder(
    provider: []const u8,
    requested: ?TransportName,
    configured_provider: ?mitos.config.ProviderConfig,
) []const TransportName {
    if (requested) |transport_name| {
        return switch (transport_name) {
            .stdio_json => &[_]TransportName{.stdio_json},
            .pty => &no_transports,
            else => &no_transports,
        };
    }

    if (configured_provider) |configured| return configured.transport_order;

    if (std.mem.eql(u8, provider, "fake")) return &fake_transports;
    if (std.mem.eql(u8, provider, "codex")) return &codex_transports;
    if (std.mem.eql(u8, provider, "claude")) return &claude_transports;
    if (std.mem.eql(u8, provider, "claudecode") or std.mem.eql(u8, provider, "claude-code")) return &claude_transports;
    if (std.mem.eql(u8, provider, "gemini")) return &gemini_transports;
    if (std.mem.eql(u8, provider, "opencode")) return &opencode_transports;
    if (std.mem.eql(u8, provider, "pi")) return &pi_transports;
    return &no_transports;
}

fn stdioTransportOrder(order: []const TransportName) ?[]const TransportName {
    for (order) |transport_name| {
        if (transport_name == .stdio_json) return &[_]TransportName{.stdio_json};
    }
    return null;
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

fn readTaskFileNoSymlink(
    allocator: std.mem.Allocator,
    base_dir: std.fs.Dir,
    path: []const u8,
) ![]u8 {
    var it = try std.fs.path.componentIterator(path);
    var current: std.fs.Dir = undefined;
    var current_owned = false;
    if (it.root()) |root| {
        current = try std.fs.openDirAbsolute(root, .{ .no_follow = true });
        current_owned = true;
    } else {
        current = base_dir;
    }
    defer if (current_owned) current.close();

    var component = it.next() orelse return error.FileNotFound;
    while (true) {
        const next_component = it.next();
        if (next_component) |next| {
            try rejectSymlink(current, component.name);
            const child = current.openDir(component.name, .{ .no_follow = true }) catch |err| switch (err) {
                error.SymLinkLoop => return error.SymlinkRejected,
                else => |e| return e,
            };
            if (current_owned) current.close();
            current = child;
            current_owned = true;
            component = next;
            continue;
        }

        return readFileFromDirNoSymlink(allocator, current, component.name);
    }
}

fn readFileFromDirNoSymlink(
    allocator: std.mem.Allocator,
    dir: std.fs.Dir,
    path: []const u8,
) ![]u8 {
    try rejectSymlink(dir, path);
    const fd = std.posix.openat(
        dir.fd,
        path,
        .{ .ACCMODE = .RDONLY, .NOFOLLOW = true, .CLOEXEC = true },
        0,
    ) catch |err| switch (err) {
        error.SymLinkLoop => return error.SymlinkRejected,
        else => |e| return e,
    };

    const file: std.fs.File = .{ .handle = fd };
    defer file.close();

    return file.readToEndAlloc(allocator, 1024 * 1024);
}

fn rejectSymlink(dir: std.fs.Dir, path: []const u8) !void {
    var buffer: [std.fs.max_path_bytes]u8 = undefined;
    if (dir.readLink(path, &buffer)) |_| {
        return error.SymlinkRejected;
    } else |err| switch (err) {
        error.NotLink, error.FileNotFound => return,
        else => |e| return e,
    }
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
        \\  mitos run --provider <name> --task-file <path> [--transport <name>]
        \\  mitos providers
        \\  mitos version
        \\
        \\Commands:
        \\  run        Run a task through a built-in headless transport.
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
        \\  --provider <name>     Provider to run: fake, claudecode, codex, gemini, opencode, pi.
        \\  --task <text>         Task text to pass to the provider.
        \\  --task-file <path>    Read task text from a file.
        \\  --run-id <id>         Artifact directory name under .runs/.
        \\  --workspace <path>    Workspace path used as provider cwd. Defaults to ".".
        \\  --transport <name>    Transport: stdio-json. `--mode` is a legacy alias.
        \\  --env KEY=VALUE       Environment variables to pass to provider (repeatable).
        \\  -h, --help            Show this help.
        \\
        \\Provider CLIs must already be installed and authenticated for real providers.
        \\
    );
}

fn printProviders(writer: anytype) !void {
    try writer.writeAll(
        \\Built-in providers:
        \\  fake    stdio-json  local deterministic test provider
        \\  codex   stdio-json  runs `codex exec --json`
        \\  claudecode stdio-json runs `claude --print --output-format stream-json`
        \\  claude  stdio-json  alias-compatible Claude Code provider
        \\  gemini  stdio-json  runs `gemini --prompt ... --output-format stream-json`
        \\  opencode stdio-json runs `opencode run --format json`
        \\  pi      stdio-json  runs `pi --mode json`
    );
}

const TraceAdapterSink = struct {
    trace: *mitos.trace.file_store.FileTrace,
    allocator: std.mem.Allocator,
    redactor: mitos.redaction.Redactor,

    fn sink(self: *TraceAdapterSink) mitos.adapters.AdapterEventSink {
        return .{ .context = self, .emit = emit };
    }

    fn emit(context: *anyopaque, event: mitos.adapters.AdapterEvent) anyerror!void {
        const self: *TraceAdapterSink = @ptrCast(@alignCast(context));
        switch (event) {
            .structured => |bytes| {
                const redacted_bytes = try redactProviderEventAlloc(
                    self.allocator,
                    self.redactor,
                    bytes,
                );
                defer self.allocator.free(redacted_bytes);

                _ = try self.trace.appendEvent(std.time.milliTimestamp(), .{ .adapter_output = .{
                    .stream = .structured,
                    .bytes = redacted_bytes,
                } });
            },
            else => {},
        }
    }
};

fn redactProviderEventAlloc(
    allocator: std.mem.Allocator,
    redactor: mitos.redaction.Redactor,
    bytes: []const u8,
) ![]u8 {
    return redactor.redactAlloc(allocator, .provider_event, bytes);
}

fn redactStringListAlloc(
    allocator: std.mem.Allocator,
    redactor: mitos.redaction.Redactor,
    values: []const []const u8,
) ![]const []const u8 {
    const redacted = try allocator.alloc([]const u8, values.len);
    var initialized: usize = 0;
    errdefer {
        for (redacted[0..initialized]) |value| {
            allocator.free(value);
        }
        allocator.free(redacted);
    }

    for (values, 0..) |value, index| {
        redacted[index] = try redactProviderEventAlloc(allocator, redactor, value);
        initialized = index + 1;
    }

    return redacted;
}

fn freeStringList(allocator: std.mem.Allocator, values: []const []const u8) void {
    for (values) |value| {
        allocator.free(value);
    }
    allocator.free(values);
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

test "run option parser rejects pty transport" {
    var err: std.io.Writer.Allocating = .init(std.testing.allocator);
    defer err.deinit();

    const args = [_][]const u8{ "--provider", "codex", "--task", "hello", "--transport", "pty" };
    try std.testing.expectError(error.InvalidArguments, parseRunOptions(&args, &err.writer));
}

test "task file reader rejects symlinked task paths" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.makePath("real");
    try tmp.dir.writeFile(.{ .sub_path = "real/task.txt", .data = "hello\n" });

    const bytes = try readTaskFileNoSymlink(allocator, tmp.dir, "real/task.txt");
    defer allocator.free(bytes);
    try std.testing.expectEqualStrings("hello\n", bytes);

    try tmp.dir.symLink("real/task.txt", "task-link.txt", .{});
    try std.testing.expectError(
        error.SymlinkRejected,
        readTaskFileNoSymlink(allocator, tmp.dir, "task-link.txt"),
    );

    try tmp.dir.symLink("real", "dir-link", .{ .is_directory = true });
    try std.testing.expectError(
        error.SymlinkRejected,
        readTaskFileNoSymlink(allocator, tmp.dir, "dir-link/task.txt"),
    );
}

test "run command rejects unsafe trace run ids before writing artifacts" {
    var out: std.io.Writer.Allocating = .init(std.testing.allocator);
    defer out.deinit();
    var err: std.io.Writer.Allocating = .init(std.testing.allocator);
    defer err.deinit();

    const args = [_][]const u8{
        "--provider",
        "fake",
        "--task",
        "hello",
        "--run-id",
        "../escape",
    };

    const code = try runCommand(std.testing.allocator, &args, &out.writer, &err.writer);
    const stderr_text = try err.toOwnedSlice();
    defer std.testing.allocator.free(stderr_text);

    try std.testing.expectEqual(@as(u8, 2), code);
    try std.testing.expect(std.mem.containsAtLeast(u8, stderr_text, 1, "--run-id must be a safe"));
}

test "cli trace persistence redacts task argv provider output and final artifact" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    const run_id = try mitos.runtime.RunId.init("run-cli-redaction");
    const redactor = mitos.redaction.Redactor{};
    const task =
        "call API_KEY=task-secret and https://user:pass@example.test/cb?token=task-token&visible=ok";
    const redacted_task = try redactProviderEventAlloc(allocator, redactor, task);
    defer allocator.free(redacted_task);

    var trace = try mitos.trace.file_store.FileTrace.create(allocator, tmp.dir, run_id, .{
        .created_at_ms = 1,
        .provider = "codex",
        .task = redacted_task,
    });
    defer trace.deinit();

    _ = try trace.appendEvent(1, .{ .run_created = .{
        .task = redacted_task,
        .provider = "codex",
    } });

    const argv = [_][]const u8{ "codex", "exec", "--", task };
    const redacted_argv = try redactStringListAlloc(allocator, redactor, &argv);
    defer freeStringList(allocator, redacted_argv);

    _ = try trace.appendEvent(2, .{ .process_spawned = .{
        .argv = redacted_argv,
        .cwd = ".",
        .pid = null,
    } });

    const stdout_bytes =
        "{\"final_output\":\"token=final-secret ghp_abcdefghijklmnopqrstuvwxyz\"}\n";
    const redacted_stdout = try redactProviderEventAlloc(allocator, redactor, stdout_bytes);
    defer allocator.free(redacted_stdout);

    _ = try trace.appendEvent(3, .{ .adapter_output = .{
        .stream = .stdout,
        .bytes = redacted_stdout,
    } });

    var sink_context = TraceAdapterSink{
        .trace = &trace,
        .allocator = allocator,
        .redactor = redactor,
    };
    var sink = sink_context.sink();
    try sink.emit(sink.context, .{ .structured = "{\"api_key\":\"structured-secret\"}\n" });

    const final_output = "password: final-secret";
    const redacted_final = try redactProviderEventAlloc(allocator, redactor, final_output);
    defer allocator.free(redacted_final);

    _ = try trace.writeArtifact(.final_output, redacted_final);
    _ = try trace.appendEvent(4, .{ .final_output = .{ .text = redacted_final } });

    const manifest = try trace.run_dir.readFileAlloc(
        allocator,
        mitos.trace.defaultArtifactPath(.run_manifest),
        4096,
    );
    defer allocator.free(manifest);
    const events = try trace.run_dir.readFileAlloc(
        allocator,
        mitos.trace.defaultArtifactPath(.events),
        16 * 1024,
    );
    defer allocator.free(events);
    const final_md = try trace.run_dir.readFileAlloc(
        allocator,
        mitos.trace.defaultArtifactPath(.final_output),
        4096,
    );
    defer allocator.free(final_md);

    const persisted = try std.mem.concat(allocator, u8, &.{ manifest, events, final_md });
    defer allocator.free(persisted);

    try std.testing.expect(!std.mem.containsAtLeast(u8, persisted, 1, "task-secret"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, persisted, 1, "user:pass"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, persisted, 1, "task-token"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, persisted, 1, "final-secret"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, persisted, 1, "ghp_abcdefghijklmnopqrstuvwxyz"));
    try std.testing.expect(!std.mem.containsAtLeast(u8, persisted, 1, "structured-secret"));
    try std.testing.expect(std.mem.containsAtLeast(u8, persisted, 1, "[REDACTED]"));
    try std.testing.expect(std.mem.containsAtLeast(u8, persisted, 1, "visible=ok"));
}

test "transport order honors project provider config before built-in defaults" {
    const cfg: mitos.config.ProjectConfig = .{
        .version = mitos.config.supported_version,
        .workspace = .{ .base_branch = "main" },
        .sandbox = .{},
        .providers = &.{
            .{ .name = "codex", .transport_order = &.{ .pty, .stdio_json } },
        },
        .policy = .{},
        .watcher = .{},
    };

    const order = transportOrder("codex", null, configuredProvider(cfg, "codex"));
    try std.testing.expectEqual(TransportName.pty, order[0]);
    try std.testing.expectEqual(TransportName.stdio_json, order[1]);
}
