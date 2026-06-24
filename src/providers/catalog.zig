const std = @import("std");
const adapters = @import("../adapters/root.zig");
const claude = @import("claude.zig");
const config = @import("../config/types.zig");
const codex = @import("codex.zig");
const fake = @import("fake.zig");
const gemini = @import("gemini.zig");
const opencode = @import("opencode.zig");
const pi = @import("pi.zig");
const transport = @import("../transport/root.zig");

pub const DefaultAdapters = struct {
    fake_headless: adapters.AgentAdapter = undefined,
    codex_exec: adapters.AgentAdapter = undefined,
    claude_print: adapters.AgentAdapter = undefined,
    claudecode_print: adapters.AgentAdapter = undefined,
    gemini_headless: adapters.AgentAdapter = undefined,
    opencode_run: adapters.AgentAdapter = undefined,
    pi_json: adapters.AgentAdapter = undefined,
    adapter_ptrs: [7]*adapters.AgentAdapter = undefined,

    pub fn init(self: *DefaultAdapters) void {
        self.fake_headless = fake.initHeadlessAdapter();
        self.codex_exec = codex.initExecAdapter();
        self.claude_print = claude.initPrintAdapter();
        self.claudecode_print = claude.initClaudeCodePrintAdapter();
        self.gemini_headless = gemini.initHeadlessAdapter();
        self.opencode_run = opencode.initRunAdapter();
        self.pi_json = pi.initJsonAdapter();
        self.adapter_ptrs = .{
            &self.fake_headless,
            &self.codex_exec,
            &self.claude_print,
            &self.claudecode_print,
            &self.gemini_headless,
            &self.opencode_run,
            &self.pi_json,
        };
    }

    pub fn registry(self: *DefaultAdapters) adapters.registry.AdapterRegistry {
        return adapters.registry.AdapterRegistry.init(&self.adapter_ptrs);
    }

    pub fn selectConfigured(
        self: *DefaultAdapters,
        allocator: std.mem.Allocator,
        provider_config: config.ProviderConfig,
    ) anyerror!adapters.registry.AdapterSelection {
        return try self.registry().selectByName(
            allocator,
            provider_config.name,
            provider_config.transport_order,
        );
    }
};

test "default adapter catalog registers headless adapters" {
    var catalog: DefaultAdapters = undefined;
    catalog.init();
    const registry = catalog.registry();

    const fake_selection = try registry.select(
        std.testing.allocator,
        .{ .value = "fake" },
        &.{.stdio_json},
    );
    defer fake_selection.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("fake.headless", fake_selection.adapter.id.value);

    const opencode_selection = try registry.selectNoDetect(
        .{ .value = "opencode" },
        &.{.stdio_json},
    );
    defer opencode_selection.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("opencode.run", opencode_selection.adapter.id.value);

    const pi_selection = try registry.selectNoDetect(
        .{ .value = "pi" },
        &.{.stdio_json},
    );
    defer pi_selection.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("pi.json", pi_selection.adapter.id.value);
}

test "default adapter catalog leaves pty to mitos-term and falls back to headless" {
    var catalog: DefaultAdapters = undefined;
    catalog.init();

    const provider: config.ProviderConfig = .{
        .name = "codex",
        .transport_order = &.{ .pty, .stdio_json },
    };
    const selection = try catalog.selectConfigured(std.testing.allocator, provider);
    defer selection.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("codex.exec", selection.adapter.id.value);
    try std.testing.expectEqual(transport.TransportName.stdio_json, selection.transport_name);
}
