import Config

# Per-instance port overrides (env vars; `serve --http-port/--broker-port`
# set these before boot). Defaults keep the documented :4646 / :4647.
http_port =
  case System.get_env("MITOS_HTTP_PORT") do
    nil -> 4646
    s -> String.to_integer(s)
  end

broker_port =
  case System.get_env("MITOS_BROKER_PORT") do
    nil -> 4647
    s -> String.to_integer(s)
  end

model_proxy_port =
  case System.get_env("MITOS_MODEL_PROXY_PORT") do
    nil -> 4648
    s -> String.to_integer(s)
  end

mitm_proxy_port =
  case System.get_env("MITOS_MITM_PROXY_PORT") do
    nil -> 0
    s -> String.to_integer(s)
  end

config :mitos,
  # Root for all durable state: event logs, sandboxes, vault, pools.
  data_dir: Path.expand("~/.local/share/mitos"),
  # Registered sandbox providers, consulted by the launch policy compiler (05 §4).
  # Each provider module implements Mitos.SandboxProvider.
  providers: [Mitos.Provider.Local, Mitos.Provider.Nucleus],
  # Registered harness adapters (01 §8.6).
  harnesses: %{
    "fake" => Mitos.Harness.Fake,
    "claude-code" => Mitos.Harness.ClaudeCode,
    "codex" => Mitos.Harness.Codex,
    "opencode" => Mitos.Harness.Opencode,
    "pi" => Mitos.Harness.Pi
  },
  # Orchestrator policy for runs that don't specify one. The default is the
  # deterministic Linear policy; switch to Agentic for non-linear pipelines.
  orchestrator_policy: Mitos.Orchestrator.Policy.Linear,
  # Default harness when a capability declares several (05 §2 prefers this if
  # it is in the capability's compatible set).
  default_harness: "pi",
  # Model auth NEVER enters the sandbox env (01 §8.5). The harness's model-API
  # traffic is proxied and credentialed at the edge — by Nucleus's credential
  # broker (gVisor provider) or mitos's broker forward-proxy (05 R1). For local
  # T1 dev without a proxy, an operator MAY allowlist passthrough vars here; it
  # is empty by default to keep the posture spec-clean.
  harness_passthrough_env: [],
  # Loop-manager defaults (04 §4.3 trip thresholds; MVP: one iteration).
  loop_manager: [
    max_iterations: 1,
    no_progress_n: 3,
    repeated_error_m: 2,
    verify_fail_k: 2,
    # 16 §2: Δpredicate ≈ 0 across K segments. Diff-based no_progress is
    # trivially satisfied by an agent that keeps editing; this is not.
    predicate_stall_k: 3,
    predicate_epsilon: 1.0e-9
  ],
  # What enters the sandbox as the run's subject (Mitos.Workspace).
  #
  #   mode: :clone     — 05 §5.1's default: copy the input INTO the sandbox.
  #                      The copy honours .gitignore, so build output, caches
  #                      and VM images never cross the boundary.
  #   mode: :in_place  — turn the sandbox off and work on the operator's real
  #                      tree. A per-run choice for a workspace you already own;
  #                      T2 refuses it outright (05 §4).
  #
  # max_bytes backstops a clone: a workspace over the ceiling is refused with
  # its largest offenders named, rather than started and discovered at 94% disk.
  # 0 disables the check.
  workspace: [mode: :clone, max_bytes: 2_000_000_000],
  # Reclaiming what a run leaves behind (Mitos.GC). `reap_on_finish` drops the
  # sandbox of a run that SUCCEEDED — a failed run's sandbox is the first thing
  # you want to look at, so it is kept until `mitos gc` ages it out.
  gc: [reap_on_finish: true, keep_days: 7],
  # Observation plane (14). The thresholds MUST satisfy snap < steer < block
  # (14 §2.1) — Clock.thresholds/1 refuses a misordered set rather than
  # reordering it, because a steer inside a repaint is a wrong keystroke.
  observation: [
    thresholds: [snap: 200, steer: 750, block: 45_000],
    rows: 24,
    cols: 80,
    session_poll_ms: 100
  ],
  # Decision plane (15 §4). `external` acts take NO default from here or
  # anywhere else — a configurable fail-secure is a fail-safe with extra steps.
  decision: [
    ttl_ms: [internal: 5 * 60_000, recoverable: 30 * 60_000],
    escalation_floor: %{}
  ],
  # Restart intensity (16 §4) — OTP semantics, the outer bound on the ladder.
  supervision: [max_restarts: 3, max_seconds: 3600, strategy: :rewind_to_green],
  # Unattended horizon granted to a run that clears 16 §6's preconditions.
  autonomy: [horizon_ms: 8 * 60 * 60 * 1000],
  # Facilitator economics (08 §4.2).
  take_rate: 0.12,
  take_floor_usd: 0.05,
  compute_rate_usd_per_ms: 0.0,
  settlement: Mitos.Settlement.Mock,
  # Web API + broker listen ports. Override per-instance with the MITOS_HTTP_PORT /
  # MITOS_BROKER_PORT env vars or `serve --http-port/--broker-port` so two control
  # planes don't collide on :eaddrinuse.
  http_port: http_port,
  broker_port: broker_port,
  # The console's identity default (11 §13.2). A local deployment is the
  # operator's own machine — no sign-in wall; the role picker survives as a
  # "view as" cut preview. A hosted deployment sets this to nil to require
  # sign-in (the IdP seam, 08 §2, is unchanged — only the default differs).
  web_default_role: "operator",
  # The host-side model-egress proxy (05 R1): the edge a harness's model calls
  # route through so the model key (in the Vault) never enters the sandbox.
  # Nucleus points its `--credential-broker` here; the Local provider sets the
  # harness base-URL/proxy env here. 0 ⇒ disabled.
  model_proxy_port: model_proxy_port,
  # The TLS-MITM edge (05 R1): intercepts the harness's HTTPS model calls,
  # injects the Vault key, re-encrypts to the real upstream. Needed for harnesses
  # with no base-URL override (pi/claude). Nucleus's --credential-broker and the
  # Local provider both route here. 0 ⇒ disabled; enable via MITOS_MITM_PROXY_PORT
  # and set :mitm_proxy_url so harnesses get HTTPS_PROXY + the CA trust env.
  mitm_proxy_port: mitm_proxy_port,
  # 09 §2 load-bearing judgment call: launch-path runs curated sellers at T1
  # locally. True T2 requires a remote microVM provider and fails closed until
  # one is registered (05 §4: distrust scales isolation; it never relaxes it).
  marketplace_tier: "T1"

import_config "#{config_env()}.exs"
