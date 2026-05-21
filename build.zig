const std = @import("std");

pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{});
    const ghostty_commit = "46d54ed673a004df09078bee56e809421a82370e";
    const ghostty_source = b.option(
        []const u8,
        "ghostty-source",
        "Path to the pinned Ghostty/libghostty-vt source tree",
    ) orelse "third_party/ghostty";
    const with_ghostty_lib = b.option(
        bool,
        "with-ghostty-lib",
        "Allow steps that build libghostty-vt from -Dghostty-source",
    ) orelse false;

    const ghostty_options = b.addOptions();
    ghostty_options.addOption([]const u8, "source_path", ghostty_source);
    ghostty_options.addOption([]const u8, "expected_commit", ghostty_commit);

    const mod = b.addModule("mitos", .{
        .root_source_file = b.path("src/root.zig"),
        .target = target,
    });
    mod.addOptions("terminal_ghostty_config", ghostty_options);

    const exe = b.addExecutable(.{
        .name = "mitos",
        .root_module = b.createModule(.{
            .root_source_file = b.path("src/main.zig"),
            .target = target,
            .optimize = optimize,
            .imports = &.{
                .{ .name = "mitos", .module = mod },
            },
        }),
    });

    b.installArtifact(exe);

    const run_cmd = b.addRunArtifact(exe);
    run_cmd.step.dependOn(b.getInstallStep());
    if (b.args) |args| {
        run_cmd.addArgs(args);
    }

    const run_step = b.step("run", "Run the mitos CLI");
    run_step.dependOn(&run_cmd.step);

    const ghostty_check_exe = b.addExecutable(.{
        .name = "check-ghostty",
        .root_module = b.createModule(.{
            .root_source_file = b.path("tools/check_ghostty.zig"),
            .target = b.graph.host,
            .optimize = .Debug,
        }),
    });
    const run_ghostty_check = b.addRunArtifact(ghostty_check_exe);
    run_ghostty_check.addArg(ghostty_source);
    run_ghostty_check.addArg(ghostty_commit);

    const ghostty_check_step = b.step(
        "ghostty-check",
        "Validate the pinned Ghostty/libghostty-vt source tree",
    );
    ghostty_check_step.dependOn(&run_ghostty_check.step);

    const ghostty_lib_step = b.step(
        "ghostty-lib",
        "Build libghostty-vt from -Dghostty-source",
    );
    const build_ghostty = b.addSystemCommand(&.{
        b.graph.zig_exe,
        "build",
        "-Demit-lib-vt=true",
        "-Demit-exe=false",
        "-Di18n=false",
        "-Dsimd=false",
    });
    build_ghostty.setCwd(.{ .cwd_relative = ghostty_source });
    build_ghostty.step.dependOn(&run_ghostty_check.step);
    if (b.graph.global_cache_root.path) |global_cache_dir| {
        build_ghostty.setEnvironmentVariable("ZIG_GLOBAL_CACHE_DIR", global_cache_dir);
    }
    if (with_ghostty_lib) {
        ghostty_lib_step.dependOn(&build_ghostty.step);
    } else {
        const enable = b.addFail(
            "ghostty-lib requires -Dwith-ghostty-lib=true so normal builds do not build external source trees",
        );
        ghostty_lib_step.dependOn(&enable.step);
    }

    const terminal_native_tests = b.addTest(.{
        .root_module = b.createModule(.{
            .root_source_file = b.path("src/terminal/ghostty.zig"),
            .target = target,
            .optimize = optimize,
            .link_libc = true,
        }),
        .filters = &.{"native libghostty"},
    });
    const run_terminal_native_tests = b.addRunArtifact(terminal_native_tests);
    run_terminal_native_tests.setEnvironmentVariable(
        "MITOS_LIBGHOSTTY_VT",
        b.pathJoin(&.{ ghostty_source, "zig-out", "lib", "libghostty-vt.so" }),
    );

    const terminal_native_step = b.step(
        "terminal-native-test",
        "Build libghostty-vt and run the native terminal adapter test",
    );
    if (with_ghostty_lib) {
        run_terminal_native_tests.step.dependOn(&build_ghostty.step);
        terminal_native_step.dependOn(&run_terminal_native_tests.step);
    } else {
        const enable = b.addFail(
            "terminal-native-test requires -Dwith-ghostty-lib=true so normal tests do not build external source trees",
        );
        terminal_native_step.dependOn(&enable.step);
    }

    const release_exe = b.addExecutable(.{
        .name = "mitos",
        .root_module = b.createModule(.{
            .root_source_file = b.path("src/main.zig"),
            .target = target,
            .optimize = .ReleaseSafe,
            .imports = &.{
                .{ .name = "mitos", .module = mod },
            },
        }),
    });
    const install_release = b.addInstallArtifact(release_exe, .{
        .dest_dir = .{ .override = .{ .custom = "release/bin" } },
    });
    const release_step = b.step("release", "Build the release binary");
    release_step.dependOn(&install_release.step);

    const mod_tests = b.addTest(.{
        .root_module = mod,
    });
    const run_mod_tests = b.addRunArtifact(mod_tests);

    const exe_tests = b.addTest(.{
        .root_module = exe.root_module,
    });
    const run_exe_tests = b.addRunArtifact(exe_tests);

    const test_step = b.step("test", "Run tests");
    test_step.dependOn(&run_mod_tests.step);
    test_step.dependOn(&run_exe_tests.step);
}
