use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

use crate::config::{
    ResourceLimits, SandboxBackendKind, SandboxMode, SandboxNetworkMode, SandboxRuntime,
};
use crate::{RunId, Timestamp};

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type")]
pub enum RuntimeEvent {
    SandboxPrepared(SandboxPrepared),
    SandboxEvent(SandboxEvent),
    ResourceSample(ResourceSample),
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxPrepared {
    pub run_id: RunId,
    pub timestamp: Timestamp,
    pub backend: SandboxBackendKind,
    pub mode: SandboxMode,
    pub runtime: SandboxRuntime,
    pub container_id: String,
    pub workspace_mount: String,
    pub network: SandboxNetworkMode,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub resources: Option<ResourceLimits>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxEvent {
    pub run_id: RunId,
    pub timestamp: Timestamp,
    pub backend: SandboxBackendKind,
    pub container_id: String,
    pub event_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
    #[serde(default)]
    pub is_error: bool,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub metadata: BTreeMap<String, serde_json::Value>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ResourceSample {
    pub run_id: RunId,
    pub timestamp: Timestamp,
    pub backend: SandboxBackendKind,
    pub container_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cpu_millis: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub memory_bytes: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pids: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub io_read_bytes: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub io_write_bytes: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub network_rx_bytes: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub network_tx_bytes: Option<u64>,
}
