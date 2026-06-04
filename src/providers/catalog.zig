const std = @import("std");
const adapters = @import("../adapters/root.zig");
const claude = @import("claude.zig");
const config = @import("../config/types.zig");
const codex = @import("codex.zig");
const fake = @import("fake.zig");
const gemini = @import("gemini.zig");
const transport = @import("../transport/root.zig");

pub const DefaultAdapters = struct {
    fake_headless: adapters.AgentAdapter = undefined,
    codex_exec: adapters.AgentAdapter = undefined,
    claude_print: adapters.AgentAdapter = undefined,
    gemini_headless: adapters.AgentAdapter = undefined,
    adapter_ptrs: [4]*adapters.AgentAdapter = undefined,

    pub fn init(self: *DefaultAdapters) void {
        self.fake_headless = fake.initHeadlessAdapter();
        self.codex_exec = codex.initExecAdapter();
        self.claude_print = claude.initPrintAdapter();
        self.gemini_headless = gemini.initHeadlessAdapter();
        self.adapter_ptrs = .{
            &self.fake_headless,
            &self.codex_exec,
            &self.claude_print,
            &self.gemini_headless,
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
