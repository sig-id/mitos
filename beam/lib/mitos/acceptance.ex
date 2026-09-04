defmodule Mitos.Acceptance do
  @moduledoc """
  The hard verdict (`16 §3.2`, `03 §7.1`) — the only ground truth a long-horizon
  run has.

  The capability's `result` is a **soft** self-report interpreted by the
  orchestrator (`01 §8.3`). Over one step that is the right trade. Over a
  twelve-hour horizon it is a closed loop in which the model grades its own
  homework, and every mechanism that depends on knowing whether progress
  happened — retry, rewind, speculation, supervisor trust — becomes
  unmeasurable.

  Three rules, each enforced here rather than documented:

    1. **Machine-checkable.** `exit 0` is the verdict. An LLM judge may
       supplement it (`04 §10`) but is never the sole verdict for a run granted
       an unattended horizon.
    2. **Executed outside the workspace.** The predicate runs in a fresh verify
       directory, and the oracle is restored there **from the capability
       directory** — a path the sandbox never had write access to. "Make the
       tests pass" and "make the test runner lie" are the same gradient until
       the oracle is out of reach.
    3. **Touching the oracle is an event, not a judgment call.** A harvested
       diff intersecting `oracle_paths` emits `oracle_tampered` and escalates.
       It is a path check on a diff — cheap, deterministic, and it removes the
       commonest way a long run produces a confidently wrong green.
  """

  alias Mitos.Capability.Manifest

  @type verdict :: :pass | :fail | :unknown | :skipped

  @doc """
  Run the predicate for one harvested step.

  Required ctx: `manifest`, `run_id`, `stage_id`, `workspace` (the sandbox
  workspace as harvested). Optional: `diff_paths`, `append`, `caused_by`,
  `previous_score`.

  Returns `%{verdict, score, delta, tampered, exit_status, output, verify_dir}`.
  """
  def run(%{manifest: %Manifest{acceptance: nil}} = ctx) do
    record(ctx, skipped())
  end

  def run(%{manifest: %Manifest{acceptance: acc} = manifest} = ctx) do
    tampered = tampered?(ctx[:diff_paths] || [], acc.oracle_paths)

    if tampered do
      # A run that edited its own oracle has produced no verdict worth having,
      # whatever the exit status would be. Report it and stop.
      emit(ctx, :oracle_tampered, %{
        "oracle_paths" => acc.oracle_paths,
        "touched" => touched(ctx[:diff_paths] || [], acc.oracle_paths)
      })

      record(ctx, %{skipped() | verdict: :fail, tampered: true, output: "oracle paths modified by the run"})
    else
      case prepare_verify_dir(ctx, manifest) do
        {:ok, verify_dir} ->
          {output, status} = exec(acc.cmd, verify_dir, acc.timeout_ms)
          score = measure(acc.measure, verify_dir, acc.timeout_ms)

          outcome = %{
            verdict: if(status == 0, do: :pass, else: :fail),
            score: score,
            delta: delta(score, ctx[:previous_score]),
            tampered: false,
            exit_status: status,
            output: String.slice(output, -4_000, 4_000) || output,
            verify_dir: verify_dir
          }

          record(ctx, outcome)

        # No verdict is not a passing verdict: `:unknown` denies the green
        # checkpoint and the unattended horizon that rests on it (16 §3.1).
        {:error, reason} ->
          record(ctx, %{skipped() | verdict: :unknown, output: "verify workspace unavailable: #{inspect(reason)}"})
      end
    end
  end

  @doc """
  Does the harvested diff touch the oracle? Paths are matched as globs relative
  to the workspace root (`16 §3.2`).
  """
  def tampered?(diff_paths, oracle_paths), do: touched(diff_paths, oracle_paths) != []

  @doc "The subset of `diff_paths` that falls inside `oracle_paths`."
  def touched(diff_paths, oracle_paths) do
    Enum.filter(diff_paths, fn path ->
      Enum.any?(oracle_paths, &glob_match?(&1, path))
    end)
  end

  @doc """
  May a capability be granted an unattended horizon (`16 §6`)? A capability with
  no machine-checkable predicate may be watched, not left.
  """
  def unattended_ok?(%Manifest{acceptance: nil}), do: false
  def unattended_ok?(%Manifest{acceptance: %{cmd: [_ | _]}}), do: true
  def unattended_ok?(_), do: false

  # --- internals ---

  defp skipped,
    do: %{verdict: :skipped, score: nil, delta: nil, tampered: false, exit_status: nil, output: nil, verify_dir: nil}

  # The verify directory is the point of the whole module: a copy of what the
  # run produced, with the oracle put back from a source the run never had.
  defp prepare_verify_dir(ctx, %Manifest{acceptance: acc} = manifest) do
    dir = Path.join([Mitos.run_dir(ctx.run_id), "acceptance", to_string(ctx.stage_id), Mitos.id()])
    File.mkdir_p!(dir)

    # The verify dir is a third copy of the workspace, per stage. It honours
    # `.gitignore` and the clone ceiling for the same reason the clone does —
    # a predicate does not need the VM images to run the tests.
    result =
      if ctx[:workspace] && File.dir?(ctx.workspace) do
        Mitos.Workspace.clone(ctx.workspace, dir)
      else
        {:ok, dir}
      end

    case result do
      {:ok, _} ->
        # Git metadata is not part of the subject under test and would let a
        # predicate inspect (or a hook mutate) run history.
        File.rm_rf!(Path.join(dir, ".git"))
        restore_oracle(dir, manifest.dir, acc.oracle_paths)
        {:ok, dir}

      {:error, reason} ->
        {:error, reason}
    end
  end

  # Restore every oracle path from the capability directory, overwriting
  # whatever the run left there. Even when the tamper check has already passed,
  # this makes the verdict independent of the workspace copy.
  defp restore_oracle(verify_dir, cap_dir, oracle_paths) do
    Enum.each(oracle_paths, fn pattern ->
      cap_dir
      |> Path.join(pattern)
      |> Path.wildcard()
      |> Enum.each(fn src ->
        rel = Path.relative_to(src, cap_dir)
        dest = Path.join(verify_dir, rel)
        File.mkdir_p!(Path.dirname(dest))
        File.rm_rf!(dest)
        File.cp_r!(src, dest)
      end)
    end)
  end

  defp exec([], _dir, _timeout), do: {"no acceptance command", 127}

  defp exec([exe | args], dir, timeout) do
    task =
      Task.async(fn ->
        try do
          System.cmd(exe, Enum.map(args, &to_string/1),
            cd: dir,
            stderr_to_stdout: true,
            env: verify_env(dir)
          )
        rescue
          e -> {Exception.message(e), 127}
        end
      end)

    case Task.yield(task, timeout) || Task.shutdown(task, :brutal_kill) do
      {:ok, {output, status}} -> {output, status}
      nil -> {"acceptance timed out after #{timeout}ms", 124}
    end
  end

  # A scalar progress measure. `Δ ≈ 0 across K segments` is `predicate_stall`
  # (16 §2), so this is what makes progress non-narrative.
  defp measure(nil, _dir, _timeout), do: nil

  defp measure(cmd, dir, timeout) do
    {output, status} = exec(cmd, dir, timeout)
    if status == 0, do: parse_score(output), else: nil
  end

  defp parse_score(output) do
    line = output |> String.split("\n", trim: true) |> List.last() |> to_string() |> String.trim()

    case Float.parse(line) do
      {n, _} ->
        n

      :error ->
        case Mitos.JSON.decode(line) do
          {:ok, %{"score" => s}} when is_number(s) -> s * 1.0
          {:ok, n} when is_number(n) -> n * 1.0
          _ -> nil
        end
    end
  end

  defp delta(nil, _prev), do: nil
  defp delta(_score, nil), do: nil
  defp delta(score, prev), do: score - prev

  # Hermetic (05 §6): the predicate is substrate code, not the agent's, but it
  # still must not pick up the operator's ambient environment or it would be
  # verifying something other than the artifact.
  defp verify_env(dir) do
    Mitos.Hermetic.env(%{
      "HOME" => dir,
      "PATH" => System.get_env("PATH", "/usr/local/bin:/usr/bin:/bin"),
      "MITOS_VERIFY_DIR" => dir
    })
    |> Enum.reject(fn {_k, v} -> is_nil(v) end)
  end

  defp record(ctx, outcome) do
    emit(ctx, :acceptance_recorded, %{
      "verdict" => to_string(outcome.verdict),
      "score" => outcome.score,
      "delta" => outcome.delta,
      "tampered" => outcome.tampered,
      "exit_status" => outcome.exit_status,
      "output" => outcome.output
    })

    outcome
  end

  defp emit(%{append: append} = ctx, type, payload) when is_function(append, 1) do
    fields = [type: type, ref: %{"stage_id" => ctx[:stage_id]}, payload: payload]
    append.(if ctx[:caused_by], do: [{:caused_by, ctx[:caused_by]} | fields], else: fields)
  end

  defp emit(_ctx, _type, _payload), do: :ok

  # `test/**` matches `test/a/b.exs`; `justfile` matches exactly.
  defp glob_match?(pattern, path) do
    regex =
      pattern
      |> Regex.escape()
      |> String.replace("\\*\\*/", "(?:.*/)?")
      |> String.replace("\\*\\*", ".*")
      |> String.replace("\\*", "[^/]*")
      |> String.replace("\\?", ".")

    Regex.match?(~r/^#{regex}$/, to_string(path))
  end
end
