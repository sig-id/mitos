defmodule Mitos.Capability.Manifest do
  @moduledoc """
  The capability manifest (01 §8.3, elaborated 03 §8). A capability is a
  content-addressed bundle of `(DATA + SKILL + TOOLS)` run under an injected
  harness in a sandbox — the atom of execution (04) and monetization (08).

  Identity is content, not the label (03 §9):

      capability_id = H(image_digest ‖ data_snapshot_ref ‖ skill_sha256 ‖ tools_manifest_sha256)

  A capability MUST NOT bake in a harness, a model, secrets, or its input — those
  are injected at launch (03 §1).
  """

  @enforce_keys [:name, :version, :dir, :capability_id, :skill_sha256]
  defstruct name: nil,
            version: nil,
            # source directory (dev) — the "image" for the local provider
            dir: nil,
            image: nil,
            # %{snapshot, mount, ro} | nil  (03 §4)
            data: nil,
            # compatible harness set; mitos injects one (03 §5)
            harnesses: [],
            # relative path to base skill markdown (03 §2)
            skill: "skill.md",
            skill_sha256: nil,
            # [%{"name","exec","input","output","deterministic","side_effects"}] (03 §3)
            tools: [],
            tools_sha256: nil,
            # T0 | T1 | T2 (01 §6, 03 §9)
            trust_tier: "T1",
            # %{cpu, mem, timeout} (01 §8.3)
            resources: %{},
            # [%{dest, credential, requires_approval}] consumed by the broker (01 §8.5)
            egress_policy: [],
            # %{schema, evidence: [...]} — soft result + evidence (03 §7)
            result: %{},
            # %{pre: [...], post: [...]} — deterministic entry/exit (03 §6)
            hooks: %{pre: [], post: []},
            capability_id: nil

  @doc "Load and validate a manifest from a capability directory (mitos.yaml)."
  def load(dir) do
    manifest_path = Path.join(dir, "mitos.yaml")

    with {:ok, raw} <- read_yaml(manifest_path) do
      build(dir, raw)
    end
  end

  def load!(dir) do
    case load(dir) do
      {:ok, m} -> m
      {:error, reason} -> raise "capability manifest error in #{dir}: #{inspect(reason)}"
    end
  end

  defp read_yaml(path) do
    case YamlElixir.read_from_file(path) do
      {:ok, map} when is_map(map) -> {:ok, map}
      {:ok, _} -> {:error, :not_a_map}
      {:error, e} -> {:error, {:yaml, e}}
    end
  end

  defp build(dir, raw) do
    skill_rel = raw["skill"] || "skill.md"
    skill_path = Path.join(dir, skill_rel)

    with {:ok, skill_body} <- read_file(skill_path) do
      tools = raw["tools"] || []
      skill_sha = Mitos.sha256(skill_body)
      tools_sha = Mitos.sha256(Mitos.canonical_json(tools))
      image = raw["image"]
      data = normalize_data(raw["data"])
      # For local dev the "image" is the dir; digest over its skill+tools stands
      # in for an OCI image digest until the build pipeline lands (09 §2, [T]).
      image_digest = image || "dir:" <> Mitos.sha256(dir <> skill_sha <> tools_sha)
      data_ref = (data && data.snapshot) || "none"

      capability_id =
        Mitos.sha256(Enum.join([image_digest, data_ref, skill_sha, tools_sha], ""))

      m = %__MODULE__{
        name: raw["name"] || Path.basename(dir),
        version: raw["version"] || "0.0.0",
        dir: dir,
        image: image,
        data: data,
        harnesses: raw["harnesses"] || ["fake"],
        skill: skill_rel,
        skill_sha256: skill_sha,
        tools: tools,
        tools_sha256: tools_sha,
        trust_tier: raw["trust_tier"] || "T1",
        resources: normalize_keys(raw["resources"] || %{}),
        egress_policy: raw["egress_policy"] || [],
        result: normalize_keys(raw["result"] || %{}),
        hooks: normalize_hooks(raw["hooks"] || %{}),
        capability_id: capability_id
      }

      validate(m)
    end
  end

  defp read_file(path) do
    case File.read(path) do
      {:ok, body} -> {:ok, body}
      {:error, _} -> {:error, {:missing_skill, path}}
    end
  end

  defp normalize_data(nil), do: nil

  defp normalize_data(%{} = d),
    do: %{snapshot: d["snapshot"], mount: d["mount"] || "/data", ro: Map.get(d, "ro", true)}

  defp normalize_hooks(%{} = h) do
    %{
      pre: normalize_hook_list(h["pre"] || []),
      post: normalize_hook_list(h["post"] || [])
    }
  end

  defp normalize_hook_list(list) do
    Enum.map(list, fn hook ->
      %{
        name: hook["name"],
        run: hook["run"],
        on_fail: String.to_atom(hook["on_fail"] || "block")
      }
    end)
  end

  defp normalize_keys(%{} = m), do: Map.new(m, fn {k, v} -> {to_string(k), v} end)

  # 03 §9 / 05 §4: a T2 capability that declares a baked secret or a bind mount
  # MUST be rejected at launch, not runtime. Here we reject at load.
  defp validate(%__MODULE__{trust_tier: "T2"} = m) do
    cond do
      m.data && m.data.mount && String.starts_with?(to_string(m.data.mount), "bind:") ->
        {:error, :t2_bind_mount_forbidden}

      true ->
        {:ok, m}
    end
  end

  defp validate(m), do: {:ok, m}
end
