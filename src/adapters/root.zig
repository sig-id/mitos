const std = @import("std");
const types = @import("types.zig");

pub const AdapterCapabilities = types.AdapterCapabilities;
pub const AdapterContext = types.AdapterContext;
pub const AdapterDetection = types.AdapterDetection;
pub const AdapterEvent = types.AdapterEvent;
pub const AdapterEventSink = types.AdapterEventSink;
pub const AdapterId = types.AdapterId;
pub const AdapterRun = types.AdapterRun;
pub const AdapterRunStatus = types.AdapterRunStatus;
pub const AgentAdapter = types.AgentAdapter;
pub const AuthStatus = types.AuthStatus;
pub const EnvVar = types.EnvVar;
pub const IdentifierError = types.IdentifierError;
pub const ProviderId = types.ProviderId;
pub const Task = types.Task;
pub const detection = @import("detection.zig");
pub const events = @import("events.zig");
pub const registry = @import("registry.zig");

test {
    std.testing.refAllDecls(@This());
}
