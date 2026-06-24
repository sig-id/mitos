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
        apps = root / "apps"
        app_package = apps / "backend-smoke-app"
        packages = root / "packages"
        traces = root / "traces"
        package = workloads
        (package / "tools").mkdir(parents=True)
        (package / "data").mkdir(parents=True)
        (package / "pipelines").mkdir(parents=True)
        apps.mkdir(parents=True)
        packages.mkdir(parents=True)
        traces.mkdir(parents=True)

        write_package(package)
        shutil.copytree(package, app_package)
        with RemoteProviderStub() as remote:
            result = run_backend(workloads, apps, packages, traces, remote.endpoint)
            assert result["skill_missing"] is True
            assert result["skill_invalid"] is True
            assert result["data_missing"] is True
            assert result["secret_env_rejected"] is True
            assert result["capability_ref"].startswith("cap:sha256:")
            assert result["capability_identity_changes"] is True
            verify_api_route(result["api_route"])
            verify_app_run(result["app_run"])
            verify_app_chain(result["app_chain"])
            verify_app_route(result["app_route"])
            verify_app_signal(result["app_signal"])
            verify_app_activation(result["app_activation"])
            verify_app_package(result["app_package"])
            verify_app_install(result["app_install"])
            verify_installed_app_run(result["installed_app_run"])
            verify_installed_app_signal(result["installed_app_signal"])
            verify_remote_provider(result["remote"], remote.requests)
            verify_paid_route(result["paid_route"], remote.requests)
            verify_remote_timeout(result["remote_timeout"], remote.requests)
            verify_remote_missing_key(result["remote_missing_key"])
            verify_remote_cleanup(result["remote_cleanup"])
            verify_broker(result["broker"])

        verify_trace(traces, result["run_id"])
        verify_trace(traces, result["api_route"]["run_id"])
        verify_trace(traces, result["app_run"]["step_run_id"])
        verify_app_trace(traces, result["app_run"])
        verify_trace(traces, result["app_chain"]["first_step_run_id"])
        verify_app_trace(traces, result["app_chain"])
        verify_trace(traces, result["app_route"]["step_run_id"])
        verify_app_trace(traces, result["app_route"])
        verify_trace(traces, result["app_signal"]["step_run_id"])
        verify_app_trace(traces, result["app_signal"])
        verify_app_signal_trace(traces, result["app_signal"])
        verify_trace(traces, result["installed_app_run"]["step_run_id"])
        verify_app_trace(traces, result["installed_app_run"])
        verify_trace(traces, result["installed_app_signal"]["step_run_id"])
        verify_app_trace(traces, result["installed_app_signal"])
        verify_paid_trace(traces, result["paid_route"]["run_id"])
        verify_remote_timeout_trace(traces, result["remote_timeout"]["run_id"])
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

    review_workload = {
        "provider": "fake",
        "template": "Review {{objective}} after {{previous_results}}",
        "transport_order": ["stdio-json"],
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-review",
            "harnesses": ["fake"],
            "trust_tier": "T1",
        },
    }
    (package / "backend-review.json").write_text(json.dumps(review_workload), encoding="utf-8")

    (package / "pipelines" / "backend-app.md").write_text(
        """# Pipeline: backend-app

Goal: run the backend smoke capability through an application entrypoint.

## Stages
1. **backend-smoke** - execute the local SKILL/TOOLS/DATA capability.
""",
        encoding="utf-8",
    )

    (package / "pipelines" / "backend-chain.md").write_text(
        """# Pipeline: backend-chain

Goal: run a two-step backend application pipeline.

## Stages
1. **backend-smoke** - execute the local SKILL/TOOLS/DATA capability.
2. **backend-review** - inspect the previous capability result.
""",
        encoding="utf-8",
    )

    app_manifest = {
        "name": "backend-smoke-app",
        "version": "1.0.0",
        "capabilities": [
            {"ref": "backend-smoke.json", "as": "backend-smoke"},
            {"ref": "backend-review.json", "as": "backend-review"},
        ],
        "pipelines": [
            {"id": "backend-app", "file": "pipelines/backend-app.md"},
            {
                "id": "backend-chain",
                "file": "pipelines/backend-chain.md",
                "requires": {"services": ["chain-audit"]},
            },
        ],
        "entrypoints": [
            {
                "id": "backend",
                "pipeline": "backend-app",
                "title": "Backend smoke app",
                "workspace": "none",
                "inputs": {
                    "schema": {
                        "type": "object",
                        "required": ["objective"],
                        "additionalProperties": False,
                        "properties": {"objective": {"type": "string"}},
                    }
                },
                "defaults": {"budget": {"usd": 1, "wall_ms": 30000}},
            },
            {
                "id": "chain",
                "pipeline": "backend-chain",
                "title": "Backend smoke chain",
                "workspace": "none",
                "inputs": {
                    "schema": {
                        "type": "object",
                        "required": ["objective"],
                        "additionalProperties": False,
                        "properties": {"objective": {"type": "string"}},
                    }
                },
                "defaults": {"budget": {"usd": 1, "wall_ms": 30000}},
            }
        ],
        "io": {
            "inputs": [
                {
                    "entrypoint": "backend",
                    "connector": "webhook",
                    "path": "/apps/backend-smoke/runs",
                    "select": {"mode": "explicit", "pipeline": "backend-app"},
                }
            ],
            "outputs": [
                {
                    "entrypoint": "backend",
                    "sink": "web",
                    "on": "run.done",
                    "render": "result.summary + artifacts",
                }
            ],
        },
        "services": {"requires": ["backend-runner"], "wants": ["usage-meter"]},
    }
    (package / "app.json").write_text(json.dumps(app_manifest), encoding="utf-8")

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

    bad_egress_workload = {
        "provider": "fake",
        "template": "bad egress {{objective}}",
        "timeout_ms": 30000,
        "capability": {
            "name": "backend-smoke-bad-egress",
            "harnesses": ["fake"],
            "trust_tier": "T1",
            "egress_policy": [
                {
                    "dest": "api.provider.test",
                    "requires_approval": True,
                }
            ],
            "result": {"path": "RESULT"},
        },
    }
    (package / "bad-egress-smoke.json").write_text(
        json.dumps(bad_egress_workload), encoding="utf-8"
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

    remote_timeout_workload = {
        "provider": "fake",
        "template": "remote timeout {{objective}}",
        "timeout_ms": 50,
        "capability": {
            "name": "backend-smoke-remote-timeout",
            "harnesses": ["fake"],
            "trust_tier": "T2",
            "result": {"path": "RESULT"},
        },
    }
    (package / "remote-timeout-smoke.json").write_text(
        json.dumps(remote_timeout_workload), encoding="utf-8"
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

    paid_route_workload = {
        "provider": "fake",
        "template": "paid route {{objective}}",
        "seller_id": "py-seller",
        "timeout_ms": 30000,
        "pricing": {
            "type": "flat",
            "amount_usdc": "2.50",
            "take_rate_bps": 1500,
            "take_floor_usdc": "0.01",
            "payout_address": "usdc:seller:smoke",
        },
        "capability": {
            "name": "backend-smoke-paid-route",
            "harnesses": ["fake"],
            "trust_tier": "T1",
            "result": {"path": "RESULT"},
        },
    }
    (package / "paid-route-smoke.json").write_text(
        json.dumps(paid_route_workload), encoding="utf-8"
    )


def run_backend(
    workloads: pathlib.Path,
    apps: pathlib.Path,
    packages: pathlib.Path,
    traces: pathlib.Path,
    remote_endpoint: str,
) -> dict[str, Any]:
    code = """
{:ok, workload} = Mitos.Workload.fetch("backend-smoke")
capability_ref = workload.capability.id
{:ok, result} = Mitos.Workload.Runner.execute(workload, %{"objective" => "from python"})

api_conn =
  Plug.Test.conn(
    :post,
    "/workloads/backend-smoke/execute",
    Jason.encode!(%{
      schema_id: "gentix.provider_execution_request",
      schema_version: 1,
      task: %{
        task_id: "backend-smoke-api",
        objective: "from backend route",
        expected_output: "ship result"
      },
      capability: %{capability_id: capability_ref},
      request: %{}
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = api_conn.status == 200
api_body = Jason.decode!(api_conn.resp_body)
true = get_in(api_body, ["deliverable", "result", "signal"]) == "ship"
true = String.contains?(api_body["deliverable_summary"], "overlay=True")
true = String.starts_with?(api_body["execution_id"], "wl-")
false = String.contains?(inspect(api_body), "FAKE_FINAL")

{:ok, app_result} =
  Mitos.AppRunner.run(
    System.fetch_env!("MITOS_WORKLOADS_DIR"),
    "backend",
    %{"objective" => "from app"},
    task_id: "backend-app-smoke",
    trace_base_dir: System.fetch_env!("MITOS_TRACE_DIR"),
    tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"},
    source: "manual"
  )

true = app_result["status"] == "completed"
true = app_result["app_id"] |> String.starts_with?("app:sha256:")
true = app_result["app_run_id"] |> String.starts_with?("app-")
true = app_result["deliverable_summary"] |> String.contains?("overlay=True")
true = Enum.map(app_result["deliveries"], &get_in(&1, ["receipt", "sink"])) == ["web"]
true = Enum.map(app_result["deliveries"], &get_in(&1, ["receipt", "status"])) == ["sent"]
[app_step] = app_result["pipeline"]["steps"]
true = app_step["capability_id"] == "backend-smoke"
true = app_step["run_id"] |> String.starts_with?("wl-")

{:ok, app_chain_result} =
  Mitos.AppRunner.run(
    System.fetch_env!("MITOS_WORKLOADS_DIR"),
    "chain",
    %{"objective" => "from chain"},
    task_id: "backend-chain-smoke",
    trace_base_dir: System.fetch_env!("MITOS_TRACE_DIR"),
    tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"},
    source: "manual"
  )

true = app_chain_result["status"] == "completed"
true = app_chain_result["app_id"] == app_result["app_id"]
true = app_chain_result["app_run_id"] |> String.starts_with?("app-")
true = app_chain_result["pipeline_id"] == "backend-chain"
true = Enum.map(app_chain_result["deliveries"], &get_in(&1, ["receipt", "sink"])) == ["web"]
[app_chain_first_step, app_chain_second_step] = app_chain_result["pipeline"]["steps"]
true = app_chain_first_step["capability_id"] == "backend-smoke"
true = app_chain_second_step["capability_id"] == "backend-review"
true = app_chain_second_step["summary"] |> String.contains?("data_write_denied=True")
true = app_chain_first_step["run_id"] |> String.starts_with?("wl-")
true = app_chain_second_step["run_id"] |> String.starts_with?("wl-")

apps_conn = Plug.Test.conn(:get, "/apps") |> Mitos.Web.Router.call([])
true = apps_conn.status == 200
apps_body = Jason.decode!(apps_conn.resp_body)
true = apps_body["totals"]["apps"] == 1
true = hd(apps_body["apps"])["id"] == "backend-smoke-app"

app_get_conn = Plug.Test.conn(:get, "/apps/backend-smoke-app") |> Mitos.Web.Router.call([])
true = app_get_conn.status == 200
app_get_body = Jason.decode!(app_get_conn.resp_body)
true = get_in(app_get_body, ["app", "name"]) == "backend-smoke-app"
true = get_in(app_get_body, ["app", "app_id"]) |> String.starts_with?("app:sha256:")

service_units = [
  %{
    id: "backend-runner",
    scope: "project",
    schedule: %{type: "manual"},
    execution: %{type: "pipeline", pipeline: "backend-app"}
  },
  %{
    id: "usage-meter",
    scope: "project",
    schedule: %{type: "interval", every_ms: 60_000},
    execution: %{type: "pipeline", pipeline: "backend-app"}
  },
  %{
    id: "chain-audit",
    scope: "project",
    schedule: %{type: "manual"},
    execution: %{type: "pipeline", pipeline: "backend-chain"}
  }
]

app_activation_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-app/activations",
    Jason.encode!(%{units: service_units})
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_activation_conn.status == 200
app_activation_body = Jason.decode!(app_activation_conn.resp_body)["activation"]
true = app_activation_body["app_id"] == get_in(app_get_body, ["app", "app_id"])
true = get_in(app_activation_body, ["services", "target", "requires"]) == ["backend-runner"]
true = get_in(app_activation_body, ["services", "target", "wants"]) == ["usage-meter"]
true = get_in(app_activation_body, ["services", "missing_wants"]) == []
app_activation_active = get_in(app_activation_body, ["services", "active_set", "active"])
true = Map.has_key?(app_activation_active, "backend-runner")
true = Map.has_key?(app_activation_active, "usage-meter")
true = Map.has_key?(app_activation_active, "chain-audit")
true = "#{app_activation_body["app_id"]} (requires)" in app_activation_active["backend-runner"]["why"]
true = "#{app_activation_body["app_id"]} (wants)" in app_activation_active["usage-meter"]["why"]
true = "pipeline:backend-chain (requires)" in app_activation_active["chain-audit"]["why"]
true = get_in(app_activation_body, ["io", "defaults_applied?"]) == false
true = Enum.map(get_in(app_activation_body, ["io", "inputs"]), & &1["connector"]) == ["webhook"]
true = Enum.map(get_in(app_activation_body, ["io", "outputs"]), & &1["sink"]) == ["web"]

app_activation_missing_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-app/activations",
    Jason.encode!(%{units: []})
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_activation_missing_conn.status == 422
app_activation_missing_body = Jason.decode!(app_activation_missing_conn.resp_body)
true = app_activation_missing_body["error"] == "application activation failed"
true = app_activation_missing_body["detail"] == "missing required service backend-runner"

app_route_invalid_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-app/entrypoints/backend/runs",
    Jason.encode!(%{inputs: %{}})
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_route_invalid_conn.status == 422

app_route_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-app/entrypoints/backend/runs",
    Jason.encode!(%{
      inputs: %{objective: "from app route"},
      task: %{task_id: "backend-app-route"},
      tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"}
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_route_conn.status == 200
app_route_body = Jason.decode!(app_route_conn.resp_body)
true = app_route_body["status"] == "completed"
true = app_route_body["app_id"] == get_in(app_get_body, ["app", "app_id"])
true = app_route_body["app_run_id"] |> String.starts_with?("app-")
true = app_route_body["deliverable_summary"] |> String.contains?("overlay=True")
true = Enum.map(app_route_body["deliveries"], &get_in(&1, ["receipt", "sink"])) == ["web"]
[app_route_step] = app_route_body["pipeline"]["steps"]
true = app_route_step["capability_id"] == "backend-smoke"
true = app_route_step["run_id"] |> String.starts_with?("wl-")

app_signal_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-app/connectors/webhook/signals",
    Jason.encode!(%{
      path: "/apps/backend-smoke/runs",
      delivery_id: "py-webhook-1",
      reply_ref: "py-thread-1",
      inputs: %{objective: "from webhook app route"},
      task: %{task_id: "backend-app-webhook"},
      tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"}
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_signal_conn.status == 200
app_signal_body = Jason.decode!(app_signal_conn.resp_body)
true = app_signal_body["status"] == "completed"
true = app_signal_body["connector"] == "webhook"
true = app_signal_body["entrypoint"] == "backend"
true = app_signal_body["task_id"] == "backend-app-webhook"
true = get_in(app_signal_body, ["io_binding", "path"]) == "/apps/backend-smoke/runs"
true = app_signal_body["app_id"] == get_in(app_get_body, ["app", "app_id"])
true = app_signal_body["app_run_id"] |> String.starts_with?("app-")
true = app_signal_body["deliverable_summary"] |> String.contains?("overlay=True")
true = Enum.map(app_signal_body["deliveries"], &get_in(&1, ["receipt", "sink"])) == ["web"]
[app_signal_step] = app_signal_body["pipeline"]["steps"]
true = app_signal_step["capability_id"] == "backend-smoke"
true = app_signal_step["run_id"] |> String.starts_with?("wl-")

app_signal_duplicate_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-app/connectors/webhook/signals",
    Jason.encode!(%{
      path: "/apps/backend-smoke/runs",
      delivery_id: "py-webhook-1",
      reply_ref: "py-thread-redelivery",
      inputs: %{objective: "from webhook app route redelivery"},
      task: %{task_id: "backend-app-webhook-redelivery"},
      tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"}
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_signal_duplicate_conn.status == 200
app_signal_duplicate_body = Jason.decode!(app_signal_duplicate_conn.resp_body)
true = app_signal_duplicate_body["duplicate?"] == true
true = app_signal_duplicate_body["app_run_id"] == app_signal_body["app_run_id"]
true = app_signal_duplicate_body["task_id"] == app_signal_body["task_id"]
true = app_signal_duplicate_body["deliverable_summary"] == app_signal_body["deliverable_summary"]
true = not String.contains?(app_signal_duplicate_body["deliverable_summary"], "redelivery")

app_signal_redrive_conn =
  Plug.Test.conn(
    :post,
    "/app-runs/#{app_signal_body["app_run_id"]}/deliveries/redrive",
    Jason.encode!(%{})
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_signal_redrive_conn.status == 200
app_signal_redrive_body = Jason.decode!(app_signal_redrive_conn.resp_body)["redrive"]
true = app_signal_redrive_body["app_run_id"] == app_signal_body["app_run_id"]
true = app_signal_redrive_body["task_id"] == app_signal_body["task_id"]
true = app_signal_redrive_body["idempotent?"] == true
true = app_signal_redrive_body["effects_appended"] == 0
true = app_signal_redrive_body["new_deliveries"] == 0
true = Enum.map(app_signal_redrive_body["deliveries"], &get_in(&1, ["receipt", "sink"])) == ["web"]

app_package_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-app/packages",
    Jason.encode!(%{package_id: "backend-smoke-release", tag: "backend-smoke-1"})
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_package_conn.status == 201
app_package_body = Jason.decode!(app_package_conn.resp_body)["package"]
true = app_package_body["package_id"] == "backend-smoke-release"
true = app_package_body["app_id"] |> String.starts_with?("app:sha256:")
true = app_package_body["app_id"] != app_route_body["app_id"]
true = get_in(app_package_body, ["app", "app_id"]) == app_route_body["app_id"]
true = app_package_body["manifest_digest"] |> String.starts_with?("sha256:")
true = File.regular?(Path.join(app_package_body["layout_dir"], "oci-layout"))
true = File.regular?(Path.join(app_package_body["layout_dir"], "index.json"))

app_install_conn =
  Plug.Test.conn(
    :post,
    "/app-packages/backend-smoke-release/install",
    Jason.encode!(%{
      install_id: "backend-smoke-installed",
      tag: "backend-smoke-1",
      app_id: app_package_body["app_id"],
      available_connectors: ["webhook"],
      available_sinks: ["reply"],
      io: %{
        inputs: [
          %{
            entrypoint: "backend",
            connector: "webhook",
            path: "/apps/backend-smoke/installed-runs",
            select: %{mode: "explicit", pipeline: "backend-app"}
          }
        ],
        outputs: [
          %{
            entrypoint: "backend",
            sink: "reply",
            on: "run.done",
            reply_to: "input.reply_ref"
          }
        ]
      }
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = app_install_conn.status == 201
app_install_body = Jason.decode!(app_install_conn.resp_body)["install"]
true = app_install_body["package_id"] == "backend-smoke-release"
true = app_install_body["install_id"] == "backend-smoke-installed"
true = app_install_body["app_id"] == app_package_body["app_id"]
true = app_install_body["app_dir"] |> String.ends_with?("/backend-smoke-installed")
true = get_in(app_install_body, ["app", "app_id"]) == app_package_body["app_id"]
true =
  get_in(app_install_body, ["app", "capabilities"])
  |> Enum.all?(fn %{"ref" => ref} -> String.starts_with?(ref, "cap:sha256:") end)
true = app_install_body["bindings_applied?"] == true
true = get_in(app_install_body, ["app", "io", "inputs"]) |> hd() |> Map.get("path") == "/apps/backend-smoke/installed-runs"
true = get_in(app_install_body, ["app", "io", "outputs"]) |> hd() |> Map.get("sink") == "reply"

installed_app_run_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-installed/entrypoints/backend/runs",
    Jason.encode!(%{
      inputs: %{objective: "from installed app"},
      reply_ref: "py-installed-thread-1",
      task: %{task_id: "backend-installed-route"},
      tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"}
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = installed_app_run_conn.status == 200
installed_app_run_body = Jason.decode!(installed_app_run_conn.resp_body)
true = installed_app_run_body["app_id"] == app_package_body["app_id"]
true = installed_app_run_body["status"] == "completed"
true = installed_app_run_body["deliverable_summary"] |> String.contains?("overlay=True")
true = Enum.map(installed_app_run_body["deliveries"], &get_in(&1, ["receipt", "sink"])) == ["reply"]
true = Enum.map(installed_app_run_body["deliveries"], &get_in(&1, ["receipt", "destination"])) == ["py-installed-thread-1"]
[installed_app_step] = installed_app_run_body["pipeline"]["steps"]
true = installed_app_step["capability_id"] == "backend-smoke"
true = installed_app_step["run_id"] |> String.starts_with?("wl-")

installed_app_signal_conn =
  Plug.Test.conn(
    :post,
    "/apps/backend-smoke-installed/connectors/webhook/signals",
    Jason.encode!(%{
      path: "/apps/backend-smoke/installed-runs",
      delivery_id: "py-installed-webhook-1",
      reply_ref: "py-installed-thread-2",
      inputs: %{objective: "from installed connector"},
      task: %{task_id: "backend-installed-webhook"},
      tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"}
    })
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = installed_app_signal_conn.status == 200
installed_app_signal_body = Jason.decode!(installed_app_signal_conn.resp_body)
true = installed_app_signal_body["app_id"] == app_package_body["app_id"]
true = installed_app_signal_body["status"] == "completed"
true = installed_app_signal_body["task_id"] == "backend-installed-webhook"
true = get_in(installed_app_signal_body, ["io_binding", "path"]) == "/apps/backend-smoke/installed-runs"
true = Enum.map(installed_app_signal_body["deliveries"], &get_in(&1, ["receipt", "sink"])) == ["reply"]
true = Enum.map(installed_app_signal_body["deliveries"], &get_in(&1, ["receipt", "destination"])) == ["py-installed-thread-2"]
[installed_app_signal_step] = installed_app_signal_body["pipeline"]["steps"]
true = installed_app_signal_step["capability_id"] == "backend-smoke"
true = installed_app_signal_step["run_id"] |> String.starts_with?("wl-")

skill_path = Path.join(System.fetch_env!("MITOS_WORKLOADS_DIR"), "skill.md")
File.write!(skill_path, File.read!(skill_path) <> "\\n<!-- identity smoke change -->\\n")
{:ok, changed_identity_workload} = Mitos.Workload.fetch("backend-smoke")
true = changed_identity_workload.capability.id != capability_ref

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

{:error,
 {:invalid_workload,
  "invalid capability manifest: egress_policy entries require credential or credential_id"}} =
  Mitos.Workload.fetch("bad-egress-smoke")

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

{:ok, remote_timeout_workload} = Mitos.Workload.fetch("remote-timeout-smoke")
{:error, {:timeout, remote_timeout_run_id}} =
  Mitos.Workload.Runner.execute(remote_timeout_workload, %{"objective" => "from python"})
remote_timeout_events = Mitos.Trace.read_events(remote_timeout_run_id)
remote_timeout_fold = Mitos.WorkPool.fold_events(remote_timeout_events)
[remote_timeout_task | _] = Map.values(remote_timeout_fold["tasks"])

paid_request = %{
  schema_id: "gentix.provider_execution_request",
  schema_version: 1,
  tenant: %{org: "py-buyer", project: "backend-smoke", actor: "python"},
  buyer_workspace: %{
    snapshot: "snap://buyer/backend-smoke",
    mount: "/workspace",
    ro: true,
    sha256: "sha256:buyer-backend-smoke"
  },
  task: %{task_id: "paid-route-smoke", objective: "paid backend route"},
  request: %{}
}

paid_quote_conn =
  Plug.Test.conn(
    :post,
    "/capabilities/paid-route-smoke/runs",
    Jason.encode!(paid_request)
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Mitos.Web.Router.call([])

true = paid_quote_conn.status == 402
paid_quote = Jason.decode!(paid_quote_conn.resp_body)

paid_payment =
  Jason.encode!(%{
    quote_id: paid_quote["quote_id"],
    amount_usdc: paid_quote["amount_usdc"],
    currency: "USDC",
    pay_to: paid_quote["pay_to"],
    status: "settled",
    settlement_tx: "usdc-tx-python-smoke"
  })

paid_conn =
  Plug.Test.conn(
    :post,
    "/capabilities/paid-route-smoke/runs",
    Jason.encode!(paid_request)
  )
  |> Plug.Conn.put_req_header("content-type", "application/json")
  |> Plug.Conn.put_req_header("x-payment", paid_payment)
  |> Mitos.Web.Router.call([])

true = paid_conn.status == 200
paid_body = Jason.decode!(paid_conn.resp_body)
true = paid_body["settlement"]["status"] == "settled"
true = paid_body["settlement"]["settlement_tx"] == "usdc-tx-python-smoke"
true = get_in(paid_body, ["deliverable", "result", "signal"]) == "ship"
true = get_in(paid_body, ["evidence", "proof", "metering", "budget_charged_units"]) == 1

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
  api_route: %{
    run_id: api_body["execution_id"],
    status: api_conn.status,
    signal: get_in(api_body, ["deliverable", "result", "signal"]),
    summary: api_body["deliverable_summary"],
    proof_hash: get_in(api_body, ["evidence", "proof", "effect_log_sha256"])
  },
  app_run: %{
    app_id: app_result["app_id"],
    app_run_id: app_result["app_run_id"],
    task_id: app_result["task_id"],
    entrypoint: app_result["entrypoint"],
    pipeline_id: app_result["pipeline_id"],
    status: app_result["status"],
    summary: app_result["deliverable_summary"],
    deliveries: Enum.map(app_result["deliveries"], &get_in(&1, ["receipt", "sink"])),
    step_run_id: app_step["run_id"],
    step_capability_id: app_step["capability_id"]
  },
  app_chain: %{
    app_id: app_chain_result["app_id"],
    app_run_id: app_chain_result["app_run_id"],
    task_id: app_chain_result["task_id"],
    entrypoint: app_chain_result["entrypoint"],
    pipeline_id: app_chain_result["pipeline_id"],
    status: app_chain_result["status"],
    summary: app_chain_result["deliverable_summary"],
    deliveries: Enum.map(app_chain_result["deliveries"], &get_in(&1, ["receipt", "sink"])),
    first_step_run_id: app_chain_first_step["run_id"],
    second_step_run_id: app_chain_second_step["run_id"],
    first_step_capability_id: app_chain_first_step["capability_id"],
    second_step_capability_id: app_chain_second_step["capability_id"]
  },
  app_route: %{
    list_status: apps_conn.status,
    get_status: app_get_conn.status,
    invalid_status: app_route_invalid_conn.status,
    run_status: app_route_conn.status,
    app_id: app_route_body["app_id"],
    app_run_id: app_route_body["app_run_id"],
    task_id: app_route_body["task_id"],
    entrypoint: app_route_body["entrypoint"],
    pipeline_id: app_route_body["pipeline_id"],
    status: app_route_body["status"],
    summary: app_route_body["deliverable_summary"],
    deliveries: Enum.map(app_route_body["deliveries"], &get_in(&1, ["receipt", "sink"])),
    step_run_id: app_route_step["run_id"],
    step_capability_id: app_route_step["capability_id"]
  },
  app_signal: %{
    connector: app_signal_body["connector"],
    app_id: app_signal_body["app_id"],
    app_run_id: app_signal_body["app_run_id"],
    task_id: app_signal_body["task_id"],
    entrypoint: app_signal_body["entrypoint"],
    pipeline_id: app_signal_body["pipeline_id"],
    status: app_signal_body["status"],
    summary: app_signal_body["deliverable_summary"],
    deliveries: Enum.map(app_signal_body["deliveries"], &get_in(&1, ["receipt", "sink"])),
    binding_path: get_in(app_signal_body, ["io_binding", "path"]),
    step_run_id: app_signal_step["run_id"],
    step_capability_id: app_signal_step["capability_id"],
    redelivery_duplicate: app_signal_duplicate_body["duplicate?"],
    redelivery_app_run_id: app_signal_duplicate_body["app_run_id"],
    redelivery_task_id: app_signal_duplicate_body["task_id"],
    redelivery_summary: app_signal_duplicate_body["deliverable_summary"],
    redrive_idempotent: app_signal_redrive_body["idempotent?"],
    redrive_effects_appended: app_signal_redrive_body["effects_appended"],
    redrive_new_deliveries: app_signal_redrive_body["new_deliveries"]
  },
  app_activation: %{
    status: app_activation_conn.status,
    missing_status: app_activation_missing_conn.status,
    app_id: app_activation_body["app_id"],
    active_services: app_activation_active |> Map.keys() |> Enum.sort(),
    missing_wants: get_in(app_activation_body, ["services", "missing_wants"]),
    pipeline_requires: get_in(app_activation_body, ["services", "pipeline_requires"]),
    input_connectors:
      Enum.map(get_in(app_activation_body, ["io", "inputs"]), fn binding ->
        binding["connector"]
      end),
    output_sinks:
      Enum.map(get_in(app_activation_body, ["io", "outputs"]), fn binding ->
        binding["sink"]
      end),
    defaults_applied: get_in(app_activation_body, ["io", "defaults_applied?"]),
    missing_detail: app_activation_missing_body["detail"]
  },
  app_package: %{
    status: app_package_conn.status,
    package_id: app_package_body["package_id"],
    app_id: app_package_body["app_id"],
    dev_app_id: get_in(app_package_body, ["app", "app_id"]),
    manifest_digest: app_package_body["manifest_digest"],
    layout_dir: app_package_body["layout_dir"],
    app_name: get_in(app_package_body, ["app", "name"])
  },
  app_install: %{
    status: app_install_conn.status,
    package_id: app_install_body["package_id"],
    install_id: app_install_body["install_id"],
    app_id: app_install_body["app_id"],
    app_dir: app_install_body["app_dir"],
    bindings_applied: app_install_body["bindings_applied?"],
    input_path: get_in(app_install_body, ["app", "io", "inputs"]) |> hd() |> Map.get("path"),
    output_sink: get_in(app_install_body, ["app", "io", "outputs"]) |> hd() |> Map.get("sink"),
    frozen_refs:
      Enum.map(get_in(app_install_body, ["app", "capabilities"]), fn %{"ref" => ref} -> ref end)
  },
  installed_app_run: %{
    app_id: installed_app_run_body["app_id"],
    app_run_id: installed_app_run_body["app_run_id"],
    task_id: installed_app_run_body["task_id"],
    entrypoint: installed_app_run_body["entrypoint"],
    pipeline_id: installed_app_run_body["pipeline_id"],
    status: installed_app_run_body["status"],
    summary: installed_app_run_body["deliverable_summary"],
    deliveries: Enum.map(installed_app_run_body["deliveries"], &get_in(&1, ["receipt", "sink"])),
    destinations:
      Enum.map(installed_app_run_body["deliveries"], &get_in(&1, ["receipt", "destination"])),
    step_run_id: installed_app_step["run_id"],
    step_capability_id: installed_app_step["capability_id"]
  },
  installed_app_signal: %{
    app_id: installed_app_signal_body["app_id"],
    app_run_id: installed_app_signal_body["app_run_id"],
    task_id: installed_app_signal_body["task_id"],
    entrypoint: installed_app_signal_body["entrypoint"],
    pipeline_id: installed_app_signal_body["pipeline_id"],
    status: installed_app_signal_body["status"],
    summary: installed_app_signal_body["deliverable_summary"],
    deliveries:
      Enum.map(installed_app_signal_body["deliveries"], &get_in(&1, ["receipt", "sink"])),
    destinations:
      Enum.map(installed_app_signal_body["deliveries"], &get_in(&1, ["receipt", "destination"])),
    binding_path: get_in(installed_app_signal_body, ["io_binding", "path"]),
    step_run_id: installed_app_signal_step["run_id"],
    step_capability_id: installed_app_signal_step["capability_id"]
  },
  capability_ref: capability_ref,
  capability_identity_changes: true,
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
  paid_route: %{
    quote_status: paid_quote_conn.status,
    run_status: paid_conn.status,
    run_id: paid_body["execution_id"],
    signal: get_in(paid_body, ["deliverable", "result", "signal"]),
    settlement_status: paid_body["settlement"]["status"],
    settlement_tx: paid_body["settlement"]["settlement_tx"],
    budget_charged_units: get_in(paid_body, ["evidence", "proof", "metering", "budget_charged_units"]),
    buyer_charge_usdc: paid_body["settlement"]["buyer_charge_usdc"]
  },
  remote_timeout: %{
    run_id: remote_timeout_run_id,
    task_status: remote_timeout_task["status"],
    status_reason: remote_timeout_task["status_reason"],
    lease_present:
      Application.get_env(:mitos, :key_vault_leases, %{})
      |> Map.has_key?(remote_timeout_run_id)
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
            "MITOS_APPS_DIR": str(apps),
            "MITOS_APP_PACKAGES_DIR": str(packages),
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

    event_cursors = [req["body"]["cursor"] for req in requests if req["path"].endswith("/events")]
    assert event_cursors[:2] == ["0", "1"]
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


def verify_api_route(api_route: dict[str, Any]) -> None:
    assert api_route["status"] == 200
    assert api_route["run_id"].startswith("wl-")
    assert api_route["signal"] == "ship"
    assert "overlay=True" in api_route["summary"]
    assert api_route["proof_hash"].startswith("sha256:")


def verify_app_run(app_run: dict[str, Any]) -> None:
    assert app_run["app_id"].startswith("app:sha256:")
    assert app_run["app_run_id"].startswith("app-")
    assert app_run["task_id"] == "backend-app-smoke"
    assert app_run["entrypoint"] == "backend"
    assert app_run["pipeline_id"] == "backend-app"
    assert app_run["status"] == "completed"
    assert "overlay=True" in app_run["summary"]
    assert app_run["deliveries"] == ["web"]
    assert app_run["step_run_id"].startswith("wl-")
    assert app_run["step_capability_id"] == "backend-smoke"


def verify_app_chain(app_chain: dict[str, Any]) -> None:
    assert app_chain["app_id"].startswith("app:sha256:")
    assert app_chain["app_run_id"].startswith("app-")
    assert app_chain["task_id"] == "backend-chain-smoke"
    assert app_chain["entrypoint"] == "chain"
    assert app_chain["pipeline_id"] == "backend-chain"
    assert app_chain["status"] == "completed"
    assert "data_write_denied=True" in app_chain["summary"]
    assert app_chain["deliveries"] == ["web"]
    assert app_chain["first_step_run_id"].startswith("wl-")
    assert app_chain["second_step_run_id"].startswith("wl-")
    assert app_chain["first_step_capability_id"] == "backend-smoke"
    assert app_chain["second_step_capability_id"] == "backend-review"


def verify_app_route(app_route: dict[str, Any]) -> None:
    assert app_route["list_status"] == 200
    assert app_route["get_status"] == 200
    assert app_route["invalid_status"] == 422
    assert app_route["run_status"] == 200
    assert app_route["app_id"].startswith("app:sha256:")
    assert app_route["app_run_id"].startswith("app-")
    assert app_route["task_id"] == "backend-app-route"
    assert app_route["entrypoint"] == "backend"
    assert app_route["pipeline_id"] == "backend-app"
    assert app_route["status"] == "completed"
    assert "overlay=True" in app_route["summary"]
    assert app_route["deliveries"] == ["web"]
    assert app_route["step_run_id"].startswith("wl-")
    assert app_route["step_capability_id"] == "backend-smoke"


def verify_app_signal(app_signal: dict[str, Any]) -> None:
    assert app_signal["connector"] == "webhook"
    assert app_signal["app_id"].startswith("app:sha256:")
    assert app_signal["app_run_id"].startswith("app-")
    assert app_signal["task_id"] == "backend-app-webhook"
    assert app_signal["entrypoint"] == "backend"
    assert app_signal["pipeline_id"] == "backend-app"
    assert app_signal["status"] == "completed"
    assert "overlay=True" in app_signal["summary"]
    assert app_signal["deliveries"] == ["web"]
    assert app_signal["binding_path"] == "/apps/backend-smoke/runs"
    assert app_signal["step_run_id"].startswith("wl-")
    assert app_signal["step_capability_id"] == "backend-smoke"
    assert app_signal["redelivery_duplicate"] is True
    assert app_signal["redelivery_app_run_id"] == app_signal["app_run_id"]
    assert app_signal["redelivery_task_id"] == app_signal["task_id"]
    assert app_signal["redelivery_summary"] == app_signal["summary"]
    assert app_signal["redrive_idempotent"] is True
    assert app_signal["redrive_effects_appended"] == 0
    assert app_signal["redrive_new_deliveries"] == 0


def verify_app_activation(app_activation: dict[str, Any]) -> None:
    assert app_activation["status"] == 200
    assert app_activation["missing_status"] == 422
    assert app_activation["app_id"].startswith("app:sha256:")
    assert app_activation["active_services"] == [
        "backend-runner",
        "chain-audit",
        "usage-meter",
    ]
    assert app_activation["missing_wants"] == []
    assert app_activation["pipeline_requires"] == [
        {"pipeline": "backend-chain", "services": ["chain-audit"], "targets": []}
    ]
    assert app_activation["input_connectors"] == ["webhook"]
    assert app_activation["output_sinks"] == ["web"]
    assert app_activation["defaults_applied"] is False
    assert app_activation["missing_detail"] == "missing required service backend-runner"


def verify_app_package(app_package: dict[str, Any]) -> None:
    assert app_package["status"] == 201
    assert app_package["package_id"] == "backend-smoke-release"
    assert app_package["app_id"].startswith("app:sha256:")
    assert app_package["dev_app_id"].startswith("app:sha256:")
    assert app_package["app_id"] != app_package["dev_app_id"]
    assert app_package["manifest_digest"].startswith("sha256:")
    assert app_package["app_name"] == "backend-smoke-app"

    layout = pathlib.Path(app_package["layout_dir"])
    assert json.loads((layout / "oci-layout").read_text(encoding="utf-8")) == {
        "imageLayoutVersion": "1.0.0"
    }

    index = json.loads((layout / "index.json").read_text(encoding="utf-8"))
    assert len(index["manifests"]) == 1
    manifest = index["manifests"][0]
    assert manifest["artifactType"] == "application/vnd.mitos.application.v1+json"
    assert manifest["annotations"]["dev.mitos.app_id"] == app_package["app_id"]
    assert manifest["annotations"]["org.opencontainers.image.ref.name"] == "backend-smoke-1"


def verify_app_install(app_install: dict[str, Any]) -> None:
    assert app_install["status"] == 201
    assert app_install["package_id"] == "backend-smoke-release"
    assert app_install["install_id"] == "backend-smoke-installed"
    assert app_install["app_id"].startswith("app:sha256:")
    assert app_install["app_dir"].endswith("/backend-smoke-installed")
    assert app_install["bindings_applied"] is True
    assert app_install["input_path"] == "/apps/backend-smoke/installed-runs"
    assert app_install["output_sink"] == "reply"
    assert app_install["frozen_refs"]
    assert all(ref.startswith("cap:sha256:") for ref in app_install["frozen_refs"])


def verify_installed_app_run(installed_app_run: dict[str, Any]) -> None:
    assert installed_app_run["app_id"].startswith("app:sha256:")
    assert installed_app_run["app_run_id"].startswith("app-")
    assert installed_app_run["task_id"] == "backend-installed-route"
    assert installed_app_run["entrypoint"] == "backend"
    assert installed_app_run["pipeline_id"] == "backend-app"
    assert installed_app_run["status"] == "completed"
    assert "overlay=True" in installed_app_run["summary"]
    assert installed_app_run["deliveries"] == ["reply"]
    assert installed_app_run["destinations"] == ["py-installed-thread-1"]
    assert installed_app_run["step_run_id"].startswith("wl-")
    assert installed_app_run["step_capability_id"] == "backend-smoke"


def verify_installed_app_signal(installed_app_signal: dict[str, Any]) -> None:
    assert installed_app_signal["app_id"].startswith("app:sha256:")
    assert installed_app_signal["app_run_id"].startswith("app-")
    assert installed_app_signal["task_id"] == "backend-installed-webhook"
    assert installed_app_signal["entrypoint"] == "backend"
    assert installed_app_signal["pipeline_id"] == "backend-app"
    assert installed_app_signal["status"] == "completed"
    assert "overlay=True" in installed_app_signal["summary"]
    assert installed_app_signal["deliveries"] == ["reply"]
    assert installed_app_signal["destinations"] == ["py-installed-thread-2"]
    assert installed_app_signal["binding_path"] == "/apps/backend-smoke/installed-runs"
    assert installed_app_signal["step_run_id"].startswith("wl-")
    assert installed_app_signal["step_capability_id"] == "backend-smoke"


def verify_app_trace(traces: pathlib.Path, app_run: dict[str, Any]) -> None:
    events = read_jsonl(traces / ".runs" / app_run["app_run_id"] / "events.jsonl")
    task_done_seq = None
    expected_sinks = app_run.get("deliveries") or ["web"]

    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "signal_received"
        and event.get("ref", {}).get("task_id") == app_run["task_id"]
        and event.get("payload", {}).get("intake", {}).get("input_keys") == ["objective"]
        for event in events
    )
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "task_enqueued"
        and event.get("payload", {}).get("task", {}).get("provenance", {}).get("app_id")
        == app_run["app_id"]
        and event.get("payload", {}).get("task", {}).get("provenance", {}).get("entrypoint")
        == app_run["entrypoint"]
        for event in events
    )

    for event in events:
        if (
            event.get("kind") == "effect"
            and event.get("type") == "task_done"
            and event.get("ref", {}).get("task_id") == app_run["task_id"]
        ):
            task_done_seq = event.get("seq")
            break

    assert isinstance(task_done_seq, int)

    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "delivery_attempted"
        and event.get("ref", {}).get("task_id") == app_run["task_id"]
        and event.get("ref", {}).get("sink") in expected_sinks
        and event.get("caused_by") == task_done_seq
        for event in events
    )
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "delivery_sent"
        and event.get("ref", {}).get("task_id") == app_run["task_id"]
        and event.get("ref", {}).get("sink") in expected_sinks
        and event.get("caused_by") == task_done_seq
        and event.get("payload", {}).get("receipt", {}).get("status") == "sent"
        for event in events
    )
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "task_done"
        and event.get("ref", {}).get("task_id") == app_run["task_id"]
        and event.get("payload", {}).get("status") == "done"
        and event.get("payload", {}).get("result", {}).get("status") == "completed"
        for event in events
    )


def verify_app_signal_trace(traces: pathlib.Path, app_signal: dict[str, Any]) -> None:
    events = read_jsonl(traces / ".runs" / app_signal["app_run_id"] / "events.jsonl")

    signal_events = [
        event
        for event in events
        if event.get("kind") == "effect"
        and event.get("type") == "signal_received"
        and event.get("ref", {}).get("connector") == "webhook"
        and event.get("ref", {}).get("idempotency_key") == "py-webhook-1"
    ]

    assert len(signal_events) == 1
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "signal_received"
        and event.get("ref", {}).get("connector") == "webhook"
        and event.get("ref", {}).get("idempotency_key") == "py-webhook-1"
        and event.get("ref", {}).get("task_id") == app_signal["task_id"]
        and event.get("payload", {}).get("intake", {}).get("reply_ref") == "py-thread-1"
        for event in events
    )
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "task_enqueued"
        and event.get("payload", {}).get("task", {}).get("source") == "connector:webhook"
        for event in events
    )
    assert not any(
        event.get("kind") == "effect"
        and event.get("type") == "task_enqueued"
        and event.get("payload", {}).get("task", {}).get("id")
        == "backend-app-webhook-redelivery"
        for event in events
    )

    delivery_sent_events = [
        event
        for event in events
        if event.get("kind") == "effect"
        and event.get("type") == "delivery_sent"
        and event.get("ref", {}).get("task_id") == app_signal["task_id"]
    ]
    assert len(delivery_sent_events) == 1


def verify_paid_route(paid_route: dict[str, Any], requests: list[dict[str, Any]]) -> None:
    assert paid_route["quote_status"] == 402
    assert paid_route["run_status"] == 200
    assert paid_route["run_id"].startswith("wl-")
    assert paid_route["signal"] == "ship"
    assert paid_route["settlement_status"] == "settled"
    assert paid_route["settlement_tx"] == "usdc-tx-python-smoke"
    assert paid_route["budget_charged_units"] == 1
    assert paid_route["buyer_charge_usdc"] == "2.500000"

    paid_run_requests = [
        req
        for req in requests
        if req["method"] == "POST"
        and req["path"] == "/runs"
        and req["body"]["launch_spec"].get("workspace", {}).get("snapshot")
        == "snap://buyer/backend-smoke"
    ]
    assert len(paid_run_requests) == 1

    paid_launch = paid_run_requests[0]["body"]["launch_spec"]
    assert paid_launch["trust_tier"] == "T2"
    assert paid_launch["workspace"] == {
        "snapshot": "snap://buyer/backend-smoke",
        "mount": "/workspace",
        "ro": True,
        "sha256": "sha256:buyer-backend-smoke",
    }
    assert paid_launch["posture"]["net_mode"] == "deny_all_except_broker"
    assert paid_launch["posture"]["egress"] == "broker"
    assert paid_run_requests[0]["body"]["env"] == {}


def verify_paid_trace(traces: pathlib.Path, run_id: str) -> None:
    events = read_jsonl(traces / ".runs" / run_id / "events.jsonl")
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "budget_charged"
        and event.get("ref", {}).get("settlement_tx") == "usdc-tx-python-smoke"
        for event in events
    )
    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "capability_finished"
        and event.get("payload", {}).get("result", {}).get("signal") == "ship"
        for event in events
    )


def verify_remote_timeout(timeout: dict[str, Any], requests: list[dict[str, Any]]) -> None:
    assert timeout["run_id"].startswith("wl-")
    assert timeout["task_status"] == "failed"
    assert "timeout" in timeout["status_reason"]
    assert timeout["lease_present"] is False

    timeout_run_requests = [
        req
        for req in requests
        if req["method"] == "POST"
        and req["path"] == "/runs"
        and req["body"]["launch_spec"].get("task") == "remote timeout from python"
    ]
    assert len(timeout_run_requests) == 1
    assert timeout_run_requests[0]["body"]["env"] == {}

    launch_spec = timeout_run_requests[0]["body"]["launch_spec"]
    assert launch_spec["trust_tier"] == "T2"
    assert launch_spec["posture"]["net_mode"] == "deny_all_except_broker"
    assert launch_spec["posture"]["egress"] == "broker"

    assert any(
        req["method"] == "POST" and req["path"] == "/runs/remote-timeout-1/events"
        for req in requests
    )

    control = find_request(requests, "POST", "/runs/remote-timeout-1/control")
    assert control["body"]["command"] == {
        "cmd": "steer",
        "interaction": "interrupt",
        "reason": "timeout",
        "timeout_ms": 50,
    }
    assert find_request(requests, "POST", "/runs/remote-timeout-1/harvest")
    assert find_request(requests, "POST", "/runs/remote-timeout-1/stop")


def verify_remote_timeout_trace(traces: pathlib.Path, run_id: str) -> None:
    events = read_jsonl(traces / ".runs" / run_id / "events.jsonl")

    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "capability_interrupted"
        and event.get("payload", {}).get("reason") == "timeout"
        and event.get("payload", {}).get("timeout_ms") == 50
        and event.get("payload", {}).get("control", {}).get("status") == "sent"
        for event in events
    )

    assert any(
        event.get("kind") == "effect"
        and event.get("type") == "capability_finished"
        and event.get("payload", {}).get("status") == "timeout"
        and event.get("payload", {}).get("exit_code") is None
        and event.get("payload", {}).get("harvest", {}).get("result", {}).get("signal")
        == "ship"
        and event.get("payload", {}).get("harvest", {}).get("result", {}).get("summary")
        == "partial timeout smoke"
        for event in events
    )


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
                try:
                    self.wfile.write(encoded)
                except BrokenPipeError:
                    return

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
            if body.get("launch_spec", {}).get("task") == "remote timeout from python":
                return (
                    200,
                    {
                        "run_id": "remote-timeout-1",
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
                        "stream_max_empty_polls": 10000,
                    },
                )

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

        if path == "/runs/remote-timeout-1/events":
            return (
                200,
                {
                    "events": [],
                    "next_cursor": body.get("cursor") or "0",
                    "done": False,
                },
            )

        if path == "/runs/remote-timeout-1/control":
            return (200, {"ok": True})

        if path == "/runs/remote-timeout-1/harvest":
            return (
                200,
                {
                    "harvest": {
                        "diff": {"empty?": False, "files_changed": ["RESULT"]},
                        "result": {
                            "path": "RESULT",
                            "signal": "ship",
                            "summary": "partial timeout smoke",
                        },
                        "artifacts": [
                            {"kind": "file", "path": "RESULT", "sha256": "sha256:partial"}
                        ],
                    }
                },
            )

        if path == "/runs/remote-timeout-1/stop":
            return (200, {"ok": True})

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
