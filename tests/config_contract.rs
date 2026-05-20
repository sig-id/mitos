use mitos::config::{
    AgentRuntimeConfig, CredentialPassThrough, PolicyAction, SandboxBackendKind, SandboxMode,
    SandboxNetworkMode, SandboxRuntime, WorkspaceMode,
};

#[test]
fn agent_runtime_yaml_matches_locked_shape() {
    let config = AgentRuntimeConfig::from_yaml_str(include_str!("../.agent-runtime.yaml")).unwrap();

    assert_eq!(config.version, 1);
    assert_eq!(config.workspace.mode, WorkspaceMode::GitWorktree);
    assert_eq!(config.workspace.container_mount, "/workspace");
    assert_eq!(config.sandbox.backend, SandboxBackendKind::Nucleus);
    assert_eq!(config.sandbox.mode, SandboxMode::StrictAgent);
    assert_eq!(config.sandbox.runtime, SandboxRuntime::Gvisor);
    assert_eq!(config.sandbox.network, SandboxNetworkMode::None);
    assert_eq!(
        config.sandbox.credentials.pass_through,
        CredentialPassThrough::ExplicitOnly
    );
    assert_eq!(config.providers["codex"].adapter_order.len(), 2);
    assert_eq!(
        config.policy.allow[0].action,
        Some(PolicyAction::ReadWorkspace)
    );
    assert!(!config.watcher.enabled);
}
