use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

use crate::config::{SandboxBackendKind, SandboxMode, SandboxNetworkMode, SandboxRuntime};
use crate::events::{ResourceSample, RuntimeEvent, SandboxEvent, SandboxPrepared};
use crate::Result;

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct NucleusEvent {
    pub timestamp: String,
    pub run_id: String,
    pub container_id: String,
    pub event_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub detail: Option<String>,
    #[serde(default)]
    pub is_error: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub mode: Option<SandboxMode>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub runtime: Option<SandboxRuntime>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub workspace_mount: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub network: Option<SandboxNetworkMode>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub resources: Option<NucleusResourceSample>,
    #[serde(flatten)]
    pub metadata: BTreeMap<String, serde_json::Value>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct NucleusResourceSample {
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

pub fn parse_nucleus_jsonl(input: &str) -> Result<Vec<RuntimeEvent>> {
    input
        .lines()
        .filter(|line| !line.trim().is_empty())
        .map(|line| {
            let event: NucleusEvent = serde_json::from_str(line)?;
            Ok(RuntimeEvent::from(event))
        })
        .collect()
}

impl From<NucleusEvent> for RuntimeEvent {
    fn from(event: NucleusEvent) -> Self {
        match event.event_type.as_str() {
            "container_start" => RuntimeEvent::SandboxPrepared(SandboxPrepared {
                run_id: event.run_id,
                timestamp: event.timestamp,
                backend: SandboxBackendKind::Nucleus,
                mode: event.mode.unwrap_or(SandboxMode::StrictAgent),
                runtime: event.runtime.unwrap_or(SandboxRuntime::Gvisor),
                container_id: event.container_id,
                workspace_mount: event
                    .workspace_mount
                    .unwrap_or_else(|| "/workspace".to_string()),
                network: event.network.unwrap_or(SandboxNetworkMode::None),
                resources: None,
            }),
            "resource_sample" => {
                let resources = event.resources.unwrap_or_default();
                RuntimeEvent::ResourceSample(ResourceSample {
                    run_id: event.run_id,
                    timestamp: event.timestamp,
                    backend: SandboxBackendKind::Nucleus,
                    container_id: event.container_id,
                    cpu_millis: resources.cpu_millis,
                    memory_bytes: resources.memory_bytes,
                    pids: resources.pids,
                    io_read_bytes: resources.io_read_bytes,
                    io_write_bytes: resources.io_write_bytes,
                    network_rx_bytes: resources.network_rx_bytes,
                    network_tx_bytes: resources.network_tx_bytes,
                })
            }
            _ => RuntimeEvent::SandboxEvent(SandboxEvent {
                run_id: event.run_id,
                timestamp: event.timestamp,
                backend: SandboxBackendKind::Nucleus,
                container_id: event.container_id,
                event_type: event.event_type,
                detail: event.detail,
                is_error: event.is_error,
                metadata: event.metadata,
            }),
        }
    }
}

impl Default for NucleusResourceSample {
    fn default() -> Self {
        Self {
            cpu_millis: None,
            memory_bytes: None,
            pids: None,
            io_read_bytes: None,
            io_write_bytes: None,
            network_rx_bytes: None,
            network_tx_bytes: None,
        }
    }
}
