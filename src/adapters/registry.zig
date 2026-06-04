const std = @import("std");
const types = @import("types.zig");
const transport = @import("../transport/root.zig");

pub const SelectionError = error{
    ProviderNotRegistered,
    AdapterUnavailable,
};

pub const AdapterSelection = struct {
    adapter: *types.AgentAdapter,
    transport_name: transport.TransportName,
    detection: ?types.AdapterDetection = null,

    pub fn deinit(self: AdapterSelection, allocator: std.mem.Allocator) void {
        if (self.detection) |detection| detection.deinit(allocator);
    }
};

pub const AdapterRegistry = struct {
    adapters: []const *types.AgentAdapter,

    pub fn init(adapters: []const *types.AgentAdapter) AdapterRegistry {
        return .{ .adapters = adapters };
    }

    pub fn select(
        self: AdapterRegistry,
        allocator: std.mem.Allocator,
        provider: types.ProviderId,
        priority_order: []const transport.TransportName,
    ) anyerror!AdapterSelection {
        var saw_provider = false;

        for (priority_order) |transport_name| {
            for (self.adapters) |adapter| {
                if (!providerEql(adapter.provider, provider)) continue;
                saw_provider = true;

                if (!adapter.capabilities.supportsTransport(transport_name)) continue;

                const detected = if (adapter.detect) |detect_fn|
                    try detect_fn(adapter, allocator)
                else
                    null;

                if (detected) |detection| {
                    if (!detection.supportsTransport(transport_name)) {
                        detection.deinit(allocator);
                        continue;
                    }
                }

                return .{
                    .adapter = adapter,
                    .transport_name = transport_name,
                    .detection = detected,
                };
            }
        }

        if (saw_provider) return SelectionError.AdapterUnavailable;
        return SelectionError.ProviderNotRegistered;
    }

    pub fn selectNoDetect(
        self: AdapterRegistry,
        provider: types.ProviderId,
        priority_order: []const transport.TransportName,
    ) anyerror!AdapterSelection {
        var saw_provider = false;

        for (priority_order) |transport_name| {
            for (self.adapters) |adapter| {
                if (!providerEql(adapter.provider, provider)) continue;
                saw_provider = true;

                if (!adapter.capabilities.supportsTransport(transport_name)) continue;
                return .{
                    .adapter = adapter,
                    .transport_name = transport_name,
                };
            }
        }

        if (saw_provider) return SelectionError.AdapterUnavailable;
        return SelectionError.ProviderNotRegistered;
    }

    pub fn selectByName(
        self: AdapterRegistry,
        allocator: std.mem.Allocator,
        provider_name: []const u8,
        priority_order: []const transport.TransportName,
    ) anyerror!AdapterSelection {
        return try self.select(
            allocator,
            try types.ProviderId.init(provider_name),
            priority_order,
        );
    }

    pub fn selectByNameNoDetect(
        self: AdapterRegistry,
        provider_name: []const u8,
        priority_order: []const transport.TransportName,
    ) anyerror!AdapterSelection {
        return try self.selectNoDetect(
            try types.ProviderId.init(provider_name),
            priority_order,
        );
    }
};

fn providerEql(a: types.ProviderId, b: types.ProviderId) bool {
    return std.mem.eql(u8, a.value, b.value);
}

fn dummyStart(
    self: *types.AgentAdapter,
    context: types.AdapterContext,
    _: types.Task,
) anyerror!types.AdapterRun {
    return .{
        .run_id = context.run_id,
        .adapter_id = self.id,
        .transport_name = .stdio_json,
        .status = .completed,
        .started_at_ms = context.started_at_ms,
    };
}

test "adapter selection follows configured priority order" {
    const provider = try types.ProviderId.init("fake");

    var headless = types.AgentAdapter{
        .id = try types.AdapterId.init("fake.headless"),
        .provider = provider,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
        },
        .start = dummyStart,
    };
    var pty = types.AgentAdapter{
        .id = try types.AdapterId.init("fake.pty"),
        .provider = provider,
        .capabilities = .{
            .supports_interactive_pty = true,
        },
        .start = dummyStart,
    };

    const adapters = [_]*types.AgentAdapter{ &headless, &pty };
    const registry = AdapterRegistry.init(&adapters);
    const selection = try registry.select(
        std.testing.allocator,
        provider,
        &.{ .pty, .stdio_json },
    );
    defer selection.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("fake.pty", selection.adapter.id.value);
    try std.testing.expectEqual(transport.TransportName.pty, selection.transport_name);
}

test "adapter selection skips unavailable detections" {
    const provider = try types.ProviderId.init("fake");

    var unavailable = types.AgentAdapter{
        .id = try types.AdapterId.init("fake.unavailable"),
        .provider = provider,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
        },
        .detect = unavailableDetect,
        .start = dummyStart,
    };
    var fallback = types.AgentAdapter{
        .id = try types.AdapterId.init("fake.fallback"),
        .provider = provider,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
        },
        .start = dummyStart,
    };

    const adapters = [_]*types.AgentAdapter{ &unavailable, &fallback };
    const registry = AdapterRegistry.init(&adapters);
    const selection = try registry.select(
        std.testing.allocator,
        provider,
        &.{.stdio_json},
    );
    defer selection.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("fake.fallback", selection.adapter.id.value);
}

test "adapter selection can bypass host detection for sandbox execution" {
    const provider = try types.ProviderId.init("fake");

    var unavailable = types.AgentAdapter{
        .id = try types.AdapterId.init("fake.unavailable"),
        .provider = provider,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
        },
        .detect = unavailableDetect,
        .start = dummyStart,
    };

    const adapters = [_]*types.AgentAdapter{&unavailable};
    const registry = AdapterRegistry.init(&adapters);
    const selection = try registry.selectByNameNoDetect("fake", &.{.stdio_json});
    defer selection.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("fake.unavailable", selection.adapter.id.value);
    try std.testing.expect(selection.detection == null);
}

test "adapter selection accepts provider names from config" {
    const provider = try types.ProviderId.init("fake");
    var adapter = types.AgentAdapter{
        .id = try types.AdapterId.init("fake.headless"),
        .provider = provider,
        .capabilities = .{
            .supports_headless = true,
            .supports_json_output = true,
        },
        .start = dummyStart,
    };

    const adapters = [_]*types.AgentAdapter{&adapter};
    const registry = AdapterRegistry.init(&adapters);
    const selection = try registry.selectByName(
        std.testing.allocator,
        "fake",
        &.{.stdio_json},
    );
    defer selection.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("fake.headless", selection.adapter.id.value);
}

fn unavailableDetect(
    _: *types.AgentAdapter,
    _: std.mem.Allocator,
) anyerror!types.AdapterDetection {
    return .{ .available = false };
}
