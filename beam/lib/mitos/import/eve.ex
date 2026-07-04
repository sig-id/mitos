defmodule Mitos.Import.Eve do
  @moduledoc """
  The Eve `agent/` import funnel (12 §10, 03 §10.1) — "import, don't compete"
  (02 §1): every Eve author is a prospective seller, so import MUST be cheap.

  An Eve agent becomes the §1 degenerate application: **one capability + one
  single-step pipeline + one entrypoint**. The mapping is mechanical, with one
  mandatory manual step — the **message→order gap**: an Eve agent's input is a
  free-form message; a sellable mitos entrypoint needs a typed order. So import
  emits a stub `{ message: string }` order, marks the entrypoint `draft`, and the
  author must type the real order form before publish (12 §10). A session vs a
  job, made mechanical.

  Imported capabilities default to **T2** (03 §10.1): distrust scales isolation;
  the operator vets down. Anything that assumes the agent owns its egress,
  secrets, or model is dropped or re-homed (sandbox/* and agent.ts go; connections
  → egress_policy claims with creds re-homed to the broker).
  """

  @doc """
  Scaffold a mitos application from an Eve `agent/` directory.

  Returns `{:ok, out_dir}`. The output is a dev app directory runnable from disk
  (`mitos run <dir>`) with a `draft` entrypoint; publish is refused until the
  order form is typed (12 §10).
  """
  def scaffold(agent_dir, out_dir) do
    agent_dir = Path.expand(agent_dir)
    out_dir = Path.expand(out_dir)

    with {:dir, true} <- {:dir, File.dir?(agent_dir)} do
      instructions = read_file(agent_dir, "instructions.md")
      {title, summary} = parse_instructions(instructions)
      name = slug(title) || Path.basename(agent_dir) |> slug()

      channels = list(agent_dir, "channels")
      connections = list(agent_dir, "connections")
      schedules = list(agent_dir, "schedules")
      tools = list(agent_dir, "tools")
      evals = list(agent_dir, "evals")

      write_scaffold(out_dir, agent_dir, %{
        name: name,
        title: title,
        summary: summary,
        instructions: instructions,
        channels: channels,
        connections: connections,
        schedules: schedules,
        tools: tools,
        evals: evals
      })

      {:ok, out_dir}
    else
      {:dir, _} -> {:error, :not_a_directory}
    end
  end

  defp write_scaffold(out_dir, agent_dir, s) do
    File.mkdir_p!(out_dir)
    File.mkdir_p!(Path.join(out_dir, "pipelines"))
    cap_dir = Path.join(out_dir, "agent")
    File.mkdir_p!(Path.join(cap_dir, "tools"))

    # SKILL base = instructions.md (unanchored — valid as-is, 03 §2/§10.1).
    File.write!(Path.join(cap_dir, "skill.md"), s.instructions || default_skill(s.name))

    # tools/*.ts → recorded as JS tools (03 §3); impl runs as a JS tool at runtime.
    for t <- s.tools,
        do: File.cp!(Path.join([agent_dir, "tools", t]), Path.join([cap_dir, "tools", t]))

    # capability manifest — imported defaults to T2 (03 §10.1).
    File.write!(Path.join(cap_dir, "mitos.yaml"), capability_yaml(s))

    # one single-step pipeline (the §1 degenerate form).
    File.write!(Path.join(out_dir, "pipelines/main.md"), pipeline_md(s))

    # evals/* → test material for `mitos test` (04 §10, 12 §10).
    unless s.evals == [], do: File.cp_r!(Path.join([agent_dir, "evals"]), Path.join(out_dir, "evals"))

    File.write!(Path.join(out_dir, "app.yaml"), app_yaml(s))

    out_dir
  end

  # The agent runs as a claude-code harness with a generic tool runner; the
  # model/harness preference hint from agent.ts is dropped (03 §10.1).
  defp capability_yaml(s) do
    egress = Enum.map(s.connections, &%{"dest" => stem(&1), "credential" => nil})

    """
    name: #{s.name}
    version: 0.1.0
    harnesses: ["claude-code"]
    skill: skill.md
    trust_tier: "T2"
    egress_policy: #{Mitos.JSON.encode!(egress)}
    result: #{Mitos.JSON.encode!(%{"evidence" => []})}
    hooks: {}
    """
  end

  defp pipeline_md(s) do
    """
    # Pipeline: main

    Goal: run the imported `#{s.name}` agent against the caller's message.

    ## Stages
    1. **agent** — execute the imported capability against the input.
    """
  end

  # io.inputs come from channels/* (10 §4); the reply sink round-trips back to
  # the originating channel (10 §5.4). requirements.trust_tier is the imported
  # T2 aggregate (informational; per-cap authority wins, 12 §11).
  defp app_yaml(s) do
    inputs =
      for c <- s.channels do
        "    - { connector: #{stem(c)}, entrypoint: run, select: { mode: explicit } }"
      end

    outputs = ["    - { sink: reply, on: capability_finished, entrypoint: run }"]

    """
    name: #{s.name}
    version: 0.1.0
    description: #{inspect(s.summary || s.title || s.name)}
    capabilities:
      - { ref: "agent", as: "agent" }
    pipelines:
      - { id: "main", file: "pipelines/main.md" }
    entrypoints:
      - id: "run"
        pipeline: "main"
        title: #{inspect(s.title || s.name)}
        description: #{inspect(s.summary || "")}
        workspace: none
        draft: true
        inputs: { schema: { message: { type: string, required: true } } }
    io:
      inputs:
    #{Enum.join(inputs, "\n")}
      outputs:
    #{Enum.join(outputs, "\n")}
    services: {}
    requirements:
      trust_tier: "T2"
      connectors: #{Mitos.JSON.encode!(Enum.map(s.channels, &stem(&1)))}
      egress: #{Mitos.JSON.encode!(Enum.map(s.connections, &stem(&1)))}
    """
  end

  defp default_skill(name) do
    """
    # #{name}

    (No instructions.md was found — replace this skill with the agent's real
    procedure. Imported skills degrade to document-scope; anchors are optional,
    03 §2.)
    """
  end

  # --- Eve source reading helpers ---

  defp read_file(dir, name) do
    case File.read(Path.join(dir, name)) do
      {:ok, body} -> body
      {:error, _} -> nil
    end
  end

  defp list(dir, sub) do
    path = Path.join(dir, sub)

    case File.ls(path) do
      {:ok, names} ->
        names |> Enum.reject(&String.starts_with?(&1, "."))

      {:error, _} ->
        []
    end
  end

  defp stem(name), do: Path.basename(name, Path.extname(name))

  defp parse_instructions(nil), do: {nil, nil}

  defp parse_instructions(text) do
    lines = String.split(text, "\n", trim: true)

    title =
      Enum.find_value(lines, fn line ->
        case String.trim(line) do
          "#" <> rest -> String.trim(rest)
          _ -> nil
        end
      end)

    summary =
      Enum.find_value(lines, fn line ->
        case String.trim(line) do
          "" -> nil
          "#" <> _ -> nil
          other -> String.trim(other)
        end
      end)

    {title, summary}
  end

  defp slug(nil), do: nil
  defp slug(s) when is_binary(s) do
    s |> String.downcase() |> String.replace(~r/[^a-z0-9]+/, "-") |> String.trim("-")
  end
end
