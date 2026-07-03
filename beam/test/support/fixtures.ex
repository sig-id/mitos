defmodule Mitos.Fixtures do
  @moduledoc """
  Test fixtures: generated capability directories driven by the Fake harness
  (a deterministic `tools/agent.sh` standing in for the LLM — 09 §2 exit
  criteria are about the boundary, not the model).

  YAML 1.2 is a JSON superset, so structured manifest fields are embedded as
  JSON — no hand-rolled YAML serializer.
  """

  @default_script """
  #!/bin/sh
  set -e
  echo "agent: working in $(pwd)"
  echo "note from agent (prompt sha: $(echo "$MITOS_PROMPT" | wc -c))" > note.txt
  cat > "$MITOS_RESULT" <<'EOF'
  {"signal":"ship","confidence":0.9,"summary":"work complete","metrics":{"n":1},
   "evidence":[{"kind":"report","path":"note.txt"}],"needs":[]}
  EOF
  echo '{"tokens": 120, "usd": 0.02}' > "$MITOS_USAGE"
  """

  @doc """
  Create a capability dir. Options:

    * `:script` — the agent.sh body (default: writes note.txt + RESULT + usage)
    * `:egress_policy`, `:hooks`, `:result`, `:trust_tier`, `:harnesses`, `:skill`
  """
  def capability(name, opts \\ []) do
    dir = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), name])
    File.mkdir_p!(Path.join(dir, "tools"))

    manifest = """
    name: #{name}
    version: 1.0.0
    harnesses: #{Mitos.JSON.encode!(opts[:harnesses] || ["fake"])}
    skill: skill.md
    trust_tier: "#{opts[:trust_tier] || "T1"}"
    egress_policy: #{Mitos.JSON.encode!(opts[:egress_policy] || [])}
    result: #{Mitos.JSON.encode!(opts[:result] || %{"evidence" => ["note.txt"]})}
    hooks: #{Mitos.JSON.encode!(opts[:hooks] || %{})}
    """

    File.write!(Path.join(dir, "mitos.yaml"), manifest)

    File.write!(
      Path.join(dir, "skill.md"),
      opts[:skill] ||
        """
        <!-- @skill:role -->
        You are the #{name} capability.
        <!-- @skill:procedure -->
        1. Do the work. 2. Write RESULT.
        """
    )

    script_path = Path.join(dir, "tools/agent.sh")
    File.write!(script_path, opts[:script] || @default_script)
    File.chmod!(script_path, 0o755)
    dir
  end

  @doc "Create an input workspace dir with the given %{relative_path => content}."
  def workspace(files \\ %{"README.md" => "# input\n"}) do
    dir = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "workspace"])
    File.mkdir_p!(dir)

    Enum.each(files, fn {rel, content} ->
      path = Path.join(dir, rel)
      File.mkdir_p!(Path.dirname(path))
      File.write!(path, content)
    end)

    dir
  end

  @doc "A two-stage linear pipeline intent (04 §8 structured lean)."
  def pipeline(stage_names, opts \\ []) do
    budget = opts[:budget] || %{"usd" => 1.0}

    stages =
      stage_names
      |> Enum.with_index(1)
      |> Enum.map_join("\n", fn {name, i} -> "#{i}. **#{name}** — stage #{i} of the flow." end)

    """
    # Pipeline: #{opts[:name] || "test-flow"}

    Goal: #{opts[:goal] || "exercise a linear run end to end."}

    ```yaml
    budget: #{Mitos.JSON.encode!(budget)}
    ```

    ## Stages
    #{stages}
    """
  end
end
