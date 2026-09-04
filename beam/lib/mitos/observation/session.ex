defmodule Mitos.Observation.Session do
  @moduledoc """
  Channel C2 (14 §1.1) — a tail of the harness's **own** session artifact,
  normalized into the one event vocabulary (`01 §8.6`).

  This is the unlock that removes the mode-change constraint. `05 §6` already
  redirects `HOME` and the harness's config/session store to mitos-controlled
  paths inside the sandbox, for hermeticity. The consequence was not previously
  drawn: because mitos *chooses where the harness writes its session record*, it
  can tail that record — and a modern harness writes a structured, append-only
  session log **in interactive mode**.

  So: one normalized stream, two possible sources — headless stdout for a Market
  run, this tail for a Studio run against an unmodified interactive harness —
  and nothing downstream of the adapter can tell which (`05 §5.2`).

  The tail is append-only and offset-tracked, so a restart resumes rather than
  re-emitting, and every append bumps the quiescence clock (14 §2): in a
  headless run with no PTY, session appends and workspace writes are the only
  evidence that a thinking harness is alive rather than blocked.
  """

  use GenServer

  alias Mitos.Observation.Clock

  @default_poll_ms 100

  # --- client ---

  @doc """
  Start tailing. Required: `harness_mod`, `sandbox`. Optional: `clock`,
  `on_event` (`fun(event)`), `poll_ms`, `run_id`, `stage_id`.

  Returns `{:error, :no_session_artifacts}` when the adapter declares none —
  the caller then has only C1 (+C3), which `14 §4` says MUST NOT be granted an
  unattended horizon.
  """
  def start_link(opts) do
    case descriptor(opts[:harness_mod]) do
      nil -> {:error, :no_session_artifacts}
      _desc -> GenServer.start_link(__MODULE__, opts, Keyword.take(opts, [:name]))
    end
  end

  @doc "Events normalized so far, oldest-first."
  def events(session), do: GenServer.call(session, :events)

  @doc "The artifact currently being tailed, or nil if it has not appeared yet."
  def artifact(session), do: GenServer.call(session, :artifact)

  @doc "Drain pending appends now (rather than waiting for the next poll)."
  def sync(session), do: GenServer.call(session, :sync)

  @doc """
  Expand a `path_template` from the observation matrix (14 §4) against a
  prepared sandbox. Supported vars: `{home}`, `{config}`, `{workspace}`,
  `{project_slug}`. The result is a glob — a harness may keep several sessions.
  """
  def resolve(template, sandbox) do
    home = sandbox[:home] || sandbox["home"] || ""
    workspace = sandbox[:workspace] || sandbox["workspace"] || ""

    template
    |> String.replace("{home}", home)
    |> String.replace("{config}", sandbox[:config_dir] || Path.join(home, ".config"))
    |> String.replace("{workspace}", workspace)
    |> String.replace("{project_slug}", project_slug(workspace))
  end

  @doc """
  The path-slug convention harnesses use to key a session by project directory
  (`/home/x/proj` -> `-home-x-proj`).
  """
  def project_slug(workspace) do
    workspace
    |> to_string()
    |> String.replace(~r{[/._]}, "-")
  end

  # --- server ---

  @impl true
  def init(opts) do
    st = %{
      harness_mod: opts[:harness_mod],
      sandbox: opts[:sandbox] || %{},
      clock: opts[:clock],
      on_event: opts[:on_event],
      run_id: opts[:run_id],
      stage_id: opts[:stage_id],
      poll_ms: opts[:poll_ms] || poll_ms(),
      desc: descriptor(opts[:harness_mod]),
      path: nil,
      offset: 0,
      buffer: "",
      events: []
    }

    {:ok, schedule(st)}
  end

  @impl true
  def handle_call(:events, _from, st), do: {:reply, st.events, st}
  def handle_call(:artifact, _from, st), do: {:reply, st.path, st}

  def handle_call(:sync, _from, st) do
    st = drain(st)
    {:reply, st.events, st}
  end

  @impl true
  def handle_info(:poll, st), do: {:noreply, st |> drain() |> schedule()}

  defp schedule(st) do
    Process.send_after(self(), :poll, st.poll_ms)
    st
  end

  defp drain(st) do
    case locate(st) do
      nil ->
        st

      path ->
        st = if path == st.path, do: st, else: %{st | path: path, offset: 0, buffer: ""}
        read_from(st, path)
    end
  end

  # Newest matching artifact wins: a harness may leave older sessions in place,
  # and this run's is the one it just created.
  defp locate(%{path: path} = st) do
    case Path.wildcard(resolve(st.desc.path_template, st.sandbox)) do
      [] -> path
      matches -> Enum.max_by(matches, &mtime/1)
    end
  end

  defp mtime(path) do
    case File.stat(path, time: :posix) do
      {:ok, %{mtime: m}} -> m
      _ -> 0
    end
  end

  defp read_from(st, path) do
    with {:ok, %{size: size}} when size > st.offset <- File.stat(path),
         {:ok, device} <- :file.open(String.to_charlist(path), [:read, :raw, :binary]),
         {:ok, chunk} <- pread(device, st.offset, size - st.offset) do
      :file.close(device)
      if st.clock, do: Clock.bump(st.clock, :session, byte_size(chunk))

      {lines, buffer} = split_lines(st.buffer <> chunk)
      new_events = Enum.flat_map(lines, &normalize(st, &1))
      Enum.each(new_events, fn e -> if st.on_event, do: st.on_event.(e) end)

      %{st | offset: size, buffer: buffer, events: st.events ++ new_events}
    else
      _ -> st
    end
  end

  defp pread(device, offset, len) do
    case :file.pread(device, offset, len) do
      {:ok, data} -> {:ok, data}
      :eof -> {:ok, ""}
      other -> other
    end
  end

  # The adapter owns its own format: a session artifact is a different record
  # from a headless stdout frame even when they rhyme, so the adapter says which
  # normalizer applies (14 §4).
  defp normalize(st, line) do
    mod = st.harness_mod

    if function_exported?(mod, :normalize_session_line, 1) do
      mod.normalize_session_line(line)
    else
      []
    end
  rescue
    _ -> []
  end

  defp split_lines(buf) do
    parts = String.split(buf, "\n")
    {rest, lines} = List.pop_at(parts, -1)
    {Enum.reject(lines, &(&1 == "")), rest || ""}
  end

  defp descriptor(nil), do: nil

  defp descriptor(harness_mod) do
    case harness_mod.observation() do
      %{session_artifacts: %{} = d} -> Map.put_new(d, :path_template, nil)
      _ -> nil
    end
  rescue
    UndefinedFunctionError -> nil
  end

  defp poll_ms,
    do: Application.get_env(:mitos, :observation, []) |> Keyword.get(:session_poll_ms, @default_poll_ms)
end
