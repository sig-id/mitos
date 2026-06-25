const std = @import("std");
const builtin = @import("builtin");

const is_windows = builtin.os.tag == .windows;

// ---------------------------------------------------------------------------
// Executable resolution
//
// Ported from open-design's daemon runtime detection. The core idea: a binary
// must be discoverable even when the process inherits a stripped PATH (GUI
// launchers, launchd, .desktop sessions don't read interactive shell rc files).
// We therefore search PATH *plus* the well-known user-level toolchain bin dirs
// (npm/bun/cargo/homebrew prefixes, nvm/fnm/mise version dirs, ...), resolve to
// an actual path rather than a bool, honour a per-agent `*_BIN` override, and
// classify the `--version` probe so callers can tell "not installed" from
// "installed but not invocable".
// ---------------------------------------------------------------------------

pub const NotInvocableCause = enum {
    /// The target exists on PATH but cannot be executed: a non-executable
    /// shim, EACCES, or a wrapper that exited 126. Settings can offer
    /// permission-specific copy instead of treating it as a missing install.
    not_executable,
    /// The spawn resolved to nothing runnable: ENOENT/ENOTDIR or a wrapper
    /// whose interpreter target is missing (exit 127).
    missing_target,
};

pub const ProbeOutcome = union(enum) {
    /// The OS rejected the spawn, or the wrapper script's target failed.
    not_invocable: NotInvocableCause,
    /// The binary itself ran. `version` is the first `--version` line, or
    /// null when the CLI ran but produced no usable version string (an
    /// unsupported `--version` flag, a timeout, or a generic non-zero exit).
    spawned: ?[]u8,

    pub fn deinit(self: ProbeOutcome, allocator: std.mem.Allocator) void {
        switch (self) {
            .spawned => |version| if (version) |v| allocator.free(v),
            .not_invocable => {},
        }
    }
};

/// Declarative inputs for resolving a single agent's executable. Mirrors the
/// `bin` / `fallbackBins` / `*_BIN` override triad from the open-design defs.
pub const ExecutableSpec = struct {
    /// Primary binary name searched on PATH (e.g. "claude").
    bin: []const u8,
    /// Alternate names tried in order when `bin` is not found. Used for forks
    /// that ship under a different binary name but speak the same CLI.
    fallback_bins: []const []const u8 = &.{},
    /// Environment variable that, when set to a valid executable file, wins
    /// over PATH resolution (e.g. "CLAUDE_BIN").
    bin_env_key: ?[]const u8 = null,
};

// ---------------------------------------------------------------------------
// Env / home helpers
// ---------------------------------------------------------------------------

fn envOwnedOptional(allocator: std.mem.Allocator, key: []const u8) !?[]u8 {
    return std.process.getEnvVarOwned(allocator, key) catch |err| switch (err) {
        error.EnvironmentVariableNotFound => null,
        else => err,
    };
}

fn homeDirAlloc(allocator: std.mem.Allocator) !?[]u8 {
    return envOwnedOptional(allocator, if (is_windows) "USERPROFILE" else "HOME");
}

/// Expand a leading `~` against `home`. Returns an owned, trimmed copy, or
/// null for blank input.
fn expandHome(allocator: std.mem.Allocator, raw: []const u8, home: []const u8) !?[]u8 {
    const trimmed = std.mem.trim(u8, raw, " \t\r\n");
    if (trimmed.len == 0) return null;
    if (std.mem.eql(u8, trimmed, "~")) return try allocator.dupe(u8, home);
    if (std.mem.startsWith(u8, trimmed, "~/") or std.mem.startsWith(u8, trimmed, "~\\")) {
        return try std.fs.path.join(allocator, &.{ home, trimmed[2..] });
    }
    return try allocator.dupe(u8, trimmed);
}

// ---------------------------------------------------------------------------
// Well-known user toolchain bin directories
// ---------------------------------------------------------------------------

fn pushJoin(
    allocator: std.mem.Allocator,
    list: *std.ArrayList([]u8),
    parts: []const []const u8,
) !void {
    try list.append(allocator, try std.fs.path.join(allocator, parts));
}

fn pushHomeJoin(
    allocator: std.mem.Allocator,
    list: *std.ArrayList([]u8),
    home: []const u8,
    segments: []const []const u8,
) !void {
    const parts = try allocator.alloc([]const u8, segments.len + 1);
    defer allocator.free(parts);
    parts[0] = home;
    for (segments, 0..) |segment, i| parts[i + 1] = segment;
    try pushJoin(allocator, list, parts);
}

/// Free a directory list produced by `userToolchainBinDirs` / `searchDirs`.
pub fn freeDirList(allocator: std.mem.Allocator, dirs: [][]u8) void {
    for (dirs) |dir| allocator.free(dir);
    allocator.free(dirs);
}

/// The user-level toolchain bin directories searched beyond PATH. Best-effort:
/// fixed candidates are emitted unconditionally (a non-existent dir simply
/// never yields a hit during resolution), while version-manager roots are
/// enumerated and only existing `<version>/bin` dirs are surfaced, newest
/// first. Caller frees with `freeDirList`.
pub fn userToolchainBinDirs(
    allocator: std.mem.Allocator,
    home: []const u8,
    include_system_bins: bool,
) ![][]u8 {
    var list: std.ArrayList([]u8) = .empty;
    errdefer {
        for (list.items) |dir| allocator.free(dir);
        list.deinit(allocator);
    }

    // Vite+ global shims: an explicit VP_HOME is the most specific signal, so
    // it leads.
    if (try envOwnedOptional(allocator, "VP_HOME")) |raw| {
        defer allocator.free(raw);
        if (try expandHome(allocator, raw, home)) |vp_home| {
            defer allocator.free(vp_home);
            try pushHomeJoin(allocator, &list, vp_home, &.{"bin"});
        }
    }

    // The user's explicit npm prefix outranks conventional locations: a binary
    // installed via `npm i -g` today lives at `<prefix>/bin`, while
    // `~/.local/bin` and friends often hold stale installs from an older
    // prefix. This matches npm's own env > .npmrc > default order.
    var npm_prefix = try envOwnedOptional(allocator, "NPM_CONFIG_PREFIX");
    if (npm_prefix == null) npm_prefix = try envOwnedOptional(allocator, "npm_config_prefix");
    if (npm_prefix) |raw| {
        defer allocator.free(raw);
        const trimmed = std.mem.trim(u8, raw, " \t\r\n");
        if (trimmed.len > 0) try pushJoin(allocator, &list, &.{ trimmed, "bin" });
    }

    const home_subdirs = [_][]const []const u8{
        &.{ ".local", "bin" },
        &.{ ".vite-plus", "bin" },
        &.{ ".opencode", "bin" },
        &.{ ".bun", "bin" },
        &.{ ".volta", "bin" },
        &.{ ".asdf", "shims" },
        &.{ "Library", "pnpm" },
        &.{ ".cargo", "bin" },
        // Common sudo-free global npm prefixes from "fix npm EACCES" guides.
        &.{ ".npm-global", "bin" },
        &.{ ".npm-packages", "bin" },
        // Non-Node toolchains that still ship agent CLIs or their deps.
        &.{ ".deno", "bin" },
        &.{ "go", "bin" },
        &.{ ".pyenv", "shims" },
    };
    for (home_subdirs) |segments| {
        try pushHomeJoin(allocator, &list, home, segments);
    }

    if (is_windows) {
        try pushHomeJoin(allocator, &list, home, &.{ "scoop", "shims" });
        if (try envOwnedOptional(allocator, "APPDATA")) |appdata| {
            defer allocator.free(appdata);
            const trimmed = std.mem.trim(u8, appdata, " \t\r\n");
            if (trimmed.len > 0) try pushJoin(allocator, &list, &.{ trimmed, "npm" });
        }
    }

    // Mise: respect MISE_DATA_DIR (the official relocation knob); only fall
    // back to the legacy ~/.mise/shims path when no override is set.
    var mise_override: ?[]u8 = null;
    if (try envOwnedOptional(allocator, "MISE_DATA_DIR")) |raw| {
        defer allocator.free(raw);
        mise_override = try expandHome(allocator, raw, home);
    }
    defer if (mise_override) |dir| allocator.free(dir);

    const mise_data: []const u8 = mise_override orelse
        try std.fs.path.join(allocator, &.{ home, ".local", "share", "mise" });
    const mise_data_owned = mise_override == null;
    defer if (mise_data_owned) allocator.free(mise_data);

    try pushJoin(allocator, &list, &.{ mise_data, "shims" });
    if (mise_override == null) {
        try pushHomeJoin(allocator, &list, home, &.{ ".mise", "shims" });
    }

    if (include_system_bins and !is_windows) {
        try list.append(allocator, try allocator.dupe(u8, "/opt/homebrew/bin"));
        try list.append(allocator, try allocator.dupe(u8, "/usr/local/bin"));
    }

    // Per-version Node toolchains: enumerate each install root and surface the
    // bin dir of every version directory, newest first.
    const mise_installs = try std.fs.path.join(allocator, &.{ mise_data, "installs" });
    defer allocator.free(mise_installs);

    try pushVersionedBinDirs(allocator, &list, &.{ mise_installs, "npm-openai-codex" }, &.{"bin"});
    try pushVersionedBinDirs(allocator, &list, &.{ mise_installs, "node" }, &.{"bin"});
    try pushVersionedBinDirs(allocator, &list, &.{ home, ".nvm", "versions", "node" }, &.{"bin"});
    try pushVersionedBinDirs(allocator, &list, &.{ home, ".local", "share", "fnm", "node-versions" }, &.{ "installation", "bin" });
    try pushVersionedBinDirs(allocator, &list, &.{ home, ".fnm", "node-versions" }, &.{ "installation", "bin" });

    return list.toOwnedSlice(allocator);
}

fn pushVersionedBinDirs(
    allocator: std.mem.Allocator,
    list: *std.ArrayList([]u8),
    root_parts: []const []const u8,
    segments: []const []const u8,
) !void {
    const root = try std.fs.path.join(allocator, root_parts);
    defer allocator.free(root);

    var dir = std.fs.cwd().openDir(root, .{ .iterate = true }) catch return;
    defer dir.close();

    var names: std.ArrayList([]u8) = .empty;
    defer {
        for (names.items) |name| allocator.free(name);
        names.deinit(allocator);
    }

    var it = dir.iterate();
    while (it.next() catch null) |entry| {
        if (entry.kind != .directory and entry.kind != .sym_link) continue;
        try names.append(allocator, try allocator.dupe(u8, entry.name));
    }

    std.sort.block([]u8, names.items, {}, versionDirLessThan);

    for (names.items) |name| {
        const parts = try allocator.alloc([]const u8, segments.len + 2);
        defer allocator.free(parts);
        parts[0] = root;
        parts[1] = name;
        for (segments, 0..) |segment, i| parts[i + 2] = segment;

        const candidate = try std.fs.path.join(allocator, parts);
        std.fs.cwd().access(candidate, .{}) catch {
            allocator.free(candidate);
            continue;
        };
        try list.append(allocator, candidate);
    }
}

fn versionDirLessThan(_: void, a: []const u8, b: []const u8) bool {
    const va = parseVersionLike(a);
    const vb = parseVersionLike(b);
    if (va != null and vb != null) {
        const la = va.?;
        const lb = vb.?;
        for (0..3) |i| {
            if (la[i] != lb[i]) return la[i] > lb[i]; // newest first
        }
        return std.mem.lessThan(u8, a, b);
    }
    if (va != null) return true; // version-like sorts ahead of plain names
    if (vb != null) return false;
    return std.mem.lessThan(u8, a, b);
}

fn parseVersionLike(name: []const u8) ?[3]u64 {
    var rest = name;
    if (rest.len > 0 and rest[0] == 'v') rest = rest[1..];
    var parts: [3]u64 = .{ 0, 0, 0 };
    var it = std.mem.splitScalar(u8, rest, '.');
    var i: usize = 0;
    while (it.next()) |segment| {
        if (i >= 3 or segment.len == 0) return null;
        parts[i] = std.fmt.parseInt(u64, segment, 10) catch return null;
        i += 1;
    }
    if (i != 3) return null;
    return parts;
}

// ---------------------------------------------------------------------------
// Search path assembly + resolution
// ---------------------------------------------------------------------------

fn appendUniqueDir(
    allocator: std.mem.Allocator,
    list: *std.ArrayList([]u8),
    seen: *std.StringHashMap(void),
    dir: []const u8,
) !void {
    if (dir.len == 0) return;
    if (seen.contains(dir)) return;
    const owned = try allocator.dupe(u8, dir);
    try seen.put(owned, {});
    try list.append(allocator, owned);
}

/// The de-duplicated directory list resolution walks: PATH first, then the
/// user toolchain dirs. Caller frees with `freeDirList`.
pub fn searchDirs(allocator: std.mem.Allocator) ![][]u8 {
    var list: std.ArrayList([]u8) = .empty;
    errdefer {
        for (list.items) |dir| allocator.free(dir);
        list.deinit(allocator);
    }

    var seen = std.StringHashMap(void).init(allocator);
    defer seen.deinit();

    if (try envOwnedOptional(allocator, "PATH")) |path_value| {
        defer allocator.free(path_value);
        var it = std.mem.splitScalar(u8, path_value, std.fs.path.delimiter);
        while (it.next()) |dir| try appendUniqueDir(allocator, &list, &seen, dir);
    }

    if (try homeDirAlloc(allocator)) |home| {
        defer allocator.free(home);
        const toolchain = try userToolchainBinDirs(allocator, home, !is_windows);
        defer freeDirList(allocator, toolchain);
        for (toolchain) |dir| try appendUniqueDir(allocator, &list, &seen, dir);
    }

    return list.toOwnedSlice(allocator);
}

fn windowsPathExts(allocator: std.mem.Allocator) ![][]u8 {
    var list: std.ArrayList([]u8) = .empty;
    errdefer {
        for (list.items) |ext| allocator.free(ext);
        list.deinit(allocator);
    }
    const raw = (try envOwnedOptional(allocator, "PATHEXT")) orelse
        try allocator.dupe(u8, ".EXE;.CMD;.BAT");
    defer allocator.free(raw);
    var it = std.mem.splitScalar(u8, raw, ';');
    while (it.next()) |ext| {
        const trimmed = std.mem.trim(u8, ext, " \t\r\n");
        if (trimmed.len == 0) continue;
        try list.append(allocator, try allocator.dupe(u8, trimmed));
    }
    return list.toOwnedSlice(allocator);
}

/// Resolve `bin` to an absolute path by walking `searchDirs`. On Windows each
/// PATHEXT extension is tried. Returns an owned path, or null when not found.
pub fn resolveOnPath(allocator: std.mem.Allocator, bin: []const u8) !?[]u8 {
    if (arg0UsesPath(bin)) return null;

    const dirs = try searchDirs(allocator);
    defer freeDirList(allocator, dirs);

    const exts: ?[][]u8 = if (is_windows) try windowsPathExts(allocator) else null;
    defer if (exts) |list| freeDirList(allocator, list);

    for (dirs) |dir| {
        if (exts) |ext_list| {
            for (ext_list) |ext| {
                const name = try std.fmt.allocPrint(allocator, "{s}{s}", .{ bin, ext });
                defer allocator.free(name);
                const full = try std.fs.path.join(allocator, &.{ dir, name });
                if (!executableFileAvailable(full)) {
                    allocator.free(full);
                    continue;
                }
                return full;
            }
        } else {
            const full = try std.fs.path.join(allocator, &.{ dir, bin });
            if (!executableFileAvailable(full)) {
                allocator.free(full);
                continue;
            }
            return full;
        }
    }
    return null;
}

pub fn resolveCommandArg0(allocator: std.mem.Allocator, arg0: []const u8) !?[]u8 {
    if (arg0.len == 0 or arg0UsesPath(arg0)) return null;

    const dirs = try searchDirs(allocator);
    defer freeDirList(allocator, dirs);
    return try resolveArg0FromDirs(allocator, arg0, dirs);
}

fn resolveArg0FromDirs(
    allocator: std.mem.Allocator,
    arg0: []const u8,
    dirs: []const []const u8,
) !?[]u8 {
    if (arg0.len == 0 or arg0UsesPath(arg0)) return null;

    for (dirs) |dir| {
        const full = try std.fs.path.join(allocator, &.{ dir, arg0 });
        if (!executableFileAvailable(full)) {
            allocator.free(full);
            continue;
        }
        return full;
    }

    return null;
}

fn arg0UsesPath(arg0: []const u8) bool {
    return std.mem.indexOfScalar(u8, arg0, '/') != null or
        (is_windows and std.mem.indexOfScalar(u8, arg0, '\\') != null);
}

fn looksExecutableOnWindows(file_path: []const u8) bool {
    const ext = std.fs.path.extension(file_path);
    if (ext.len == 0) return false;
    const executable = [_][]const u8{ ".EXE", ".CMD", ".BAT", ".COM" };
    for (executable) |candidate| {
        if (std.ascii.eqlIgnoreCase(ext, candidate)) return true;
    }
    return false;
}

pub fn executableFileAvailable(file_path: []const u8) bool {
    const stat = std.fs.cwd().statFile(file_path) catch return false;
    if (stat.kind != .file) return false;

    if (is_windows) {
        return looksExecutableOnWindows(file_path);
    }

    std.posix.access(file_path, std.posix.X_OK) catch return false;
    return true;
}

/// Validate a `*_BIN`-style override: it must expand to an absolute path that
/// is a regular, executable file. Returns the owned path, or null when unusable
/// (so callers fall through to PATH resolution).
fn executableFilePath(allocator: std.mem.Allocator, raw: []const u8, home: []const u8) !?[]u8 {
    const expanded = (try expandHome(allocator, raw, home)) orelse return null;
    errdefer allocator.free(expanded);

    if (!std.fs.path.isAbsolute(expanded)) {
        allocator.free(expanded);
        return null;
    }
    if (!executableFileAvailable(expanded)) {
        allocator.free(expanded);
        return null;
    }
    return expanded;
}

/// Resolve an agent's executable: a valid `*_BIN` override wins, otherwise the
/// primary `bin` then each fallback name is tried on the extended search path.
/// Returns the owned selected path, or null when nothing resolves.
pub fn resolveExecutable(allocator: std.mem.Allocator, spec: ExecutableSpec) !?[]u8 {
    if (spec.bin_env_key) |key| {
        if (try envOwnedOptional(allocator, key)) |raw| {
            defer allocator.free(raw);
            const home = (try homeDirAlloc(allocator)) orelse try allocator.dupe(u8, "");
            defer allocator.free(home);
            if (try executableFilePath(allocator, raw, home)) |path| return path;
        }
    }

    if (try resolveOnPath(allocator, spec.bin)) |path| return path;
    for (spec.fallback_bins) |fallback| {
        if (try resolveOnPath(allocator, fallback)) |path| return path;
    }
    return null;
}

// ---------------------------------------------------------------------------
// Version probe
// ---------------------------------------------------------------------------

fn firstLine(text: []const u8) []const u8 {
    const trimmed = std.mem.trim(u8, text, " \t\r\n");
    const newline = std.mem.indexOfScalar(u8, trimmed, '\n') orelse return trimmed;
    return std.mem.trim(u8, trimmed[0..newline], " \t\r\n");
}

/// Run `path` with `version_args` and classify the result. Distinguishes a
/// genuinely not-invocable target (OS rejection, or exit 126/127 from a broken
/// wrapper) from a CLI that ran but yielded no version string. Caller frees the
/// outcome with `ProbeOutcome.deinit`.
pub fn probeVersion(
    allocator: std.mem.Allocator,
    path: []const u8,
    version_args: []const []const u8,
) !ProbeOutcome {
    const argv = try allocator.alloc([]const u8, version_args.len + 1);
    defer allocator.free(argv);
    argv[0] = path;
    for (version_args, 0..) |arg, i| argv[i + 1] = arg;

    const result = std.process.Child.run(.{
        .allocator = allocator,
        .argv = argv,
        .max_output_bytes = 64 * 1024,
    }) catch |err| switch (err) {
        error.FileNotFound => return .{ .not_invocable = .missing_target },
        error.AccessDenied => return .{ .not_invocable = .not_executable },
        error.OutOfMemory => return error.OutOfMemory,
        // Timeouts, broken pipes, and other spawn-time noise mean the binary
        // is present enough that we don't want to advertise it as missing.
        else => return .{ .spawned = null },
    };
    defer allocator.free(result.stdout);
    defer allocator.free(result.stderr);

    switch (result.term) {
        .Exited => |code| {
            if (code == 126) return .{ .not_invocable = .not_executable };
            if (code == 127) return .{ .not_invocable = .missing_target };
            if (code == 0) {
                const line = firstLine(result.stdout);
                if (line.len == 0) return .{ .spawned = null };
                return .{ .spawned = try allocator.dupe(u8, line) };
            }
            return .{ .spawned = null };
        },
        else => return .{ .spawned = null },
    }
}

// ---------------------------------------------------------------------------
// Existing helpers (unchanged public API)
// ---------------------------------------------------------------------------

/// Whether `name` resolves on the extended search path. Retained for callers
/// that only need a yes/no; backed by `resolveOnPath` so it now sees the user
/// toolchain dirs too, not just PATH.
pub fn executableAvailable(allocator: std.mem.Allocator, name: []const u8) !bool {
    const resolved = (try resolveOnPath(allocator, name)) orelse return false;
    allocator.free(resolved);
    return true;
}

pub fn commandOutputContains(
    allocator: std.mem.Allocator,
    argv: []const []const u8,
    needle: []const u8,
) !bool {
    const output = commandOutputAlloc(allocator, argv) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return false,
    };
    defer allocator.free(output);

    return std.mem.indexOf(u8, output, needle) != null;
}

pub fn commandOutputAlloc(
    allocator: std.mem.Allocator,
    argv: []const []const u8,
) ![]u8 {
    var owned_arg0: ?[]u8 = null;
    defer if (owned_arg0) |arg0| allocator.free(arg0);

    var owned_argv: ?[][]const u8 = null;
    defer if (owned_argv) |copy| allocator.free(copy);

    const run_argv = if (argv.len > 0) argv: {
        owned_arg0 = try resolveCommandArg0(allocator, argv[0]);
        if (owned_arg0) |arg0| {
            const copy = try allocator.alloc([]const u8, argv.len);
            @memcpy(copy, argv);
            copy[0] = arg0;
            owned_argv = copy;
            break :argv copy;
        }
        break :argv argv;
    } else argv;

    const result = try std.process.Child.run(.{
        .allocator = allocator,
        .argv = run_argv,
        .max_output_bytes = 64 * 1024,
    });
    defer allocator.free(result.stdout);
    defer allocator.free(result.stderr);

    return try std.mem.concat(allocator, u8, &.{ result.stdout, result.stderr });
}

pub fn commandOutputTrimmedAlloc(
    allocator: std.mem.Allocator,
    argv: []const []const u8,
) ![]u8 {
    const output = try commandOutputAlloc(allocator, argv);
    defer allocator.free(output);
    return try allocator.dupe(u8, std.mem.trim(u8, output, " \t\r\n"));
}

pub fn authStatusFromEnv(
    allocator: std.mem.Allocator,
    keys: []const []const u8,
) !@import("types.zig").AuthStatus {
    for (keys) |key| {
        if (try std.process.hasEnvVar(allocator, key)) return .authenticated;
    }
    return .unknown;
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

test "missing executable is reported unavailable" {
    try std.testing.expect(!try executableAvailable(
        std.testing.allocator,
        "mitos-definitely-not-present",
    ));
}

test "command output detection scans stdout" {
    try std.testing.expect(try commandOutputContains(
        std.testing.allocator,
        &.{ "sh", "-c", "printf feature-json" },
        "feature-json",
    ));
}

test "command output detection treats missing commands as unsupported" {
    try std.testing.expect(!try commandOutputContains(
        std.testing.allocator,
        &.{"mitos-definitely-missing-command-output-helper"},
        "feature-json",
    ));
}

test "command output detection treats non-executable targets as unsupported" {
    if (is_windows) return;

    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    var file = try tmp.dir.createFile("not-executable", .{});
    try file.writeAll("#!/bin/sh\nprintf feature-json\n");
    file.close();

    const base = try tmpDirPath(allocator, tmp.sub_path[0..]);
    defer allocator.free(base);
    const target = try std.fs.path.join(allocator, &.{ base, "not-executable" });
    defer allocator.free(target);

    try std.testing.expect(!try commandOutputContains(
        allocator,
        &.{target},
        "feature-json",
    ));
}

test "command output helper returns trimmed output" {
    const output = try commandOutputTrimmedAlloc(
        std.testing.allocator,
        &.{ "sh", "-c", "printf '  version 1  \\n'" },
    );
    defer std.testing.allocator.free(output);

    try std.testing.expectEqualStrings("version 1", output);
}

test "command arg resolver finds binaries in a supplied search dir" {
    if (is_windows) return;

    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.makeDir("bin");
    var file = try tmp.dir.createFile("bin/mitos-detect-tool", .{});
    try file.writeAll(
        \\#!/bin/sh
        \\printf 'detect-ok'
        \\
    );
    try file.chmod(0o755);
    file.close();

    const base = try tmpDirPath(allocator, tmp.sub_path[0..]);
    defer allocator.free(base);
    const bin_dir = try std.fs.path.join(allocator, &.{ base, "bin" });
    defer allocator.free(bin_dir);

    const dirs = [_][]const u8{bin_dir};
    const resolved = try resolveArg0FromDirs(allocator, "mitos-detect-tool", &dirs);
    defer if (resolved) |path| allocator.free(path);

    try std.testing.expect(resolved != null);
    try std.testing.expect(std.mem.endsWith(u8, resolved.?, "mitos-detect-tool"));
}

test "command arg resolver skips non-executable shadows" {
    if (is_windows) return;

    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.makePath("bad");
    try tmp.dir.makePath("good");
    var bad_file = try tmp.dir.createFile("bad/mitos-detect-tool", .{});
    try bad_file.writeAll("#!/bin/sh\nprintf bad\n");
    bad_file.close();

    var good_file = try tmp.dir.createFile("good/mitos-detect-tool", .{});
    try good_file.writeAll("#!/bin/sh\nprintf good\n");
    try good_file.chmod(0o755);
    good_file.close();

    const base = try tmpDirPath(allocator, tmp.sub_path[0..]);
    defer allocator.free(base);
    const bad_dir = try std.fs.path.join(allocator, &.{ base, "bad" });
    defer allocator.free(bad_dir);
    const good_dir = try std.fs.path.join(allocator, &.{ base, "good" });
    defer allocator.free(good_dir);

    const dirs = [_][]const u8{ bad_dir, good_dir };
    const resolved = try resolveArg0FromDirs(allocator, "mitos-detect-tool", &dirs);
    defer if (resolved) |path| allocator.free(path);

    try std.testing.expect(resolved != null);
    try std.testing.expect(std.mem.indexOf(u8, resolved.?, "/good/") != null);
}

test "command arg resolver skips directory shadows" {
    if (is_windows) return;

    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();

    try tmp.dir.makePath("bad/mitos-detect-tool");
    try tmp.dir.makePath("good");
    var good_file = try tmp.dir.createFile("good/mitos-detect-tool", .{});
    try good_file.writeAll("#!/bin/sh\nprintf good\n");
    try good_file.chmod(0o755);
    good_file.close();

    const base = try tmpDirPath(allocator, tmp.sub_path[0..]);
    defer allocator.free(base);
    const bad_dir = try std.fs.path.join(allocator, &.{ base, "bad" });
    defer allocator.free(bad_dir);
    const good_dir = try std.fs.path.join(allocator, &.{ base, "good" });
    defer allocator.free(good_dir);

    const dirs = [_][]const u8{ bad_dir, good_dir };
    const resolved = try resolveArg0FromDirs(allocator, "mitos-detect-tool", &dirs);
    defer if (resolved) |path| allocator.free(path);

    try std.testing.expect(resolved != null);
    try std.testing.expect(std.mem.indexOf(u8, resolved.?, "/good/") != null);
}

test "command arg resolver leaves path-like argv zero untouched" {
    const dirs = [_][]const u8{"/bin"};
    try std.testing.expect(try resolveArg0FromDirs(std.testing.allocator, "./sh", &dirs) == null);
}

test "auth status helper leaves missing env-based auth unknown" {
    const status = try authStatusFromEnv(
        std.testing.allocator,
        &.{"MITOS_DEFINITELY_MISSING_PROVIDER_KEY"},
    );

    try std.testing.expectEqual(@import("types.zig").AuthStatus.unknown, status);
}

test "version-like dir names parse and the rest are rejected" {
    try std.testing.expect(parseVersionLike("not-a-version") == null);
    try std.testing.expect(parseVersionLike("1.2") == null);
    try std.testing.expect(parseVersionLike("1.2.3.4") == null);

    const parsed = parseVersionLike("v20.11.0") orelse return error.TestUnexpectedNull;
    try std.testing.expectEqual(@as(u64, 20), parsed[0]);
    try std.testing.expectEqual(@as(u64, 11), parsed[1]);
    try std.testing.expectEqual(@as(u64, 0), parsed[2]);
}

test "versioned dirs sort newest first, plain names trail" {
    var names = [_][]const u8{ "v18.0.0", "extra", "v20.11.0", "v20.9.1" };
    std.sort.block([]const u8, &names, {}, versionDirLessThan);

    try std.testing.expectEqualStrings("v20.11.0", names[0]);
    try std.testing.expectEqualStrings("v20.9.1", names[1]);
    try std.testing.expectEqualStrings("v18.0.0", names[2]);
    try std.testing.expectEqualStrings("extra", names[3]);
}

test "user toolchain dirs include conventional bins for a synthetic home" {
    const dirs = try userToolchainBinDirs(std.testing.allocator, "/tmp/mitos-fake-home", false);
    defer freeDirList(std.testing.allocator, dirs);

    var saw_local_bin = false;
    var saw_cargo_bin = false;
    for (dirs) |dir| {
        if (std.mem.endsWith(u8, dir, ".local/bin")) saw_local_bin = true;
        if (std.mem.endsWith(u8, dir, ".cargo/bin")) saw_cargo_bin = true;
        try std.testing.expect(std.mem.startsWith(u8, dir, "/tmp/mitos-fake-home"));
    }
    try std.testing.expect(saw_local_bin);
    try std.testing.expect(saw_cargo_bin);
}

test "version probe classifies an absent target as not invocable" {
    const outcome = try probeVersion(
        std.testing.allocator,
        "/tmp/mitos-definitely-missing-binary-xyz",
        &.{"--version"},
    );
    defer outcome.deinit(std.testing.allocator);

    try std.testing.expectEqual(NotInvocableCause.missing_target, outcome.not_invocable);
}

test "version probe reads the first line of a spawned binary" {
    if (is_windows) return;
    const outcome = try probeVersion(
        std.testing.allocator,
        "/bin/sh",
        &.{ "-c", "printf 'mitos 1.2.3\\nignored second line\\n'" },
    );
    defer outcome.deinit(std.testing.allocator);

    switch (outcome) {
        .spawned => |version| {
            try std.testing.expect(version != null);
            try std.testing.expectEqualStrings("mitos 1.2.3", version.?);
        },
        .not_invocable => return error.TestUnexpectedResult,
    }
}

fn tmpDirPath(allocator: std.mem.Allocator, sub_path: []const u8) ![]u8 {
    const cwd = try std.fs.cwd().realpathAlloc(allocator, ".");
    defer allocator.free(cwd);
    return try std.fs.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", sub_path });
}
