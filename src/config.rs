use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;

use crate::Result;

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AgentRuntimeConfig {
    pub version: u32,
    pub workspace: WorkspaceConfig,
    pub sandbox: SandboxConfig,
    #[serde(default)]
    pub providers: BTreeMap<String, ProviderConfig>,
    pub policy: PolicyConfig,
    #[serde(default)]
    pub watcher: WatcherConfig,
}

impl AgentRuntimeConfig {
    pub fn from_yaml_str(input: &str) -> Result<Self> {
        Ok(serde_yaml::from_str(input)?)
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WorkspaceConfig {
    pub mode: WorkspaceMode,
    pub base_branch: String,
    pub clean_before_run: bool,
    pub container_mount: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum WorkspaceMode {
    #[serde(rename = "git-worktree")]
    GitWorktree,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SandboxConfig {
    pub backend: SandboxBackendKind,
    pub mode: SandboxMode,
    pub runtime: SandboxRuntime,
    pub network: SandboxNetworkMode,
    pub workdir: String,
    pub home: String,
    pub resources: ResourceLimits,
    pub toolchain_rootfs: String,
    pub credentials: CredentialsConfig,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum SandboxBackendKind {
    #[serde(rename = "none")]
    NoSandbox,
    #[serde(rename = "host-process")]
    HostProcess,
    #[serde(rename = "nucleus")]
    Nucleus,
    #[serde(rename = "remote")]
    Remote,
    #[serde(rename = "vm")]
    Vm,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum SandboxMode {
    #[serde(rename = "none")]
    None,
    #[serde(rename = "host-process")]
    HostProcess,
    #[serde(rename = "strict-agent")]
    StrictAgent,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum SandboxRuntime {
    #[serde(rename = "none")]
    None,
    #[serde(rename = "native")]
    Native,
    #[serde(rename = "gvisor")]
    Gvisor,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum SandboxNetworkMode {
    #[serde(rename = "none")]
    None,
    #[serde(rename = "host")]
    Host,
    #[serde(rename = "bridge")]
    Bridge,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ResourceLimits {
    pub memory: String,
    pub cpus: u16,
    pub pids: u32,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CredentialsConfig {
    pub pass_through: CredentialPassThrough,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum CredentialPassThrough {
    #[serde(rename = "explicit_only")]
    ExplicitOnly,
    #[serde(rename = "none")]
    None,
    #[serde(rename = "inherit")]
    Inherit,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProviderConfig {
    pub adapter_order: Vec<AdapterKind>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum AdapterKind {
    #[serde(rename = "headless_json")]
    HeadlessJson,
    #[serde(rename = "exec_json")]
    ExecJson,
    #[serde(rename = "pty")]
    Pty,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PolicyConfig {
    pub default: PolicyDecision,
    #[serde(default)]
    pub allow: Vec<PolicyRule>,
    #[serde(default)]
    pub ask: Vec<PolicyRule>,
    #[serde(default)]
    pub block: Vec<PolicyRule>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum PolicyDecision {
    #[serde(rename = "allow")]
    Allow,
    #[serde(rename = "ask")]
    Ask,
    #[serde(rename = "block")]
    Block,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PolicyRule {
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub action: Option<PolicyAction>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub command: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub path: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum PolicyAction {
    #[serde(rename = "read_workspace")]
    ReadWorkspace,
    #[serde(rename = "write_workspace")]
    WriteWorkspace,
    #[serde(rename = "run_tests")]
    RunTests,
    #[serde(rename = "install_package")]
    InstallPackage,
    #[serde(rename = "network_access")]
    NetworkAccess,
    #[serde(rename = "git_push")]
    GitPush,
    #[serde(rename = "write_outside_workspace")]
    WriteOutsideWorkspace,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct WatcherConfig {
    #[serde(default)]
    pub enabled: bool,
    pub mode: WatcherMode,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
}

impl Default for WatcherConfig {
    fn default() -> Self {
        Self {
            enabled: false,
            mode: WatcherMode::LocalOnly,
            model: None,
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub enum WatcherMode {
    #[serde(rename = "local_only")]
    LocalOnly,
    #[serde(rename = "remote_approved_context")]
    RemoteApprovedContext,
}
