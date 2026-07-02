import Config

config :mitos,
  # Root for all durable state: event logs, sandboxes, vault, pools.
  data_dir: Path.expand("~/.local/share/mitos"),
  # Registered sandbox providers, consulted by the launch policy compiler (05 §4).
  # Each provider module implements Mitos.SandboxProvider.
  providers: [Mitos.Provider.Local],
  # Registered harness adapters (01 §8.6).
  harnesses: %{
    "fake" => Mitos.Harness.Fake,
    "claude-code" => Mitos.Harness.ClaudeCode
  },
  # Orchestrator policy for runs that don't specify one. The MVP default is the
  # deterministic Linear policy (09 §3: single linear pipeline, no branching).
  orchestrator_policy: Mitos.Orchestrator.Policy.Linear,
  # Loop-manager defaults (04 §4.3 trip thresholds; MVP: one iteration).
  loop_manager: [max_iterations: 1, no_progress_n: 3, repeated_error_m: 2, verify_fail_k: 2],
  # Facilitator economics (08 §4.2).
  take_rate: 0.12,
  take_floor_usd: 0.05,
  compute_rate_usd_per_ms: 0.0,
  settlement: Mitos.Settlement.Mock,
  # Web API + broker listen config.
  http_port: 4646,
  broker_port: 4647,
  # 09 §3 load-bearing judgment call: MVP runs curated sellers at T1 locally.
  # True T2 requires a remote microVM provider (P4) and fails closed until one
  # is registered (05 §4: distrust scales isolation; it never relaxes it).
  marketplace_tier: "T1"

import_config "#{config_env()}.exs"
