defmodule Mitos.Hooks do
  @moduledoc """
  Deterministic capability hook runner.

  Hooks are declared in the capability manifest and run outside the model loop:
  `pre` before the harness starts and `post` before harvest. The runner is
  intentionally small: execute argv/string commands, collect deterministic
  results, and let the session append canonical Effects.
  """

  @type result :: map()

  @spec run(String.t() | atom(), map() | nil, keyword()) :: [result()]
  def run(phase, launch_spec, opts \\ []) do
    phase = to_string(phase)
    hooks = hooks_for(launch_spec, phase)
    cwd = Keyword.get(opts, :cwd) || File.cwd!()
    env = Keyword.get(opts, :env)

    hooks
    |> Enum.reduce_while([], fn hook, acc ->
      result = run_one(phase, hook, cwd, env)
      acc = acc ++ [result]

      if result["blocked?"] do
        {:halt, acc}
      else
        {:cont, acc}
      end
    end)
  end

  def blocked?(results), do: Enum.any?(results, & &1["blocked?"])

  def failed(results), do: Enum.filter(results, &(not &1["success?"]))

  def effect_payload(result) do
    result
    |> Map.drop(["stdout"])
    |> Map.put("stdout_bytes", byte_size(result["stdout"] || ""))
  end

  defp run_one(phase, hook, cwd, env) do
    hook = normalize_hook(hook)
    on_fail = hook["on_fail"] || "block"
    started_at = System.monotonic_time(:millisecond)

    {stdout, exit_status, error, command_text} =
      case command(hook["run"]) do
        {:ok, cmd, args, command_text} ->
          try do
            case System.cmd(cmd, args, cmd_opts(cwd, env)) do
              {output, status} -> {output, status, nil, command_text}
            end
          rescue
            exception -> {"", 127, Exception.message(exception), command_text}
          end

        {:error, reason} ->
          {"", 127, reason, nil}
      end

    finished_at = System.monotonic_time(:millisecond)
    success? = exit_status == 0 and is_nil(error)

    %{
      "phase" => phase,
      "name" => hook["name"] || "hook",
      "run" => hook["run"],
      "command" => command_text,
      "on_fail" => on_fail,
      "success?" => success?,
      "exit_status" => exit_status,
      "stdout" => stdout,
      "error" => error,
      "duration_ms" => max(finished_at - started_at, 0),
      "blocked?" => not success? and on_fail == "block"
    }
    |> Enum.reject(fn {_key, value} -> is_nil(value) end)
    |> Map.new()
  end

  defp normalize_hook(%{} = hook) do
    Map.new(hook, fn {key, value} -> {to_string(key), value} end)
  end

  defp normalize_hook(other), do: %{"name" => "hook", "run" => other, "on_fail" => "block"}

  defp command([cmd | args] = argv) when is_binary(cmd) do
    args = Enum.map(args, &to_string/1)
    {:ok, cmd, args, Enum.join(Enum.map(argv, &to_string/1), " ")}
  end

  defp command(command) when is_binary(command) and command != "" do
    {:ok, "/bin/sh", ["-c", command], command}
  end

  defp command(_), do: {:error, "hook run must be a command string or argv list"}

  defp hooks_for(%{} = launch_spec, phase) do
    launch_spec
    |> field("hooks", %{})
    |> case do
      %{} = hooks -> field(hooks, phase, [])
      _ -> []
    end
    |> case do
      hooks when is_list(hooks) -> hooks
      _ -> []
    end
  end

  defp hooks_for(_launch_spec, _phase), do: []

  defp field(%{} = map, key, default) when is_binary(key) do
    Map.get(map, key, Map.get(map, String.to_atom(key), default))
  end

  defp cmd_opts(cwd, env) do
    [cd: cwd, stderr_to_stdout: true]
    |> maybe_put_env(env_pairs(env))
  end

  defp maybe_put_env(opts, nil), do: opts
  defp maybe_put_env(opts, env), do: Keyword.put(opts, :env, env)

  defp env_pairs(nil), do: nil

  defp env_pairs(%{} = env) do
    Enum.map(env, fn {key, value} -> {to_string(key), to_string(value)} end)
  end

  defp env_pairs(env) when is_list(env) do
    Enum.flat_map(env, fn
      {key, value} -> [{to_string(key), to_string(value)}]
      %{"name" => key, "value" => value} -> [{to_string(key), to_string(value)}]
      %{name: key, value: value} -> [{to_string(key), to_string(value)}]
      _ -> []
    end)
  end
end
