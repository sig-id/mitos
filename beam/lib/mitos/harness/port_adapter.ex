defmodule Mitos.Harness.PortAdapter do
  @moduledoc false
  # Shared subprocess machinery for harness adapters that observe a CLI through
  # its structured stdout (01 §8.6 "fire-and-drain"): spawn the CLI with a
  # hermetic env, stream newline-delimited JSON, and let the adapter own the
  # per-line normalization. PTY/interactive steering stays out of scope (09 §2).

  @doc "Spawn a harness CLI. `env` is the FULL process env (Hermetic.env/1 applied)."
  def launch(exe, args, cwd, env) do
    if exe = System.find_executable(exe) do
      port_env =
        Enum.map(env, fn
          {k, nil} -> {String.to_charlist(k), false}
          {k, v} -> {String.to_charlist(k), String.to_charlist(v)}
        end)

      port =
        Port.open({:spawn_executable, exe}, [
          :binary,
          :exit_status,
          :hide,
          args: args,
          cd: cwd,
          env: port_env
        ])

      {:ok, %{port: port}}
    else
      {:error, :cli_not_found}
    end
  end

  @doc """
  A line-stream of normalized events. `normalize` is `(line :: binary) -> [event]`.
  Emits a final `%{type: :status, status: {:exited, code}}` on process exit.
  """
  def events(%{port: port} = session, normalize) do
    Stream.resource(
      fn -> {port, "", session} end,
      fn
        :halt ->
          {:halt, :closed}

        {port, buf, sess} ->
          receive do
            {^port, {:data, data}} ->
              {lines, rest} = split_lines(buf <> data)
              {Enum.flat_map(lines, normalize), {port, rest, sess}}

            {^port, {:exit_status, status}} ->
              tail = if buf == "", do: [], else: normalize.(buf)
              {tail ++ [%{type: :status, status: {:exited, status}}], :halt}
          end
      end,
      fn
        :closed -> :ok
        {port, _, _} -> safe_close(port)
      end
    )
  end

  def safe_close(port) do
    if Port.info(port), do: Port.close(port)
    :ok
  catch
    :error, :badarg -> :ok
  end

  def split_lines(buf) do
    parts = String.split(buf, "\n")
    {rest, lines} = List.pop_at(parts, -1)
    {Enum.reject(lines, &(&1 == "")), rest || ""}
  end
end
