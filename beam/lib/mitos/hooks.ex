defmodule Mitos.Hooks do
  @moduledoc """
  Deterministic entry/exit hooks (03 §6). A capability run is a bracket:
  `hooks.pre` → agentic loop → `hooks.post` → harvest. Hooks run deterministic
  code (no model) with access to the workspace, `/data`, and TOOLS; each
  execution is Effect-logged (`hook_ran`).

  `on_fail: block` at a pre gate aborts the step **before any model spend**;
  at a post gate it marks the step's harvest unverified — the orchestrator
  decides what that means (03 §6), never the substrate.
  """

  @doc """
  Run one hook phase. `append` funnels `hook_ran` Effects through the run's
  single writer. Returns `:ok` or `{:blocked, hook_name}`.
  """
  def run(hooks, sandbox, phase, append) when is_list(hooks) do
    Enum.reduce_while(hooks, :ok, fn hook, :ok ->
      {out, exit_code} = execute(hook, sandbox)

      append.(
        type: :hook_ran,
        ref: %{"phase" => to_string(phase), "name" => hook.name},
        payload: %{
          "exit" => exit_code,
          "on_fail" => to_string(hook.on_fail),
          "output" => String.slice(out, 0, 2000)
        }
      )

      case {exit_code, hook.on_fail} do
        {0, _} -> {:cont, :ok}
        {_, :block} -> {:halt, {:blocked, hook.name}}
        {_, _warn_or_skip} -> {:cont, :ok}
      end
    end)
  end

  # `run` is a tool path / argv list / shell string, executed in the workspace
  # with the same hermetic env the harness gets — hooks are inside the boundary
  # (03 §11 q2), so /cap tools resolve and no ambient credentials exist.
  defp execute(%{run: run}, sandbox) do
    env = hook_env(sandbox)

    case run do
      [cmd | args] ->
        safe_cmd(resolve(cmd, sandbox), args, sandbox, env)

      cmd when is_binary(cmd) ->
        safe_cmd("sh", ["-c", resolve(cmd, sandbox)], sandbox, env)

      nil ->
        {"hook has no run", 1}
    end
  end

  defp safe_cmd(cmd, args, sandbox, env) do
    System.cmd(cmd, args,
      cd: sandbox.workspace,
      env: Mitos.Hermetic.env(env),
      stderr_to_stdout: true
    )
  rescue
    e in ErlangError -> {"hook exec failed: #{inspect(e.original)}", 127}
  end

  # `/tools/...` refs resolve into the capability image copy (03 §3).
  defp resolve("/tools/" <> rest, sandbox), do: Path.join([sandbox.cap_dir, "tools", rest])
  defp resolve(cmd, _sandbox), do: cmd

  defp hook_env(sandbox) do
    Mitos.Hermetic.git_env(sandbox.home)
    |> Map.merge(%{
      "MITOS_WORKSPACE" => sandbox.workspace,
      "MITOS_CAP" => sandbox.cap_dir
    })
    |> then(fn env ->
      if sandbox[:data_mount], do: Map.put(env, "MITOS_DATA", sandbox.data_mount), else: env
    end)
  end
end
