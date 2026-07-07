defmodule Mitos.Provider.Nucleus do
  @moduledoc """
  The Nucleus `SandboxProvider` (05 §3) — **gVisor-class local isolation**.
  Nucleus is a lightweight, security-hardened container runtime (`nucleus run`)
  that provides a gVisor userspace kernel + seccomp + cgroups + namespaces, so
  the harness runs under syscall interposition with kernel-enforced deny-all
  egress except the allowlist mitos declares.

  Per spec 05 §3 Nucleus(gVisor) is a **T0/T1** provider: stronger than
  `Provider.Local`'s process isolation, but local (shared host kernel mediated
  by gVisor). True **T2** is a *remote* microVM with a separate kernel
  (E2B/Daytona, Firecracker); the launch policy reserves T2 for those. An
  operator who accepts gVisor as sufficient isolation for a curated workload
  registers this provider for T1 — distrust scales isolation; it never relaxes
  it (05 §4).

  Integration model (01 §8.6): Nucleus is the sandbox; the harness CLI is the
  *inner* command. The provider asks the harness adapter for its argv
  (`argv/2`, no spawn), wraps it in `nucleus run --workspace … -- <harness>`,
  and the adapter still owns normalization of the passthrough stream. Egress to
  the model API is allowlisted via Nucleus's `--egress-domain` (config
  `:nucleus_egress_domains`); the production posture forces model traffic
  through the broker's forward proxy (05 R1) — a gap until that proxy lands.

  The sandbox layout + harvest are shared with `Provider.Local` (same hermetic
  materialization, same git-baseline diff); only the launch boundary differs.
  """
  @behaviour Mitos.SandboxProvider

  alias Mitos.Harness.PortAdapter

  @impl true
  def prepare(spec), do: Mitos.Provider.Local.prepare(spec)

  @impl true
  def start(handle, argv, env) do
    harness_mod = handle.spec.harness_mod

    unless function_exported?(harness_mod, :argv, 2) do
      raise "Nucleus provider requires the harness adapter to implement argv/2 (#{inspect(harness_mod)} does not)"
    end

    prompt = List.first(argv) || ""
    opts = [model: env["MITOS_MODEL"], max_turns: env["MITOS_MAX_TURNS"] || 30]
    {inner_exe, inner_args} = harness_mod.argv(prompt, opts)
    nucleus_args = build_argv(handle, inner_exe, inner_args)

    env = Map.merge(harness_mod.hermetic_env(%{home: "/home/agent"}), env)

    case PortAdapter.launch("nucleus", nucleus_args, handle.workspace, Mitos.Hermetic.env(env)) do
      {:ok, port_session} ->
        session = Map.merge(port_session, %{harness_mod: harness_mod, handle: handle})
        {:ok, session}

      {:error, :cli_not_found} ->
        {:error, :nucleus_cli_not_found}
    end
  end

  @doc "The `nucleus run` argv wrapping an inner harness command (exposed for tests)."
  def build_argv(handle, inner_exe, inner_args) do
    # When a harness rootfs bundle is configured (G6 — nucleus.lib.mkAgentToolchain-
    # Rootfs), run the harness inside it via `--bundle` instead of the minimal
    # agent rootfs (which has no shell/harness). Otherwise use agent mode.
    base =
      case Application.get_env(:mitos, :nucleus_bundle, nil) do
        nil ->
          [
            "--workspace",
            handle.workspace,
            "--workspace-mode",
            "bind-rw",
            "--workspace-exec",
            "--workdir",
            "/workspace",
            "--home",
            "/home/agent"
          ]

        bundle ->
          ["--bundle", bundle]
      end

    [
      "run"
      | base
    ] ++
      [
        "--runtime",
        "gvisor",
        "--rootless",
        "--userns",
        "keep-id",
        "--memory",
        resource(handle, :memory, "2G"),
        "--cpus",
        resource(handle, :cpus, "2"),
        "--pids",
        resource(handle, :pids, "512")
      ] ++ egress_args() ++ credential_broker_args() ++ ["--", inner_exe | inner_args]
  end

  @impl true
  def stream(%{harness_mod: mod} = session), do: PortAdapter.events(session, &mod.normalize/1)

  @impl true
  def control(%{port: port}, :interrupt), do: PortAdapter.safe_close(port)
  def control(_session, _command), do: {:error, :unsupported}

  @impl true
  def snapshot(_session), do: :unsupported

  @impl true
  def harvest(session), do: Mitos.Provider.Local.harvest(session)

  @impl true
  def stop(%{port: port}) do
    PortAdapter.safe_close(port)
    :ok
  end

  def stop(_), do: :ok

  @impl true
  def capabilities do
    %{
      isolation: :gvisor,
      remote?: false,
      egress: :broker,
      data_mount: :ro_snapshot,
      snapshot?: false,
      net_modes: [:deny_all_except_allowlist]
    }
  end

  # --- helpers ---

  # Point Nucleus at the mitos model-egress proxy (05 R1). Nucleus then injects
  # HTTP_PROXY/HTTPS_PROXY for the harness and enforces broker-only egress, so
  # the model key (held at the proxy/Vault) never enters the sandbox. The
  # endpoint must be reachable from the container namespace — operators set
  # `:nucleus_credential_broker` to the bridge IP:port; default assumes host loopback.
  defp credential_broker_args do
    case Application.get_env(:mitos, :nucleus_credential_broker) do
      nil -> []
      endpoint -> ["--credential-broker", endpoint]
    end
  end

  defp resource(handle, key, default) do
    resources = (handle.spec.manifest && handle.spec.manifest.resources) || %{}

    case Map.get(resources, to_string(key)) || Map.get(resources, key) do
      nil -> default
      val -> to_string(val)
    end
  end

  # Egress allowlist: the model API hosts the harness must reach. Production
  # forces this through the broker's forward proxy (05 R1); for local gVisor the
  # operator declares the endpoints here. The broker endpoint itself is reached
  # over the container's default networking.
  defp egress_args do
    domains = Application.get_env(:mitos, :nucleus_egress_domains, default_model_domains())
    ports = Application.get_env(:mitos, :nucleus_egress_ports, ["443"])

    domain_args = for d <- domains, do: ["--egress-domain", d]
    port_args = for p <- ports, do: ["--egress-tcp-port", to_string(p)]
    List.flatten(domain_args ++ port_args)
  end

  defp default_model_domains do
    [
      "api.anthropic.com",
      "api.openai.com",
      "generativelanguage.googleapis.com",
      "open.bigmodel.cn"
    ]
  end
end
