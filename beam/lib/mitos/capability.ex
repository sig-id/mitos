defmodule Mitos.Capability do
  @moduledoc """
  Canonical capability manifest (`01_ARCHITECTURE` §8.3 / `03_CAPABILITY`).

  The current loader accepts the JSON representation of the spec fields. YAML
  authoring can be added at the file boundary later without changing this
  struct or its validation rules.
  """

  @enforce_keys [:id, :name, :harnesses, :trust_tier]
  defstruct [
    :id,
    :name,
    :version,
    :image,
    :skill,
    :skill_adapt,
    :data,
    :resources,
    :result,
    :hooks,
    :digest,
    harnesses: [],
    tools: [],
    egress_policy: [],
    trust_tier: "T1"
  ]

  @trust_tiers ~w(T0 T1 T2)
  @hook_phases ~w(pre post)
  @hook_on_fail ~w(block warn skip)
  @skill_anchors ~w(skill:role skill:rules skill:procedure skill:success)
  @manifest_keys ~w(
    data
    egress_policy
    harnesses
    hooks
    image
    name
    resources
    result
    skill
    skill_adapt
    tools
    trust_tier
    version
  )
  @egress_policy_keys ~w(id dest destination credential credential_id requires_approval)
  @injected_runtime_fields ~w(harness input model prompt provider workspace)
  @secret_field_names ~w(
    api_key
    apikey
    auth_header
    authorization
    bearer
    client_secret
    credentials
    credential_value
    env
    environment
    password
    passwd
    private_key
    refresh_token
    secret
    secrets
    token
  )

  @type t :: %__MODULE__{
          id: String.t(),
          name: String.t(),
          version: String.t() | nil,
          image: String.t() | nil,
          skill: String.t() | nil,
          skill_adapt: map(),
          data: map() | nil,
          harnesses: [String.t()],
          tools: [map()],
          trust_tier: String.t(),
          resources: map(),
          egress_policy: [map()],
          result: map(),
          hooks: map(),
          digest: String.t()
        }

  @spec from_json(String.t(), map(), keyword()) :: {:ok, t()} | {:error, term()}
  def from_json(id, json, opts \\ [])

  def from_json(id, %{} = json, opts) when is_binary(id) and is_list(opts) do
    name = string_or(field(json, "name"), id)
    harnesses = string_list(field(json, "harnesses"))
    trust_tier = string_or(field(json, "trust_tier"), "T1")
    egress_policy = field(json, "egress_policy") || []
    manifest_field_error = manifest_field_error(json)
    egress_error = egress_policy_error(egress_policy)
    runtime_binding_error = runtime_binding_error(json)
    secret_error = secret_error(json)
    tools_error = tools_error(field(json, "tools"))
    hooks_error = hooks_error(field(json, "hooks"))
    skill_adapt_error = skill_adapt_error(field(json, "skill_adapt"))

    cond do
      name == "" ->
        {:error, {:invalid_capability, "name is required"}}

      harnesses == [] ->
        {:error, {:invalid_capability, "harnesses must list at least one harness"}}

      trust_tier not in @trust_tiers ->
        {:error, {:invalid_capability, "trust_tier must be one of T0, T1, T2"}}

      invalid_image?(field(json, "image")) ->
        {:error, {:invalid_capability, "image must be content-addressed with @sha256:"}}

      invalid_data?(field(json, "data"), trust_tier) ->
        {:error, {:invalid_capability, data_error(field(json, "data"), trust_tier)}}

      not is_map(field(json, "result") || %{}) ->
        {:error, {:invalid_capability, "result must be an object"}}

      not is_list(egress_policy) ->
        {:error, {:invalid_capability, "egress_policy must be a list"}}

      egress_error ->
        {:error, {:invalid_capability, egress_error}}

      runtime_binding_error ->
        {:error, {:invalid_capability, runtime_binding_error}}

      manifest_field_error ->
        {:error, {:invalid_capability, manifest_field_error}}

      secret_error ->
        {:error, {:invalid_capability, secret_error}}

      tools_error ->
        {:error, {:invalid_capability, tools_error}}

      hooks_error ->
        {:error, {:invalid_capability, hooks_error}}

      skill_adapt_error ->
        {:error, {:invalid_capability, skill_adapt_error}}

      true ->
        manifest = normalize_manifest(json)
        digest = "sha256:" <> digest_manifest(identity_material(manifest, opts))

        {:ok,
         %__MODULE__{
           id: "cap:" <> digest,
           name: name,
           version: string_or(field(json, "version"), nil),
           image: string_or(field(json, "image"), nil),
           skill: string_or(field(json, "skill"), nil),
           skill_adapt: skill_adapt_or(field(json, "skill_adapt"), %{}),
           data: map_or_nil(field(json, "data")),
           harnesses: harnesses,
           tools: map_list(field(json, "tools")),
           trust_tier: trust_tier,
           resources: map_or(field(json, "resources"), %{}),
           egress_policy: map_list(field(json, "egress_policy")),
           result: map_or(field(json, "result"), %{}),
           hooks: hooks_or(field(json, "hooks"), %{"pre" => [], "post" => []}),
           digest: digest
         }}
    end
  end

  def from_json(_id, _json, _opts),
    do: {:error, {:invalid_capability, "manifest must be an object"}}

  @doc "Return fields that should flow into a LaunchSpec."
  def launch_fields(%__MODULE__{} = capability) do
    %{
      capability_ref: capability.id,
      trust_tier: capability.trust_tier,
      image: capability.image,
      data: capability.data,
      harnesses: capability.harnesses,
      skill: capability.skill,
      skill_adapt: capability.skill_adapt,
      tools: capability.tools,
      resources: capability.resources,
      egress_policy: capability.egress_policy,
      result: capability.result,
      hooks: capability.hooks
    }
    |> Enum.reject(fn {_key, value} -> blank?(value) end)
    |> Map.new()
  end

  @doc "Buyer-facing manifest view that omits recipe-bearing fields and run mechanics."
  def public_view(%__MODULE__{} = capability) do
    %{
      "capability_ref" => capability.id,
      "name" => capability.name,
      "version" => capability.version,
      "trust_tier" => capability.trust_tier,
      "result" => capability.result
    }
  end

  def compatible_harness?(%__MODULE__{} = capability, harness) when is_binary(harness) do
    harness in capability.harnesses
  end

  @doc """
  Resolve anchored base skill markdown with one or more deterministic overlays.

  Empty overlays preserve the base verbatim. Overlay directives use the spec
  form `@adapt replace|append|prepend skill:<anchor> ...`.
  """
  def resolve_skill(base_skill, overlays \\ [])

  def resolve_skill(base_skill, overlays) when is_binary(base_skill) do
    with :ok <- validate_base_skill_anchors(base_skill),
         {:ok, directives} <- adapt_directives(overlays),
         {:ok, body} <- apply_adapt_directives(base_skill, directives) do
      {:ok, %{"body" => body, "sha256" => "sha256:" <> digest_bytes(body)}}
    end
  end

  def resolve_skill(_base_skill, _overlays), do: {:error, :invalid_base_skill}

  defp invalid_image?(nil), do: false
  defp invalid_image?(""), do: false
  defp invalid_image?(image) when is_binary(image), do: not String.contains?(image, "@sha256:")
  defp invalid_image?(_image), do: true

  defp invalid_data?(nil, _trust_tier), do: false
  defp invalid_data?(%{} = data, trust_tier), do: data_error(data, trust_tier) != nil
  defp invalid_data?(_data, _trust_tier), do: true

  defp data_error(nil, _trust_tier), do: nil

  defp data_error(%{} = data, trust_tier) do
    ro = field(data, "ro", true)
    mount = field(data, "mount", "/data")
    has_snapshot? = present?(field(data, "snapshot")) or present?(field(data, "volume"))
    bind? = present?(field(data, "bind")) or present?(field(data, "path"))

    cond do
      ro != true ->
        "data.ro must be true"

      mount != "/data" ->
        "data.mount must be /data"

      trust_tier == "T2" and bind? ->
        "T2 data must not declare bind or path mounts"

      trust_tier == "T2" and not has_snapshot? ->
        "T2 data must declare a snapshot or volume"

      true ->
        nil
    end
  end

  defp data_error(_data, _trust_tier), do: "data must be an object"

  defp manifest_field_error(%{} = json) do
    json
    |> normalize()
    |> Map.keys()
    |> Enum.find(&(&1 not in @manifest_keys))
    |> case do
      nil -> nil
      key -> "capability manifest contains unsupported field #{inspect(key)}"
    end
  end

  defp egress_policy_error(policy) when is_list(policy) do
    Enum.find_value(policy, fn
      %{} = rule ->
        keys = rule |> normalize() |> Map.keys()
        unknown = Enum.find(keys, &(&1 not in @egress_policy_keys))

        cond do
          unknown != nil ->
            "egress_policy contains unsupported field #{inspect(unknown)}"

          not present?(field(rule, "dest")) and not present?(field(rule, "destination")) ->
            "egress_policy entries require dest"

          not present?(field(rule, "credential")) and not present?(field(rule, "credential_id")) ->
            "egress_policy entries require credential or credential_id"

          true ->
            nil
        end

      _other ->
        "egress_policy entries must be objects"
    end)
  end

  defp egress_policy_error(_policy), do: "egress_policy must be a list"

  defp runtime_binding_error(%{} = json) do
    json
    |> normalize()
    |> Map.keys()
    |> Enum.find(&(&1 in @injected_runtime_fields))
    |> case do
      nil ->
        nil

      field ->
        "capability manifests must not bake injected runtime field #{field}"
    end
  end

  defp tools_error(nil), do: nil

  defp tools_error(tools) when is_list(tools) do
    tools
    |> Enum.with_index()
    |> Enum.find_value(fn
      {%{} = tool, index} -> tool_error(normalize(tool), index)
      {_other, index} -> "tools.#{index} must be an object"
    end)
  end

  defp tools_error(_tools), do: "tools must be a list"

  defp tool_error(tool, index) do
    cond do
      not present?(field(tool, "name")) ->
        "tools.#{index}.name is required"

      not has_tool_schema?(tool) ->
        "tools.#{index} must declare input/output or schema object"

      field(tool, "deterministic") != true ->
        "tools.#{index}.deterministic must be true"

      invalid_side_effects?(field(tool, "side_effects")) ->
        "tools.#{index}.side_effects must be a list of strings"

      true ->
        nil
    end
  end

  defp has_tool_schema?(tool) do
    is_map(field(tool, "schema")) or
      (is_map(field(tool, "input")) and is_map(field(tool, "output")))
  end

  defp invalid_side_effects?(nil), do: false

  defp invalid_side_effects?(effects) when is_list(effects) do
    Enum.any?(effects, &(not is_binary(&1) or &1 == ""))
  end

  defp invalid_side_effects?(_effects), do: true

  defp hooks_error(nil), do: nil

  defp hooks_error(%{} = hooks) do
    hooks = normalize(hooks)
    unknown_phase = hooks |> Map.keys() |> Enum.find(&(&1 not in @hook_phases))

    cond do
      unknown_phase != nil ->
        "hooks contains unsupported phase #{inspect(unknown_phase)}"

      true ->
        Enum.find_value(@hook_phases, fn phase ->
          phase_hooks_error(phase, field(hooks, phase, []))
        end)
    end
  end

  defp hooks_error(_hooks), do: "hooks must be an object"

  defp skill_adapt_error(nil), do: nil

  defp skill_adapt_error(%{} = overlays) do
    overlays
    |> normalize()
    |> Enum.find_value(fn
      {model, overlay} when is_binary(model) and model != "" and is_binary(overlay) ->
        nil

      {model, _overlay} when is_binary(model) and model != "" ->
        "skill_adapt overlays must be strings"

      {_model, _overlay} ->
        "skill_adapt model keys must be strings"
    end)
  end

  defp skill_adapt_error(_overlays), do: "skill_adapt must be an object"

  defp phase_hooks_error(phase, hooks) when is_list(hooks) do
    hooks
    |> Enum.with_index()
    |> Enum.find_value(fn
      {%{} = hook, index} -> hook_error(phase, normalize(hook), index)
      {_other, index} -> "hooks.#{phase}.#{index} must be an object"
    end)
  end

  defp phase_hooks_error(phase, _hooks), do: "hooks.#{phase} must be a list"

  defp hook_error(phase, hook, index) do
    path = "hooks.#{phase}.#{index}"

    cond do
      not present?(field(hook, "name")) ->
        "#{path}.name is required"

      not valid_run_spec?(field(hook, "run")) ->
        "#{path}.run must be a string or a non-empty list of strings"

      not valid_on_fail?(field(hook, "on_fail", "block")) ->
        "#{path}.on_fail must be one of block, warn, skip"

      field(hook, "deterministic", true) != true ->
        "#{path}.deterministic must be true"

      invalid_side_effects?(field(hook, "side_effects")) ->
        "#{path}.side_effects must be a list of strings"

      true ->
        nil
    end
  end

  defp valid_run_spec?(run) when is_binary(run), do: run != ""

  defp valid_run_spec?(run) when is_list(run) do
    run != [] and Enum.all?(run, &(is_binary(&1) and &1 != ""))
  end

  defp valid_run_spec?(_run), do: false

  defp valid_on_fail?(on_fail), do: on_fail in @hook_on_fail

  defp secret_error(value), do: secret_error(value, [])

  defp secret_error(%{} = map, path) do
    map
    |> normalize()
    |> Enum.find_value(fn {key, value} ->
      if secret_field?(key) do
        "capability manifests must not contain baked secret field #{format_path(path ++ [key])}"
      else
        secret_error(value, path ++ [key])
      end
    end)
  end

  defp secret_error(list, path) when is_list(list) do
    list
    |> Enum.with_index()
    |> Enum.find_value(fn {value, index} -> secret_error(value, path ++ [index]) end)
  end

  defp secret_error(_value, _path), do: nil

  defp adapt_directives(overlays) do
    overlays
    |> List.wrap()
    |> Enum.reject(&(&1 in [nil, ""]))
    |> Enum.reduce_while({:ok, []}, fn overlay, {:ok, acc} ->
      case parse_adapt_overlay(overlay) do
        {:ok, directives} -> {:cont, {:ok, acc ++ directives}}
        {:error, reason} -> {:halt, {:error, reason}}
      end
    end)
  end

  defp validate_base_skill_anchors(base_skill) do
    missing =
      Enum.reject(@skill_anchors, fn anchor ->
        String.contains?(base_skill, "<!-- @" <> anchor <> " -->")
      end)

    if missing == [] do
      :ok
    else
      {:error, {:missing_skill_anchors, missing}}
    end
  end

  defp parse_adapt_overlay(overlay) when is_binary(overlay) do
    overlay
    |> String.split("\n")
    |> Enum.reduce_while({:ok, [], nil}, fn line, {:ok, directives, current} ->
      case parse_adapt_header(line) do
        {:ok, directive} ->
          directives = append_current_directive(directives, current)
          {:cont, {:ok, directives, directive}}

        :not_header ->
          if current do
            {:cont, {:ok, directives, append_directive_line(current, line)}}
          else
            if String.trim(line) == "" do
              {:cont, {:ok, directives, current}}
            else
              {:halt, {:error, {:invalid_adapt_directive, line}}}
            end
          end

        {:error, reason} ->
          {:halt, {:error, reason}}
      end
    end)
    |> case do
      {:ok, directives, current} -> {:ok, append_current_directive(directives, current)}
      {:error, reason} -> {:error, reason}
    end
  end

  defp parse_adapt_overlay(_overlay), do: {:error, :invalid_adapt_overlay}

  defp parse_adapt_header(line) do
    case Regex.run(~r/^@adapt\s+(replace|append|prepend)\s+(skill:[^\s]+)\s*(.*)$/, line) do
      [_, op, anchor, body] ->
        {:ok, %{"op" => op, "anchor" => anchor, "body" => body}}

      nil ->
        if String.starts_with?(String.trim_leading(line), "@adapt") do
          {:error, {:invalid_adapt_directive, line}}
        else
          :not_header
        end
    end
  end

  defp append_current_directive(directives, nil), do: directives

  defp append_current_directive(directives, directive),
    do: directives ++ [normalize_directive(directive)]

  defp append_directive_line(%{"body" => ""} = directive, line),
    do: %{directive | "body" => line}

  defp append_directive_line(%{"body" => body} = directive, line),
    do: %{directive | "body" => body <> "\n" <> line}

  defp normalize_directive(%{"body" => body} = directive),
    do: %{directive | "body" => String.trim(body)}

  defp apply_adapt_directives(base_skill, directives) do
    Enum.reduce_while(directives, {:ok, base_skill}, fn directive, {:ok, body} ->
      case apply_adapt_directive(body, directive) do
        {:ok, body} -> {:cont, {:ok, body}}
        {:error, reason} -> {:halt, {:error, reason}}
      end
    end)
  end

  defp apply_adapt_directive(body, %{"anchor" => anchor, "op" => op, "body" => patch}) do
    marker = "<!-- @" <> anchor <> " -->"

    case anchored_section(body, marker) do
      nil ->
        {:error, {:adapt_anchor_not_found, anchor}}

      {start_pos, content_start, content_end} ->
        {:ok, splice_anchor_section(body, start_pos, content_start, content_end, op, patch)}
    end
  end

  defp anchored_section(body, marker) do
    with start_pos when is_integer(start_pos) <- :binary.match(body, marker) |> match_pos(),
         content_start <- section_content_start(body, start_pos, byte_size(marker)) do
      content_end = next_anchor_pos(body, content_start) || byte_size(body)
      {start_pos, content_start, content_end}
    else
      _other -> nil
    end
  end

  defp match_pos({pos, _len}), do: pos
  defp match_pos(:nomatch), do: nil

  defp section_content_start(body, marker_pos, marker_len) do
    after_marker = marker_pos + marker_len

    case :binary.match(binary_part(body, after_marker, byte_size(body) - after_marker), "\n") do
      {newline, 1} -> after_marker + newline + 1
      :nomatch -> byte_size(body)
    end
  end

  defp next_anchor_pos(body, start_pos) do
    rest = binary_part(body, start_pos, byte_size(body) - start_pos)

    case Regex.run(~r/\n<!-- @skill:[^>]+ -->/, rest, return: :index) do
      [{relative, _len}] -> start_pos + relative + 1
      nil -> nil
    end
  end

  defp splice_anchor_section(body, _start_pos, content_start, content_end, "replace", patch) do
    binary_part(body, 0, content_start) <>
      section_body(patch) <> binary_part(body, content_end, byte_size(body) - content_end)
  end

  defp splice_anchor_section(body, _start_pos, content_start, content_end, "prepend", patch) do
    existing = binary_part(body, content_start, content_end - content_start)

    binary_part(body, 0, content_start) <>
      section_body(patch) <>
      existing <> binary_part(body, content_end, byte_size(body) - content_end)
  end

  defp splice_anchor_section(body, _start_pos, content_start, content_end, "append", patch) do
    existing = binary_part(body, content_start, content_end - content_start)

    binary_part(body, 0, content_start) <>
      existing <>
      section_body(patch) <> binary_part(body, content_end, byte_size(body) - content_end)
  end

  defp section_body(""), do: ""
  defp section_body(body), do: String.trim_trailing(body) <> "\n"

  defp secret_field?("credential"), do: false
  defp secret_field?("credential_id"), do: false
  defp secret_field?("credential_type"), do: false
  defp secret_field?(key), do: key in @secret_field_names

  defp format_path(path) do
    Enum.map_join(path, ".", fn
      index when is_integer(index) -> Integer.to_string(index)
      key -> key
    end)
  end

  defp normalize_manifest(%{} = json) do
    %{
      "name" => string_or(field(json, "name"), ""),
      "version" => string_or(field(json, "version"), nil),
      "image" => string_or(field(json, "image"), nil),
      "data" => map_or_nil(field(json, "data")),
      "harnesses" => string_list(field(json, "harnesses")),
      "skill" => string_or(field(json, "skill"), nil),
      "skill_adapt" => skill_adapt_or(field(json, "skill_adapt"), %{}),
      "tools" => map_list(field(json, "tools")),
      "trust_tier" => string_or(field(json, "trust_tier"), "T1"),
      "resources" => map_or(field(json, "resources"), %{}),
      "egress_policy" => map_list(field(json, "egress_policy")),
      "result" => map_or(field(json, "result"), %{}),
      "hooks" => hooks_or(field(json, "hooks"), %{"pre" => [], "post" => []})
    }
  end

  defp identity_material(manifest, opts) do
    package_root = Keyword.get(opts, :package_root)

    if is_binary(package_root) and package_root != "" do
      %{
        "image_digest" => image_digest(manifest["image"]),
        "data_snapshot_ref" => data_snapshot_ref(manifest["data"]),
        "data_sha256" => data_content_digest(manifest["data"], package_root),
        "skill_sha256" => package_content_digest(package_root, manifest["skill"]),
        "tools_manifest_sha256" => tools_manifest_digest(manifest["tools"], package_root),
        "manifest" => manifest
      }
      |> reject_blank_values()
    else
      manifest
    end
  end

  defp image_digest(image) when is_binary(image) and image != "" do
    case String.split(image, "@", parts: 2) do
      [_ref, digest] -> digest
      _other -> image
    end
  end

  defp image_digest(_image), do: nil

  defp data_snapshot_ref(%{} = data) do
    field(data, "snapshot") || field(data, "volume")
  end

  defp data_snapshot_ref(_data), do: nil

  defp data_content_digest(%{} = data, package_root) do
    source = field(data, "path") || field(data, "bind")
    package_content_digest(package_root, source)
  end

  defp data_content_digest(_data, _package_root), do: nil

  defp tools_manifest_digest(tools, package_root) when is_list(tools) do
    tools
    |> Enum.map(fn
      %{} = tool ->
        tool
        |> normalize()
        |> maybe_put_exec_digest(package_root)

      other ->
        other
    end)
    |> canonical_json()
    |> digest_bytes()
  end

  defp tools_manifest_digest(_tools, _package_root), do: nil

  defp maybe_put_exec_digest(%{"exec" => exec} = tool, package_root) when is_binary(exec) do
    Map.put(tool, "exec_sha256", package_content_digest(package_root, exec))
  end

  defp maybe_put_exec_digest(tool, _package_root), do: tool

  defp package_content_digest(_package_root, value) when value in [nil, ""], do: nil

  defp package_content_digest(package_root, path)
       when is_binary(package_root) and is_binary(path) do
    case package_path(package_root, path) do
      {:ok, absolute_path} -> content_digest(absolute_path)
      {:error, reason} -> %{"unavailable" => inspect(reason)}
    end
  end

  defp package_content_digest(_package_root, _path), do: nil

  defp content_digest(path) do
    cond do
      File.regular?(path) ->
        case File.read(path) do
          {:ok, bytes} -> "sha256:" <> digest_bytes(bytes)
          {:error, reason} -> %{"unavailable" => inspect(reason)}
        end

      File.dir?(path) ->
        path
        |> content_tree_entries(path)
        |> canonical_json()
        |> digest_bytes()
        |> then(&("sha256:" <> &1))

      true ->
        %{"unavailable" => ":missing"}
    end
  end

  defp content_tree_entries(root, path) do
    case File.ls(path) do
      {:ok, entries} ->
        entries
        |> Enum.sort()
        |> Enum.flat_map(fn entry ->
          child = Path.join(path, entry)
          relative = Path.relative_to(child, root)

          cond do
            File.regular?(child) ->
              case File.read(child) do
                {:ok, bytes} ->
                  [%{"path" => relative, "sha256" => "sha256:" <> digest_bytes(bytes)}]

                {:error, reason} ->
                  [%{"path" => relative, "unavailable" => inspect(reason)}]
              end

            File.dir?(child) ->
              content_tree_entries(root, child)

            true ->
              []
          end
        end)

      {:error, reason} ->
        [%{"path" => Path.relative_to(path, root), "unavailable" => inspect(reason)}]
    end
  end

  defp package_path(package_root, path) do
    root = Path.expand(package_root)

    expanded =
      if Path.type(path) == :absolute do
        Path.expand(path)
      else
        Path.expand(path, root)
      end

    if expanded == root or String.starts_with?(expanded, root <> "/") do
      {:ok, expanded}
    else
      {:error, :path_escape}
    end
  end

  defp hooks_or(%{} = hooks, _default) do
    Map.new(@hook_phases, fn phase ->
      {phase, map_list(field(hooks, phase, []))}
    end)
  end

  defp hooks_or(_hooks, default), do: default

  defp string_or(value, _default) when is_binary(value), do: value
  defp string_or(_value, default), do: default

  defp string_list(value) when is_list(value) do
    value
    |> Enum.filter(&is_binary/1)
    |> Enum.reject(&(&1 == ""))
  end

  defp string_list(_value), do: []

  defp map_or(%{} = value, _default), do: normalize(value)
  defp map_or(_value, default), do: default

  defp map_or_nil(%{} = value), do: normalize(value)
  defp map_or_nil(_value), do: nil

  defp skill_adapt_or(%{} = value, _default), do: normalize(value)
  defp skill_adapt_or(_value, default), do: default

  defp map_list(value) when is_list(value) do
    value
    |> Enum.filter(&is_map/1)
    |> Enum.map(&normalize/1)
  end

  defp map_list(_value), do: []

  defp normalize(%{} = map),
    do: Map.new(map, fn {key, val} -> {to_string(key), normalize(val)} end)

  defp normalize(list) when is_list(list), do: Enum.map(list, &normalize/1)
  defp normalize(value), do: value

  defp field(%{} = map, key, default \\ nil) when is_binary(key) do
    Map.get(map, key, Map.get(map, String.to_atom(key), default))
  end

  defp digest_manifest(manifest) do
    :crypto.hash(:sha256, canonical_json(manifest))
    |> Base.encode16(case: :lower)
  end

  defp digest_bytes(bytes) when is_binary(bytes) do
    :crypto.hash(:sha256, bytes)
    |> Base.encode16(case: :lower)
  end

  defp canonical_json(%{} = map) do
    entries =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map(fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> Enum.join(entries, ",") <> "}"
  end

  defp canonical_json(list) when is_list(list) do
    "[" <> Enum.map_join(list, ",", &canonical_json/1) <> "]"
  end

  defp canonical_json(value), do: Jason.encode!(value)

  defp reject_blank_values(map) do
    map
    |> Enum.reject(fn {_key, value} -> blank?(value) end)
    |> Map.new()
  end

  defp blank?(nil), do: true
  defp blank?(value) when is_map(value), do: map_size(value) == 0
  defp blank?(value) when is_list(value), do: value == []
  defp blank?(""), do: true
  defp blank?(_value), do: false

  defp present?(value), do: not blank?(value)
end
