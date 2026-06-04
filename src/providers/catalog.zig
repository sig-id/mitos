const std = @import("std");
const adapters = @import("../adapters/root.zig");
const claude = @import("claude.zig");
const config = @import("../config/types.zig");
const codex = @import("codex.zig");
const fake = @import("fake.zig");
const gemini = @import("gemini.zig");
const pty = @import("pty.zig");
const transport = @import("../transport/root.zig");

pub const DefaultAdapters = struct {
    fake_headless: adapters.AgentAdapter = undefined,
    codex_exec: adapters.AgentAdapter = undefined,
    codex_pty: adapters.AgentAdapter = undefined,
    claude_print: adapters.AgentAdapter = undefined,
    claude_pty: adapters.AgentAdapter = undefined,
    gemini_headless: adapters.AgentAdapter = undefined,
    generic_pty: adapters.AgentAdapter = undefined,
    adapter_ptrs: [7]*adapters.AgentAdapter = undefined,

    pub fn init(self: *DefaultAdapters) void {
        self.fake_headless = fake.initHeadlessAdapter();
        self.codex_exec = codex.initExecAdapter();
        self.codex_pty = codex.initPtyAdapter();
        self.claude_print = claude.initPrintAdapter();
        self.claude_pty = claude.initPtyAdapter();
        self.gemini_headless = gemini.initHeadlessAdapter();
        self.generic_pty = pty.initAdapter();
        self.adapter_ptrs = .{
            &self.fake_headless,
            &self.codex_exec,
            &self.codex_pty,
            &self.claude_print,
            &self.claude_pty,
            &self.gemini_headless,
            &self.generic_pty,
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

test "default adapter catalog registers fake codex and pty adapters" {
    var catalog: DefaultAdapters = undefined;
    catalog.init();
    const registry = catalog.registry();

    const fake_selection = try registry.select(
        std.testing.allocator,
        .{ .value = "fake" },
        &.{.stdio_json},
    );
    defer fake_selection.deinit(std.testing.allocator);
    const pty_selection = try registry.select(
        std.testing.allocator,
        .{ .value = "generic-pty" },
        &.{.pty},
    );
    defer pty_selection.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("fake.headless", fake_selection.adapter.id.value);
    try std.testing.expectEqualStrings("generic-pty.terminal", pty_selection.adapter.id.value);
}

test "default adapter catalog selects from parsed provider config" {
    var catalog: DefaultAdapters = undefined;
    catalog.init();

    const provider: config.ProviderConfig = .{
        .name = "codex",
        .transport_order = &.{ .pty, .stdio_json },
    };
    const selection = try catalog.selectConfigured(std.testing.allocator, provider);
    defer selection.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("codex.pty", selection.adapter.id.value);
    try std.testing.expectEqual(transport.TransportName.pty, selection.transport_name);
}
