defmodule Mitos.Harness.Fake do
  @moduledoc """
  A deterministic harness for tests and dry-runs (the `fake` entry in the
  harness registry). Instead of an LLM it executes the capability's baked
  agent script — by convention `tools/agent.sh` in the capability image — with
  the same contract a real harness gets:

    * cwd = the RW workspace
    * `MITOS_SKILL`  → /skill/resolved.md (the base ⊕ adapt text, 03 §2.1)
    * `MITOS_DATA`   → the RO data mount (if any)
    * `MITOS_RESULT` → where to write the RESULT artifact (03 §7)
    * `MITOS_BROKER` + `MITOS_EGRESS_TOKEN` → the only way out (05 §5.3)
    * `MITOS_PROMPT` → the loop-manager's composed context (04 §4.2)

  The script may write `MITOS_USAGE` (JSON `{"tokens": n, "usd": x}`) to report
  deterministic spend; otherwise a size-based estimate is used. This exercises
  every boundary crossing — workspace diff, brokered egress, harvested result —
  without a model, which is exactly what the P2/P3 exit criteria need (09 §2).
  """
  @behaviour Mitos.Harness

  @impl true
  def launch(sandbox, prompt, _opts) do
    script = Path.join(sandbox.cap_dir, "tools/agent.sh")

    if File.exists?(script) do
      {:ok, %{sandbox: sandbox, script: script, prompt: prompt, ran: :atomics.new(1, [])}}
    else
      {:error, {:no_agent_script, script}}
    end
  end

  @impl true
  def events(%{sandbox: sandbox, script: script, prompt: prompt} = session) do
    Stream.resource(
      fn ->
        env = Map.merge(Mitos.Harness.Fake.hermetic_env(sandbox), run_env(sandbox, prompt))

        {out, exit_code} =
          System.cmd("sh", [script],
            cd: sandbox.workspace,
            # full env clear + overrides: nothing ambient reaches the agent (05 §6)
            env: Mitos.Hermetic.env(env),
            stderr_to_stdout: true
          )

        :atomics.put(session.ran, 1, 1)
        {:emit, out, exit_code}
      end,
      fn
        {:emit, out, exit_code} ->
          usage = read_usage(sandbox, out)

          events = [
            %{type: :status, status: :started},
            %{type: :assistant_text, text: out},
            %{type: :token_usage, tokens: usage.tokens, usd: usage.usd},
            %{type: :final_result, exit: exit_code, ok: exit_code == 0}
          ]

          {events, :done}

        :done ->
          {:halt, :done}
      end,
      fn _ -> :ok end
    )
  end

  @impl true
  def steer(_session, _command), do: :ok

  @impl true
  def result(%{sandbox: sandbox}) do
    Mitos.Capability.Result.harvest(sandbox.workspace)
  end

  @impl true
  def hermetic_env(sandbox) do
    # 05 §6: HOME/dotfiles redirected to an ephemeral in-sandbox path; ambient
    # credentials absent (env is built from scratch — nothing inherited).
    Map.merge(Mitos.Hermetic.git_env(sandbox.home), %{
      "XDG_CONFIG_HOME" => Path.join(sandbox.home, ".config"),
      "XDG_CACHE_HOME" => Path.join(sandbox.home, ".cache")
    })
  end

  @impl true
  def capabilities do
    %{
      structured_output: true,
      mcp_tools?: false,
      sessions?: false,
      effort_levels: [],
      observation: observation()
    }
  end

  @impl true
  # Deterministic and headless: C1 + a structured stream, no terminal. Enough to
  # be granted an unattended horizon (16 §6), which is what makes it usable as
  # the fixture harness for the autonomy tests.
  def observation do
    %{
      structured_output: :stream,
      session_artifacts: nil,
      interactive: false,
      modal_classifier: nil,
      pty_control: false,
      resume: :none,
      think_time_p95_ms: 1_000
    }
  end

  defp run_env(sandbox, prompt) do
    %{
      "MITOS_SKILL" => sandbox.skill_path,
      "MITOS_RESULT" => Path.join(sandbox.workspace, Mitos.Capability.Result.result_filename()),
      "MITOS_PROMPT" => prompt,
      "MITOS_USAGE" => usage_path(sandbox)
    }
    |> put_if("MITOS_DATA", sandbox[:data_mount])
    |> put_if("MITOS_BROKER", get_in(sandbox, [:broker, :endpoint]))
    |> put_if("MITOS_EGRESS_TOKEN", get_in(sandbox, [:broker, :token]))
  end

  defp put_if(env, _k, nil), do: env
  defp put_if(env, k, v), do: Map.put(env, k, v)

  defp usage_path(sandbox), do: Path.join(sandbox.dir, "usage.json")

  defp read_usage(sandbox, out) do
    with {:ok, body} <- File.read(usage_path(sandbox)),
         {:ok, %{} = m} <- Mitos.JSON.decode(body) do
      %{tokens: m["tokens"] || 0, usd: m["usd"] || 0.0}
    else
      # Deterministic size-based estimate keeps budget_charged non-zero.
      _ -> %{tokens: max(div(byte_size(out), 4), 1), usd: 0.0}
    end
  end
end
