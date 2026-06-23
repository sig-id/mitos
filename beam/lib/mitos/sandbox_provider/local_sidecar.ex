defmodule Mitos.SandboxProvider.LocalSidecar do
  @moduledoc """
  Local engine-backed sandbox provider.

  The provider starts one `Mitos.Session`, which owns one `mitos-term` sidecar
  process and presents the current PTY/stdio-json local execution path behind
  the sandbox-provider seam.
  """

  @behaviour Mitos.SandboxProvider

  @impl true
  def prepare(%{} = launch_spec), do: {:ok, launch_spec}

  @impl true
  def start(_handle, _argv, _env), do: {:error, :use_session_supervisor}

  @impl true
  def stream(_session), do: []

  @impl true
  def control(session, {:write_stdin, bytes}) when is_binary(bytes) do
    Mitos.Session.write_stdin(session, bytes)
    :ok
  end

  def control(session, {:steer, interaction, opts}) when is_list(opts) do
    Mitos.Session.steer(session, interaction, opts)
    :ok
  end

  def control(session, {:resize, cols, rows}) when is_integer(cols) and is_integer(rows) do
    Mitos.Session.resize(session, cols, rows)
    :ok
  end

  def control(session, {:signal, signum}) when is_integer(signum) do
    Mitos.Session.signal(session, signum)
    :ok
  end

  def control(session, {:shutdown, grace_ms}) when is_integer(grace_ms) do
    Mitos.Session.shutdown(session, grace_ms)
    :ok
  end

  def control(_session, _command), do: {:error, :unsupported_control}

  @impl true
  def snapshot(session), do: Mitos.Session.snapshot(session)

  @impl true
  def harvest(session) when is_pid(session), do: Mitos.Session.harvest(session)

  def harvest(%{} = session), do: Mitos.Harvest.collect(session)

  def harvest(opts) when is_list(opts), do: Mitos.Harvest.collect(opts)

  def harvest(_session), do: Mitos.Harvest.collect(%{})

  @impl true
  def stop(session) do
    Mitos.Session.shutdown(session)
    :ok
  end

  @impl true
  def capabilities do
    %{
      isolation: :local_process,
      remote?: false,
      egress: :broker,
      data_mount: :bind,
      snapshot?: true,
      net_modes: [:host]
    }
  end

  @impl true
  def child_spec(opts), do: Mitos.Session.child_spec(opts)
end
