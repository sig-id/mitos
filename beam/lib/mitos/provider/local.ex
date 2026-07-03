defmodule Mitos.Provider.Local do
  @moduledoc """
  The local dev/test `SandboxProvider` (05 §3) — the seam the Nucleus (gVisor)
  and remote (E2B/Daytona) providers fill in later phases (09 P2/P4).

  It realizes the full boundary *contract* — hermetic injection at `prepare`
  (05 §5.1), harness launch with a from-scratch env (no ambient credentials, no
  secrets), broker-only egress via `MITOS_BROKER` + lease token (05 §5.3), and
  pull-based harvest (05 §5.4) — with **process-level isolation only**:
  `capabilities().isolation = :process`, so the launch policy compiler admits
  it for T0/T1 and never for T2 (05 §4: distrust scales isolation; it never
  relaxes it). Kernel-enforced deny-all egress belongs to the Nucleus provider.

  Sandbox layout under `<data_dir>/sandboxes/<run_id>/<stage_id>/`:

      workspace/          input cloned IN, RW, git baseline committed
      cap/                capability dir copy (SKILL+TOOLS), RO by convention
      skill/resolved.md   base ⊕ per_model_adapt (03 §2.1), sha recorded
      data -> …           RO DATA mount (if declared)
      ctx/                curated context slices (07), RO
      home/               ephemeral HOME (hermeticity, 05 §6)
  """
  @behaviour Mitos.SandboxProvider

  alias Mitos.{Capability, LaunchSpec}

  @impl true
  def prepare(%LaunchSpec{} = spec) do
    dir = sandbox_dir(spec)

    # Idempotent on (run_id, stage): a re-driven Decision replaces the orphaned
    # sandbox (04 §7.1 — one capability_started wins).
    File.rm_rf!(dir)
    File.mkdir_p!(dir)

    workspace = Path.join(dir, "workspace")
    cap_dir = Path.join(dir, "cap")
    home = Path.join(dir, "home")
    ctx_dir = Path.join(dir, "ctx")
    skill_dir = Path.join(dir, "skill")
    Enum.each([workspace, home, ctx_dir, skill_dir], &File.mkdir_p!/1)

    with :ok <- clone_workspace(spec.workspace, workspace),
         {:ok, baseline} <- git_baseline(workspace),
         :ok <- copy_capability(spec.manifest, cap_dir),
         {:ok, skill_path, skill_sha} <- resolve_skill(spec.manifest, cap_dir, skill_dir),
         {:ok, data_mount} <- mount_data(spec, dir) do
      write_context(spec.context, ctx_dir)

      {:ok,
       %{
         spec: spec,
         dir: dir,
         workspace: workspace,
         cap_dir: cap_dir,
         home: home,
         ctx_dir: ctx_dir,
         skill_path: skill_path,
         skill_sha: skill_sha,
         data_mount: data_mount,
         baseline: baseline,
         broker: nil
       }}
    end
  end

  @impl true
  def start(handle, argv, env) do
    prompt = List.first(argv) || ""
    harness_mod = handle.spec.harness_mod

    # The run-scoped egress lease arrives via non-secret env: the broker
    # endpoint + an opaque token — never a credential (05 §5.1/§5.3).
    handle =
      case env["MITOS_EGRESS_TOKEN"] do
        nil ->
          handle

        token ->
          %{handle | broker: %{endpoint: env["MITOS_BROKER"] || Mitos.Broker.Listener.endpoint(), token: token}}
      end

    opts = [
      model: env["MITOS_MODEL"],
      max_turns: env["MITOS_MAX_TURNS"] || 30
    ]

    case harness_mod.launch(sandbox_view(handle), prompt, opts) do
      {:ok, harness_session} ->
        {:ok, %{handle: handle, harness_mod: harness_mod, harness: harness_session}}

      {:error, reason} ->
        {:error, reason}
    end
  end

  @impl true
  def stream(%{harness_mod: mod, harness: session}), do: mod.events(session)

  @impl true
  def control(%{harness_mod: mod, harness: session}, command), do: mod.steer(session, command)

  @impl true
  def snapshot(_session), do: :unsupported

  @impl true
  def harvest(session_or_handle) do
    handle = handle_of(session_or_handle)
    manifest = handle.spec.manifest

    diff = workspace_diff(handle)
    result = Capability.Result.harvest(handle.workspace)
    artifacts = collect_artifacts(handle, manifest, result)

    {:ok, %{diff: diff, result: result, artifacts: artifacts}}
  end

  @impl true
  def stop(_session) do
    # The Fake harness is synchronous; port-backed harnesses close their port
    # when the stream is drained or via steer(:interrupt). The sandbox dir is
    # kept for audit — evidence artifacts were already copied to the run store.
    :ok
  end

  @impl true
  def capabilities do
    %{
      isolation: :process,
      remote?: false,
      egress: :broker,
      data_mount: :bind,
      snapshot?: false,
      net_modes: [:deny_all_except_broker]
    }
  end

  # --- prepare internals ---

  defp sandbox_dir(spec) do
    Path.join([Mitos.data_dir(), "sandboxes", spec.run_id, spec.stage_id || "step"])
  end

  defp clone_workspace(%{source: nil}, _dest), do: :ok

  defp clone_workspace(%{source: source}, dest) do
    cond do
      is_binary(source) and File.dir?(source) ->
        # Clone the input INTO the sandbox (05 §5.1) — never bind the original.
        File.cp_r!(Path.expand(source), dest)
        :ok

      is_binary(source) ->
        {:error, {:workspace_source_not_found, source}}

      true ->
        :ok
    end
  end

  # A git baseline makes harvest a pure `diff` even for non-repo inputs.
  defp git_baseline(workspace) do
    with {_, 0} <- git(workspace, ["init", "-q"]),
         {_, 0} <- git(workspace, ["add", "-A"]),
         {_, 0} <-
           git(workspace, ["commit", "-q", "--allow-empty", "-m", "mitos baseline"]),
         {sha, 0} <- git(workspace, ["rev-parse", "HEAD"]) do
      {:ok, String.trim(sha)}
    else
      {out, code} -> {:error, {:git_baseline_failed, code, out}}
    end
  end

  defp copy_capability(%Capability.Manifest{dir: dir}, cap_dir) do
    File.cp_r!(dir, cap_dir)
    :ok
  end

  defp resolve_skill(manifest, cap_dir, skill_dir) do
    base_path = Path.join(cap_dir, manifest.skill)

    with {:ok, base} <- File.read(base_path) do
      overlay =
        case File.read(Path.join(cap_dir, "adapt/default.md")) do
          {:ok, o} -> o
          _ -> nil
        end

      {resolved, sha} = Capability.Skill.resolve(base, overlay)
      path = Path.join(skill_dir, "resolved.md")
      File.write!(path, resolved)
      {:ok, path, sha}
    else
      {:error, reason} -> {:error, {:skill_unreadable, base_path, reason}}
    end
  end

  # Local DATA is a bind (symlink) mount, RO by convention (05 §7 T0/T1).
  defp mount_data(%LaunchSpec{data: nil}, _dir), do: {:ok, nil}

  defp mount_data(%LaunchSpec{data: data, manifest: m}, dir) do
    source = data.snapshot && Path.expand(data.snapshot, m.dir)

    cond do
      is_nil(source) -> {:ok, nil}
      File.dir?(source) or File.exists?(source) ->
        link = Path.join(dir, "data")
        File.ln_s!(source, link)
        {:ok, link}

      true ->
        {:error, {:data_snapshot_not_found, source}}
    end
  end

  defp write_context(slices, ctx_dir) do
    slices
    |> Enum.with_index()
    |> Enum.each(fn {slice, i} ->
      File.write!(Path.join(ctx_dir, "slice_#{i}.md"), to_string(slice))
    end)
  end

  # --- harvest internals ---

  defp handle_of(%{handle: handle}), do: handle
  defp handle_of(%{workspace: _} = handle), do: handle

  defp workspace_diff(handle) do
    ws = handle.workspace
    {_, _} = git(ws, ["add", "-A"])
    {patch, _} = git(ws, ["diff", "--cached", handle.baseline])
    {stat, _} = git(ws, ["diff", "--cached", "--stat", handle.baseline])
    %{patch: patch, stat: String.trim(stat)}
  end

  # Evidence artifacts are copied out to the run's content-addressed store so
  # they outlive the sandbox (proof/delivery read them, 08 §5).
  defp collect_artifacts(handle, manifest, result) do
    declared = get_in(manifest.result, ["evidence"]) || []
    from_result = for %{"path" => p} <- result["evidence"] || [], do: p

    (declared ++ from_result)
    |> Enum.uniq()
    |> Enum.flat_map(fn rel ->
      src = Path.join(handle.workspace, rel)

      case File.read(src) do
        {:ok, body} ->
          sha = Mitos.sha256(body)
          store = Path.join([Mitos.run_dir(handle.spec.run_id), "artifacts"])
          File.mkdir_p!(store)
          File.write!(Path.join(store, sha), body)

          [
            %{
              "path" => rel,
              "sha256" => sha,
              "bytes" => byte_size(body),
              "artifact" => "art://#{handle.spec.run_id}/#{sha}"
            }
          ]

        _ ->
          []
      end
    end)
  end

  defp git(dir, args) do
    System.cmd("git", ["-c", "commit.gpgsign=false" | args],
      cd: dir,
      stderr_to_stdout: true,
      env: Mitos.Hermetic.env(Mitos.Hermetic.git_env(dir))
    )
  end

  defp sandbox_view(handle) do
    Map.take(handle, [:dir, :workspace, :cap_dir, :home, :ctx_dir, :skill_path, :data_mount, :broker])
  end
end
