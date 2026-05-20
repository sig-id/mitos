use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

use crate::config::{
    ResourceLimits, SandboxBackendKind, SandboxMode, SandboxNetworkMode, SandboxRuntime,
};
use crate::events::SandboxEvent;
use crate::{Result, RunId, Timestamp};

pub type SandboxEventStream = Box<dyn Iterator<Item = Result<SandboxEvent>> + Send>;

pub trait SandboxBackend: Send + Sync {
    fn prepare(&self, request: SandboxRequest) -> Result<SandboxHandle>;
    fn spawn(&self, handle: &SandboxHandle, command: SandboxCommand) -> Result<SandboxProcess>;
    fn attach_pty(&self, handle: &SandboxHandle, size: PtySize) -> Result<SandboxPty>;
    fn events(&self, handle: &SandboxHandle) -> Result<SandboxEventStream>;
    fn terminate(&self, handle: &SandboxHandle, reason: TerminationReason) -> Result<()>;
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxRequest {
    pub run_id: RunId,
    pub container_name: String,
    pub backend: SandboxBackendKind,
    pub mode: SandboxMode,
    pub runtime: SandboxRuntime,
    pub mounts: SandboxMounts,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub environment: BTreeMap<String, String>,
    pub resources: ResourceLimits,
    pub network: NetworkPolicy,
    pub events: SandboxEventsConfig,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxCommand {
    pub argv: Vec<String>,
    pub cwd: String,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub env: BTreeMap<String, String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxHandle {
    pub run_id: RunId,
    pub backend: SandboxBackendKind,
    pub mode: SandboxMode,
    pub runtime: SandboxRuntime,
    pub container_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub process_id: Option<u32>,
    pub workspace_mount: String,
    pub event_channel: SandboxEventChannel,
    pub io: SandboxIo,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxProcess {
    pub run_id: RunId,
    pub container_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub process_id: Option<u32>,
    pub argv: Vec<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub started_at: Option<Timestamp>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxPty {
    pub run_id: RunId,
    pub container_id: String,
    pub size: PtySize,
    pub endpoint: PtyEndpoint,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxMounts {
    pub workspace: SandboxMount,
    pub home: SandboxMount,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context: Option<SandboxMount>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub secrets: Vec<SandboxMount>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxMount {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub host_path: Option<String>,
    pub container_path: String,
    pub mount_type: MountType,
    pub access: MountAccess,
    #[serde(default)]
    pub optional: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum MountType {
    #[serde(rename = "bind")]
    Bind,
    #[serde(rename = "tmpfs")]
    Tmpfs,
    #[serde(rename = "secret")]
    Secret,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum MountAccess {
    #[serde(rename = "read_only")]
    ReadOnly,
    #[serde(rename = "read_write")]
    ReadWrite,
    #[serde(rename = "read_write_execute")]
    ReadWriteExecute,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct NetworkPolicy {
    pub mode: SandboxNetworkMode,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub egress_allowlist: Vec<EgressRule>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct EgressRule {
    pub host: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub port: Option<u16>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub protocol: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxEventsConfig {
    pub format: SandboxEventFormat,
    pub channel: SandboxEventChannel,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum SandboxEventFormat {
    #[serde(rename = "jsonl")]
    Jsonl,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum SandboxEventChannel {
    #[serde(rename = "sidecar")]
    Sidecar,
    #[serde(rename = "fd")]
    Fd,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxIo {
    pub stdin: IoEndpoint,
    pub stdout: IoEndpoint,
    pub stderr: IoEndpoint,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pty: Option<PtyEndpoint>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum IoEndpoint {
    #[serde(rename = "inherit")]
    Inherit,
    #[serde(rename = "null")]
    Null,
    #[serde(rename = "pipe")]
    Pipe,
    #[serde(rename = "path")]
    Path(String),
    #[serde(rename = "fd")]
    Fd(i32),
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum PtyEndpoint {
    #[serde(rename = "console_socket")]
    ConsoleSocket(String),
    #[serde(rename = "fd")]
    Fd(i32),
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PtySize {
    pub rows: u16,
    pub cols: u16,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pixel_width: Option<u16>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pixel_height: Option<u16>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum TerminationReason {
    #[serde(rename = "user_cancelled")]
    UserCancelled,
    #[serde(rename = "run_completed")]
    RunCompleted,
    #[serde(rename = "run_failed")]
    RunFailed,
    #[serde(rename = "timeout")]
    Timeout,
    #[serde(rename = "runtime_shutdown")]
    RuntimeShutdown,
}
