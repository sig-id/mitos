#!/usr/bin/env python3
"""Backend-only smoke test for a local capability-backed agent run.

The script builds a temporary workload package with the three capability
pillars:

* SKILL: anchored markdown plus a fake-provider overlay.
* TOOLS: a deterministic Python post-hook copied into the runtime PATH.
* DATA: a read-only local corpus copied into the per-run runtime directory.

It then runs `Mitos.Workload.Runner.execute/3` through `mix run` and verifies the
canonical event log. No frontend assets or browser code are used.
"""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import stat
import subprocess
import sys
import tempfile
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


REPO = pathlib.Path(__file__).resolve().parents[1]
BEAM = REPO / "beam"
SIDECAR = REPO / "zig-out" / "bin" / "mitos-term"
MARKER = "MITOS_BACKEND_SMOKE "


def main() -> int:
    if not SIDECAR.exists():
        print(f"missing sidecar binary: {SIDECAR}", file=sys.stderr)
        print("build it first with: env ZIG_GLOBAL_CACHE_DIR=/tmp/zig-cache zig build", file=sys.stderr)
        return 2

    root = pathlib.Path(tempfile.mkdtemp(prefix="mitos-backend-smoke-"))
    try:
        workloads = root / "workloads"
        traces = root / "traces"
        package = workloads
        (package / "tools").mkdir(parents=True)
        (package / "data").mkdir(parents=True)
        traces.mkdir(parents=True)

        write_package(package)
        with RemoteProviderStub() as remote:
            result = run_backend(workloads, traces, remote.endpoint)
            assert result["skill_missing"] is True
            assert result["skill_invalid"] is True
            assert result["data_missing"] is True
            assert result["secret_env_rejected"] is True
            verify_remote_provider(result["remote"], remote.requests)
            verify_remote_missing_key(result["remote_missing_key"])
            verify_remote_cleanup(result["remote_cleanup"])
            verify_broker(result["broker"])

        verify_trace(traces, result["run_id"])
        verify_provider_trace(traces, result["provider_session_id"], result["provider_events"])
        print(json.dumps({"ok": True, "run_id": result["run_id"], "trace_dir": str(traces)}))
        return 0
    finally:
        make_tree_writable(root)
        shutil.rmtree(root, ignore_errors=True)


def write_package(package: pathlib.Path) -> None:
    (package / "skill.md").write_text(
        """<!-- @skill:role -->
Backend smoke agent.
<!-- @skill:rules -->
Base backend rule.
<!-- @skill:procedure -->
Use the tool to read data and emit RESULT.
<!-- @skill:success -->
RESULT and report.md exist.
""",
        encoding="utf-8",
    )

    tool = package / "tools" / "write_result.py"
    tool.write_text(
        """#!/usr/bin/env python3
import json
import os
import pathlib

skill = pathlib.Path(os.environ["MITOS_RESOLVED_SKILL"]).read_text()
data = (pathlib.Path(os.environ["MITOS_DATA_DIR"]) / "ref.txt").read_text().strip()
data_write_denied = False
try:
    (pathlib.Path(os.environ["MITOS_DATA_DIR"]) / "mutate.txt").write_text("bad")
except OSError:
    data_write_denied = True
report = pathlib.Path("report.md")
report.write_text(
    "overlay=%s\\ndata=%s\\ndata_write_denied=%s\\n"
    % ("Overlay backend smoke rule." in skill, data, data_write_denied)
)
pathlib.Path("RESULT").write_text(json.dumps({
    "signal": "ship",
    "confidence": 1.0,
    "summary": report.read_text().strip(),
    "evidence": [{"kind": "report", "path": "report.md"}],
}))
""",
        encoding="utf-8",
    )
    tool.chmod(0o755)

    (package / "data" / "ref.txt").write_text("backend-corpus\n", encoding="utf-8")

    workload = {
        "provider": "fake",
        "template": "backend smoke {{objective}}",
        "transport_order": ["stdio-json"],
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke",
            "harnesses": ["fake"],
            "trust_tier": "T1",
            "skill": "skill.md",
            "skill_adapt": {"fake": "@adapt append skill:rules Overlay backend smoke rule."},
            "data": {"path": "data", "mount": "/data", "ro": True},
            "tools": [
                {
                    "name": "write_result",
                    "exec": "tools/write_result.py",
                    "input": {},
                    "output": {},
                    "deterministic": True,
                    "side_effects": ["workspace_write", "data_read"],
                }
            ],
            "result": {"path": "RESULT", "evidence": ["report.md"]},
            "hooks": {
                "pre": [
                    {
                        "name": "warn-gate",
                        "run": ["/bin/sh", "-c", "exit 5"],
                        "on_fail": "warn",
                    },
                    {
                        "name": "skip-gate",
                        "run": ["/bin/sh", "-c", "exit 6"],
                        "on_fail": "skip",
                    },
                    {
                        "name": "prepare",
                        "run": ["/bin/sh", "-c", "printf prepared > prepared.txt"],
                        "on_fail": "block",
                    },
                ],
                "post": [
                    {
                        "name": "write-result",
                        "run": ["/bin/sh", "-c", "write_result.py"],
                        "on_fail": "block",
                    }
                ]
            },
        },
    }
    (package / "backend-smoke.json").write_text(json.dumps(workload), encoding="utf-8")

    missing_skill_workload = {
        "provider": "fake",
        "template": "missing skill {{objective}}",
        "transport_order": ["stdio-json"],
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke-missing-skill",
            "harnesses": ["fake"],
            "trust_tier": "T1",
            "skill": "missing.md",
            "result": {"path": "RESULT"},
        },
    }
    (package / "missing-skill-smoke.json").write_text(
        json.dumps(missing_skill_workload), encoding="utf-8"
    )

    (package / "invalid_skill.md").write_text(
        """<!-- @skill:role -->
Missing required anchors.
<!-- @skill:rules -->
Only rules.
""",
        encoding="utf-8",
    )

    invalid_skill_workload = {
        "provider": "fake",
        "template": "invalid skill {{objective}}",
        "transport_order": ["stdio-json"],
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke-invalid-skill",
            "harnesses": ["fake"],
            "trust_tier": "T1",
            "skill": "invalid_skill.md",
            "result": {"path": "RESULT"},
        },
    }
    (package / "invalid-skill-smoke.json").write_text(
        json.dumps(invalid_skill_workload), encoding="utf-8"
    )

    missing_data_workload = {
        "provider": "fake",
        "template": "missing data {{objective}}",
        "transport_order": ["stdio-json"],
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke-missing-data",
            "harnesses": ["fake"],
            "trust_tier": "T1",
            "data": {"path": "missing-data", "mount": "/data", "ro": True},
            "result": {"path": "RESULT"},
        },
    }
    (package / "missing-data-smoke.json").write_text(
        json.dumps(missing_data_workload), encoding="utf-8"
    )

    secret_env_workload = {
        "provider": "fake",
        "template": "secret env {{objective}}",
        "env_passthrough": ["ANTHROPIC_API_KEY"],
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke-secret-env",
            "harnesses": ["fake"],
            "trust_tier": "T1",
            "result": {"path": "RESULT"},
        },
    }
    (package / "secret-env-smoke.json").write_text(
        json.dumps(secret_env_workload), encoding="utf-8"
    )

    remote_cleanup_workload = {
        "provider": "fake",
        "template": "remote cleanup {{objective}}",
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke-remote-cleanup",
            "harnesses": ["fake"],
            "trust_tier": "T2",
            "result": {"path": "RESULT"},
        },
    }
    (package / "remote-cleanup-smoke.json").write_text(
        json.dumps(remote_cleanup_workload), encoding="utf-8"
    )

    remote_missing_key_workload = {
        "provider": "fake",
        "template": "remote missing key {{objective}}",
        "seller_id": "py-seller",
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke-remote-missing-key",
            "harnesses": ["fake"],
            "trust_tier": "T2",
            "egress_policy": [
                {
                    "id": "missing-provider-api",
                    "dest": "api.missing.test",
                    "credential_id": "missing-provider-key",
                }
            ],
            "result": {"path": "RESULT"},
        },
    }
    (package / "remote-missing-key-smoke.json").write_text(
        json.dumps(remote_missing_key_workload), encoding="utf-8"
    )


def run_backend(workloads: pathlib.Path, traces: pathlib.Path, remote_endpoint: str) -> dict[str, Any]:
    code = """
{:ok, workload} = Mitos.Workload.fetch("backend-smoke")
{:ok, result} = Mitos.Workload.Runner.execute(workload, %{"objective" => "from python"})

{:ok, missing_skill_workload} = Mitos.Workload.fetch("missing-skill-smoke")
{:error, {:runtime_prepare_failed, {:skill_unavailable, "missing.md", :enoent}}} =
  Mitos.Workload.Runner.execute(missing_skill_workload, %{"objective" => "from python"})

{:ok, invalid_skill_workload} = Mitos.Workload.fetch("invalid-skill-smoke")
{:error,
 {:runtime_prepare_failed,
  {:skill_resolution_failed, "invalid_skill.md",
   {:missing_skill_anchors, ["skill:procedure", "skill:success"]}}}} =
  Mitos.Workload.Runner.execute(invalid_skill_workload, %{"objective" => "from python"})

{:ok, missing_data_workload} = Mitos.Workload.fetch("missing-data-smoke")
{:error, {:runtime_prepare_failed, {:data_unavailable, "missing-data", :missing}}} =
  Mitos.Workload.Runner.execute(missing_data_workload, %{"objective" => "from python"})

{:error, {:invalid_workload, "capability workloads must not use env_passthrough"}} =
  Mitos.Workload.fetch("secret-env-smoke")

provider_session_id = "py-provider-" <> Base.url_encode64(:crypto.strong_rand_bytes(6), padding: false)
{:ok, handle} = Mitos.SandboxProvider.LocalSidecar.prepare(%{
  provider: "fake",
  task: "provider contract from python",
  session_id: provider_session_id,
  trace_base_dir: System.fetch_env!("MITOS_TRACE_DIR"),
  transport_order: ["stdio-json"],
  idle_ms: 20
})
{:ok, session} = Mitos.SandboxProvider.LocalSidecar.start(handle, [], %{})
provider_events =
  session
  |> Map.put(:stream_timeout_ms, 5_000)
  |> Mitos.SandboxProvider.LocalSidecar.stream()
  |> Enum.to_list()
{:ok, _harvest} = Mitos.SandboxProvider.LocalSidecar.harvest(session)
:ok = Mitos.SandboxProvider.LocalSidecar.stop(session)

Application.put_env(:mitos, :broker_endpoint, System.fetch_env!("MITOS_BROKER_ENDPOINT"))
bad_t2_launch_spec = %{
  provider: "fake",
  trust_tier: "T2",
  data: %{"snapshot" => "snap://data", "ro" => true},
  posture: %{}
}
{:error, {:invalid_t2_launch_spec, _}} =
  Mitos.SandboxProvider.RemoteMicrovm.prepare(bad_t2_launch_spec)

t2_launch_spec = %{
  provider: "fake",
  run_id: "py-remote-t2",
  trust_tier: "T2",
  data: %{"snapshot" => "snap://capability-data", "mount" => "/data", "ro" => true},
  resources: %{"timeout" => "60s"}
}
t2_launch_spec =
  Map.put(t2_launch_spec, :posture, Mitos.SandboxPolicy.launch_posture("T2", t2_launch_spec))
bad_t2_workspace =
  Map.put(t2_launch_spec, :workspace, %{"path" => "/host/workspace", "ro" => true})
{:error, {:invalid_t2_launch_spec, _}} =
  Mitos.SandboxProvider.RemoteMicrovm.prepare(bad_t2_workspace)
bad_t2_cwd = Map.put(t2_launch_spec, :cwd, "/host/workspace")
{:error, {:invalid_t2_launch_spec, _}} =
  Mitos.SandboxProvider.RemoteMicrovm.prepare(bad_t2_cwd)
{:ok, _no_data_handle} =
  t2_launch_spec
  |> Map.delete(:data)
  |> Mitos.SandboxProvider.RemoteMicrovm.prepare()
{:ok, remote_handle} = Mitos.SandboxProvider.RemoteMicrovm.prepare(t2_launch_spec)
{:error, {:invalid_t2_launch_spec, _}} =
  Mitos.SandboxProvider.RemoteMicrovm.start(remote_handle, [], %{"SECRET" => "bad"})
{:ok, remote_session} = Mitos.SandboxProvider.RemoteMicrovm.start(remote_handle, [], %{})
remote_events = Mitos.SandboxProvider.RemoteMicrovm.stream(remote_session) |> Enum.to_list()
:ok = Mitos.SandboxProvider.RemoteMicrovm.control(remote_session, {:steer, :answer, text: "approved"})
{:ok, remote_snapshot} = Mitos.SandboxProvider.RemoteMicrovm.snapshot(remote_session)
{:ok, remote_harvest} = Mitos.SandboxProvider.RemoteMicrovm.harvest(remote_session)
:ok = Mitos.SandboxProvider.RemoteMicrovm.stop(remote_session)

missing_id_launch_spec = Map.put(t2_launch_spec, :run_id, "py-remote-missing-id")
{:ok, missing_id_handle} = Mitos.SandboxProvider.RemoteMicrovm.prepare(missing_id_launch_spec)
{:error, :missing_remote_run_id} =
  Mitos.SandboxProvider.RemoteMicrovm.start(missing_id_handle, [], %{})

no_attestation_launch_spec = Map.put(t2_launch_spec, :run_id, "py-remote-no-attestation")
{:ok, no_attestation_handle} =
  Mitos.SandboxProvider.RemoteMicrovm.prepare(no_attestation_launch_spec)
{:error, {:egress_attestation_required, _}} =
  Mitos.SandboxProvider.RemoteMicrovm.start(no_attestation_handle, [], %{})

broker_upstream_endpoint = System.fetch_env!("MITOS_BROKER_UPSTREAM_ENDPOINT")
broker_upstream_uri = URI.parse(broker_upstream_endpoint)
broker_upstream_default_port =
  case broker_upstream_uri.scheme do
    "https" -> 443
    _ -> 80
  end
broker_upstream_dest =
  if broker_upstream_uri.port in [nil, broker_upstream_default_port] do
    broker_upstream_uri.host
  else
    broker_upstream_uri.host <> ":" <> Integer.to_string(broker_upstream_uri.port)
  end

{:ok, _broker_ref} =
  Mitos.KeyVault.register(%{
    seller_id: "py-seller",
    credential_id: "py-provider-key",
    type: "bearer",
    destinations: ["api.provider.test", "approval.provider.test", broker_upstream_dest],
    secret: "py-secret"
  })

{:ok, [_lease | _]} =
  Mitos.KeyVault.lease_run("py-broker-run", %{
    seller_id: "py-seller",
    egress_policy: [
      %{dest: "api.provider.test", credential_id: "py-provider-key", requires_approval: false},
      %{dest: broker_upstream_dest, credential_id: "py-provider-key", requires_approval: false}
    ]
  })

broker_conn =
  Plug.Test.conn(
    :post,
    "/broker/runs/py-broker-run/egress",
    Jason.encode!(%{credential_id: "py-provider-key", dest: "api.provider.test"})
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = broker_conn.status == 200
false = String.contains?(broker_conn.resp_body, "py-secret")
broker_body = Jason.decode!(broker_conn.resp_body)

broker_forward_conn =
  Plug.Test.conn(
    :post,
    "/broker/runs/py-broker-run/egress",
    Jason.encode!(%{
      credential_id: "py-provider-key",
      dest: broker_upstream_dest,
      url: broker_upstream_endpoint <> "/broker-upstream",
      method: "POST",
      headers: %{"authorization" => "Bearer sandbox-supplied", "x-smoke" => "python"},
      body: %{"ping" => "pong"},
      request_id: "py-broker-forward"
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = broker_forward_conn.status == 202
false = String.contains?(broker_forward_conn.resp_body, "py-secret")
broker_forward_body = Jason.decode!(broker_forward_conn.resp_body)
broker_events = Mitos.Trace.read_events("py-broker-run")
broker_fold = Mitos.Trace.fold_run("py-broker-run")

{:ok, [_approval_lease]} =
  Mitos.KeyVault.lease_run("py-broker-approval-run", %{
    seller_id: "py-seller",
    egress_policy: [
      %{
        dest: "approval.provider.test",
        credential_id: "py-provider-key",
        requires_approval: true
      }
    ]
  })

broker_approval_conn =
  Plug.Test.conn(
    :post,
    "/broker/runs/py-broker-approval-run/egress",
    Jason.encode!(%{
      credential_id: "py-provider-key",
      dest: "approval.provider.test",
      request_id: "py-broker-approval"
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = broker_approval_conn.status == 202
false = String.contains?(broker_approval_conn.resp_body, "py-secret")
broker_approval_body = Jason.decode!(broker_approval_conn.resp_body)
broker_approval_events = Mitos.Trace.read_events("py-broker-approval-run")
broker_approval_fold = Mitos.Trace.fold_run("py-broker-approval-run")

:ok = Mitos.KeyVault.revoke_run("py-broker-run")

broker_revoked_conn =
  Plug.Test.conn(
    :post,
    "/broker/runs/py-broker-run/egress",
    Jason.encode!(%{credential_id: "py-provider-key", dest: "api.provider.test"})
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = broker_revoked_conn.status == 403

defmodule Mitos.BackendSmokeRemoteProvider do
  @behaviour Mitos.SandboxProvider

  @impl true
  def prepare(%{} = launch_spec), do: {:ok, launch_spec}

  @impl true
  def start(%{} = launch_spec, _argv, env) do
    send(self(), {:backend_smoke_cleanup_env, env})

    {:ok,
     %{
       run_id: launch_spec.run_id,
       events: [
         %{"type" => "FINAL_OUTPUT", "text" => "cleanup smoke"},
         %{"type" => "PROCESS_EXITED", "code" => 0}
       ]
     }}
  end

  @impl true
  def stream(%{events: events}), do: events

  @impl true
  def control(_session, _command), do: {:error, :unsupported_control}

  @impl true
  def snapshot(_session), do: :unsupported

  @impl true
  def harvest(_session), do: {:error, :harvest_failed}

  @impl true
  def stop(%{run_id: run_id}) do
    send(self(), {:backend_smoke_cleanup_stopped, run_id})
    :ok
  end

  @impl true
  def capabilities do
    %{
      isolation: :microvm,
      remote?: true,
      egress: :broker,
      data_mount: :snapshot,
      metering: [:cpu, :mem, :wall, :egress],
      snapshot?: true,
      net_modes: [:broker_proxy]
    }
  end

  @impl true
  def child_spec(_opts), do: %{id: __MODULE__, start: {Task, :start_link, [fn -> :ok end]}}
end

Application.put_env(:mitos, :sandbox_providers, [Mitos.BackendSmokeRemoteProvider])
Application.delete_env(:mitos, :key_vault_leases)

{:ok, missing_key_workload} = Mitos.Workload.fetch("remote-missing-key-smoke")
{:error, {:launch_failed, missing_key_run_id, {:credential_lease_failed, missing_key_failures}}} =
  Mitos.Workload.Runner.execute(missing_key_workload, %{"objective" => "from python"})

missing_key_provider_started =
  receive do
    {:backend_smoke_cleanup_env, _value} -> true
  after
    100 -> false
  end

false = missing_key_provider_started

{:ok, cleanup_workload} = Mitos.Workload.fetch("remote-cleanup-smoke")
{:error, {:launch_failed, cleanup_run_id, :harvest_failed}} =
  Mitos.Workload.Runner.execute(cleanup_workload, %{"objective" => "from python"})

cleanup_env =
  receive do
    {:backend_smoke_cleanup_env, value} -> value
  after
    1_000 -> :missing
  end

cleanup_stopped =
  receive do
    {:backend_smoke_cleanup_stopped, ^cleanup_run_id} -> true
  after
    1_000 -> false
  end

cleanup_lease_present =
  Application.get_env(:mitos, :key_vault_leases, %{})
  |> Map.has_key?(cleanup_run_id)

IO.puts("MITOS_BACKEND_SMOKE " <> Jason.encode!(%{
  run_id: result.run_id,
  summary: result.result,
  skill_missing: true,
  skill_invalid: true,
  data_missing: true,
  secret_env_rejected: true,
  provider_session_id: provider_session_id,
  provider_events: Enum.map(provider_events, & &1["type"]),
  remote: %{
    events: Enum.map(remote_events, & &1["type"]),
    egress_attestation: remote_session.egress_attestation,
    snapshot: remote_snapshot,
    harvest: remote_harvest
  },
  remote_missing_key: %{
    run_id: missing_key_run_id,
    failures: missing_key_failures,
    provider_started: missing_key_provider_started,
    lease_present:
      Application.get_env(:mitos, :key_vault_leases, %{})
      |> Map.has_key?(missing_key_run_id)
  },
  remote_cleanup: %{
    run_id: cleanup_run_id,
    env: cleanup_env,
    stopped: cleanup_stopped,
    lease_present: cleanup_lease_present
  },
  broker: %{
    decision: broker_body["decision"],
    credential: broker_body["credential"],
    forward_status: broker_forward_conn.status,
    forward_authorization: broker_forward_body["authorization"],
    forward_body: broker_forward_body["body"],
    event_types: Enum.map(broker_events, & &1["type"]),
    replay_requires_approval: broker_fold["egress_replay_requires_approval?"],
    revoked_status: broker_revoked_conn.status,
    approval: %{
      status: broker_approval_conn.status,
      decision: broker_approval_body["decision"],
      event_types: Enum.map(broker_approval_events, & &1["type"]),
      replay_requires_approval: broker_approval_fold["egress_replay_requires_approval?"]
    }
  }
}))
"""
    env = os.environ.copy()
    env.update(
        {
            "MIX_ENV": "test",
            "MITOS_WORKLOADS_DIR": str(workloads),
            "MITOS_TRACE_DIR": str(traces),
            "MITOS_TERM_BIN": str(SIDECAR),
            "MITOS_REMOTE_MICROVM_ENDPOINT": remote_endpoint,
            "MITOS_BROKER_UPSTREAM_ENDPOINT": remote_endpoint,
            "MITOS_BROKER_ENDPOINT": "https://broker.test/proxy",
        }
    )

    proc = subprocess.run(
        ["mix", "run", "-e", code],
        cwd=BEAM,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        check=False,
    )

    if proc.returncode != 0:
        print(proc.stdout, file=sys.stderr)
        raise SystemExit(proc.returncode)

    for line in proc.stdout.splitlines():
        if line.startswith(MARKER):
            return json.loads(line[len(MARKER) :])

    print(proc.stdout, file=sys.stderr)
    raise RuntimeError("backend smoke marker missing")


def verify_trace(traces: pathlib.Path, run_id: str) -> None:
    run_dir = traces / ".runs" / run_id
    events = read_jsonl(run_dir / "events.jsonl")
    runtime = run_dir / "runtime"

    assert (runtime / "skill" / "resolved.md").read_text(encoding="utf-8").find(
        "Overlay backend smoke rule."
    ) >= 0
    assert (runtime / "tools" / "write_result.py").exists()
    assert (runtime / "data" / "ref.txt").read_text(encoding="utf-8") == "backend-corpus\n"
    assert not (runtime / "data" / "mutate.txt").exists()
    assert (runtime / "workspace" / "prepared.txt").read_text(encoding="utf-8") == "prepared"

    tools_manifest = json.loads((runtime / "tools" / "tools.json").read_text(encoding="utf-8"))
    assert tools_manifest["items"][0]["runtime_exec"] == str(runtime / "tools" / "write_result.py")
    assert is_read_only(runtime / "skill")
    assert is_read_only(runtime / "tools")
    assert is_read_only(runtime / "data")

    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "hook_executed"
        and event.get("ref", {}).get("name") == "write-result"
        and event.get("payload", {}).get("success?") is True
        for event in events
    )
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "hook_executed"
        and event.get("ref", {}).get("phase") == "pre"
        and event.get("ref", {}).get("name") == "warn-gate"
        and event.get("payload", {}).get("success?") is False
        and event.get("payload", {}).get("blocked?") is False
        for event in events
    )
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "hook_executed"
        and event.get("ref", {}).get("phase") == "pre"
        and event.get("ref", {}).get("name") == "skip-gate"
        and event.get("payload", {}).get("success?") is False
        and event.get("payload", {}).get("blocked?") is False
        for event in events
    )

    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "capability_finished"
        and event.get("payload", {}).get("result", {}).get("signal") == "ship"
        and event.get("payload", {}).get("result", {}).get("summary")
        == "overlay=True\ndata=backend-corpus\ndata_write_denied=True"
        for event in events
    )

    assert any(
        event.get("kind") == "decision"
        and event.get("params", {}).get("capability_runtime", {}).get("workspace", "").startswith(
            str(runtime)
        )
        for event in events
    )


def verify_provider_trace(traces: pathlib.Path, session_id: str, event_types: list[str]) -> None:
    assert "RUN_STARTED" in event_types
    assert "FINAL_OUTPUT" in event_types
    assert "PROCESS_EXITED" in event_types or "exit" in event_types

    events = read_jsonl(traces / ".runs" / session_id / "events.jsonl")
    assert any(event.get("kind") == "decision" for event in events)
    assert any(
        event.get("kind") == "effect" and event.get("type") == "capability_finished"
        for event in events
    )


def verify_remote_provider(remote: dict[str, Any], requests: list[dict[str, Any]]) -> None:
    assert remote["events"] == ["RUN_STARTED", "FINAL_OUTPUT", "PROCESS_EXITED"]
    assert remote["egress_attestation"] == {
        "enforced": True,
        "egress": "broker",
        "net_mode": "deny_all_except_broker",
        "deny_all_else": True,
        "host_network": False,
        "loopback_bypass": False,
        "mechanism": "forced_proxy",
        "broker_endpoint": "https://broker.test/proxy",
    }
    assert remote["snapshot"] == {"id": "snap-smoke", "sha256": "sha256:remote-smoke"}
    assert remote["harvest"]["result"]["signal"] == "ship"
    assert remote["harvest"]["result"]["summary"] == "remote smoke"

    run_request = find_request(requests, "POST", "/runs")
    assert run_request["body"]["env"] == {}
    launch_spec = run_request["body"]["launch_spec"]
    assert launch_spec["trust_tier"] == "T2"
    assert launch_spec["posture"]["net_mode"] == "deny_all_except_broker"
    assert launch_spec["posture"]["egress"] == "broker"
    assert launch_spec["posture"]["broker_endpoint"] == "https://broker.test/proxy"
    assert launch_spec["posture"]["workspace"]["host_bind"] is False
    assert launch_spec["posture"]["data"]["host_bind"] is False

    assert [req["body"]["cursor"] for req in requests if req["path"].endswith("/events")] == [
        "0",
        "1",
    ]
    control = find_request(requests, "POST", "/runs/remote-smoke-1/control")
    assert control["body"]["command"] == {
        "cmd": "steer",
        "interaction": "answer",
        "text": "approved",
    }
    assert find_request(requests, "POST", "/runs/remote-smoke-1/snapshot")
    assert find_request(requests, "POST", "/runs/remote-smoke-1/harvest")
    assert find_request(requests, "POST", "/runs/remote-smoke-1/stop")
    forward_request = find_request(requests, "POST", "/broker-upstream")
    assert forward_request["headers"]["authorization"] == "Bearer py-secret"
    assert forward_request["headers"]["x-smoke"] == "python"
    assert forward_request["body"] == {"ping": "pong"}


def verify_remote_cleanup(cleanup: dict[str, Any]) -> None:
    assert cleanup["run_id"].startswith("wl-")
    assert cleanup["env"] is None
    assert cleanup["stopped"] is True
    assert cleanup["lease_present"] is False


def verify_remote_missing_key(missing_key: dict[str, Any]) -> None:
    assert missing_key["run_id"].startswith("wl-")
    assert missing_key["provider_started"] is False
    assert missing_key["lease_present"] is False
    assert missing_key["failures"] == [
        {
            "rule": "missing-provider-api",
            "dest": "api.missing.test",
            "credential_id": "missing-provider-key",
            "reason": "credential unavailable",
        }
    ]


def verify_broker(broker: dict[str, Any]) -> None:
    assert broker["decision"] == "allow"
    assert broker["revoked_status"] == 403
    assert broker["replay_requires_approval"] is False
    assert broker["forward_status"] == 202
    assert broker["forward_authorization"] == "Bearer [redacted]"
    assert broker["forward_body"] == {"ping": "pong"}
    assert broker["event_types"] == [
        "egress_attempted",
        "egress_allowed",
        "egress_completed",
        "egress_attempted",
        "egress_allowed",
        "egress_completed",
    ]
    assert broker["credential"] == {
        "credential_id": "py-provider-key",
        "credential_type": "bearer",
        "seller_id": "py-seller",
        "dest": "api.provider.test",
    }
    assert broker["approval"] == {
        "status": 202,
        "decision": "pending_approval",
        "event_types": ["egress_attempted", "approval_requested"],
        "replay_requires_approval": True,
    }


def find_request(requests: list[dict[str, Any]], method: str, path: str) -> dict[str, Any]:
    for request in requests:
        if request["method"] == method and request["path"] == path:
            return request
    raise AssertionError(f"missing request {method} {path}")


def read_jsonl(path: pathlib.Path) -> list[dict[str, Any]]:
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line]


def is_read_only(path: pathlib.Path) -> bool:
    return (path.stat().st_mode & 0o222) == 0


def make_tree_writable(path: pathlib.Path) -> None:
    if not path.exists():
        return

    for child in path.rglob("*"):
        try:
            mode = child.stat().st_mode
            if child.is_dir():
                child.chmod(mode | stat.S_IWUSR | stat.S_IXUSR)
            else:
                child.chmod(mode | stat.S_IWUSR)
        except OSError:
            pass

    try:
        path.chmod(path.stat().st_mode | stat.S_IWUSR | stat.S_IXUSR)
    except OSError:
        pass


class RemoteProviderStub:
    def __init__(self) -> None:
        self.requests: list[dict[str, Any]] = []
        self._server: ThreadingHTTPServer | None = None
        self._thread: threading.Thread | None = None
        self.endpoint = ""

    def __enter__(self) -> "RemoteProviderStub":
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self) -> None:  # noqa: N802 - stdlib callback name
                length = int(self.headers.get("content-length", "0"))
                raw = self.rfile.read(length).decode("utf-8")
                body = json.loads(raw) if raw else {}
                headers = {key.lower(): value for key, value in self.headers.items()}
                owner.requests.append(
                    {"method": "POST", "path": self.path, "headers": headers, "body": body}
                )
                status, response = owner.response(self.path, headers, body)
                encoded = json.dumps(response).encode("utf-8")
                self.send_response(status)
                self.send_header("content-type", "application/json")
                self.send_header("content-length", str(len(encoded)))
                self.end_headers()
                self.wfile.write(encoded)

            def log_message(self, _format: str, *_args: Any) -> None:
                return

        self._server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        host, port = self._server.server_address
        self.endpoint = f"http://{host}:{port}"
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()
        return self

    def __exit__(self, *_exc: object) -> None:
        if self._server:
            self._server.shutdown()
            self._server.server_close()
        if self._thread:
            self._thread.join(timeout=2)

    def response(
        self, path: str, headers: dict[str, str], body: dict[str, Any]
    ) -> tuple[int, dict[str, Any]]:
        if path == "/broker-upstream":
            return (
                202,
                {
                    "ok": True,
                    "authorization": headers.get("authorization"),
                    "client": headers.get("x-smoke"),
                    "body": body,
                },
            )

        if path == "/runs":
            if body.get("launch_spec", {}).get("run_id") == "py-remote-missing-id":
                return (200, {"events": [{"type": "RUN_STARTED"}]})

            if body.get("launch_spec", {}).get("run_id") == "py-remote-no-attestation":
                return (200, {"run_id": "remote-no-attestation"})

            return (
                200,
                {
                    "run_id": "remote-smoke-1",
                    "egress_attestation": {
                        "enforced": True,
                        "egress": "broker",
                        "net_mode": "deny_all_except_broker",
                        "deny_all_else": True,
                        "host_network": False,
                        "loopback_bypass": False,
                        "mechanism": "forced_proxy",
                        "broker_endpoint": "https://broker.test/proxy",
                    },
                    "event_cursor": "0",
                    "stream_poll_ms": 1,
                    "stream_max_empty_polls": 2,
                },
            )

        if path == "/runs/remote-smoke-1/events" and body.get("cursor") == "0":
            return (
                200,
                {
                    "events": [{"type": "RUN_STARTED", "provider": "fake"}],
                    "next_cursor": "1",
                },
            )

        if path == "/runs/remote-smoke-1/events" and body.get("cursor") == "1":
            return (
                200,
                {
                    "events": [
                        {"type": "FINAL_OUTPUT", "text": "remote smoke"},
                        {"type": "PROCESS_EXITED", "code": 0},
                    ],
                    "next_cursor": "2",
                    "done": True,
                },
            )

        if path == "/runs/remote-smoke-1/control":
            return (200, {"ok": True})

        if path == "/runs/remote-smoke-1/snapshot":
            return (200, {"snapshot": {"id": "snap-smoke", "sha256": "sha256:remote-smoke"}})

        if path == "/runs/remote-smoke-1/harvest":
            return (
                200,
                {
                    "harvest": {
                        "diff": {"empty?": True, "files_changed": []},
                        "result": {"signal": "ship", "summary": "remote smoke"},
                        "artifacts": [],
                    }
                },
            )

        if path == "/runs/remote-smoke-1/stop":
            return (200, {"ok": True})

        return (404, {"ok": False, "error": f"unexpected route {path}"})


if __name__ == "__main__":
    raise SystemExit(main())
