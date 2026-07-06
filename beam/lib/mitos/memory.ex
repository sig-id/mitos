defmodule Mitos.Memory do
  @moduledoc """
  The cross-run knowledge service (07) — OS-owned, two-track memory with
  write-time contradiction-supersession. Memory is the substrate hook named in
  `01 §3`; it is NOT a parallel store: writes are `memory_written` Effects on
  the run log (01 §8.2), and retrieval produces the `/ctx` slices injected into
  a capability's prompt (07 §3, 03 §2).

  Two tracks (07 §2):

    * `:ephemeral` — working notes for an in-flight run (scratch, plan, todo);
      scoped to the run, not carried forward unless promoted.
    * `:long_term` — distilled lessons that survive across runs (the security
      profile, the verified fix patterns). Supersession is contradiction-based:
      a new entry with the same `(scope, key)` SUPERSEDES the prior, which is
      retained for audit but no longer retrieved.

  Verify→distill (07 §4, 04 §10): after a run whose `result.signal` is verified,
  `distill/2` mines the run's harvested result + evidence for lessons and writes
  them long-term. The verify signal is reused as the self-improvement signal —
  nothing distills from an unverified run.

  In-memory for the substrate; a hosted backing store plugs in behind the same
  `write`/`retrieve`/`distill` surface.
  """
  use GenServer

  @enforce_keys [:scope, :track, :key, :content]
  defstruct [:id, :scope, :track, :key, :content, :stance, :ts, :superseded_by]

  ## client API

  def start_link(_opts \\ []), do: GenServer.start_link(__MODULE__, %{}, name: __MODULE__)

  @doc "Write a memory entry. Returns the entry; supersession is recorded, not deleted."
  def write(scope, key, content, opts \\ []) do
    GenServer.call(__MODULE__, {:write, scope, key, content, opts})
  end

  @doc "Retrieve context slices for injection into a prompt (07 §3). `opts[:track]`, `opts[:limit]`."
  def retrieve(scope, query, opts \\ []) do
    GenServer.call(__MODULE__, {:retrieve, scope, query, opts})
  end

  @doc "All non-superseded entries for a scope (the profile read)."
  def profile(scope), do: GenServer.call(__MODULE__, {:profile, scope})

  def clear, do: GenServer.call(__MODULE__, :clear)

  @doc "Effect fields for a memory_written event (the caller — the run — appends it, 01 §8.2)."
  def effect_fields(%__MODULE__{} = e) do
    [
      type: :memory_written,
      ref: %{"track" => e.track, "key" => e.key, "memory_id" => e.id, "supersedes" => e.superseded_by},
      payload: %{"scope" => e.scope, "stance" => e.stance, "content" => e.content}
    ]
  end

  @doc """
  Verify→distill (04 §10, 07 §4): from a verified run's harvested result +
  evidence, mine lessons and write them long-term. Returns the entries written
  (possibly []). The caller appends a `memory_written` Effect per entry; this
  function only derives + stores them — keeping it a pure-ish fold over the run.
  """
  def distill(run_id, opts \\ []) do
    scope = opts[:scope] || run_id |> String.split("/") |> hd()
    result = Mitos.Proof.deliverable(run_id)
    signal = get_in(result, ["result", "signal"])

    if signal in ["ship", "pass"] do
      summary = get_in(result, ["result", "summary"]) || ""
      evidence = Enum.map_join(result["artifacts"] || [], "\n", & &1["path"])

      lessons =
        case opts[:lesson_extractor] do
          fun when is_function(fun, 1) -> fun.(result)
          nil -> default_lessons(summary, evidence)
        end

      Enum.map(lessons, fn {key, content} ->
        write(scope, key, content, track: :long_term, stance: signal)
      end)
    else
      []
    end
  end

  defp default_lessons(summary, evidence) do
    # Minimal deterministic extraction: the run's summary becomes a "last_run"
    # lesson; non-empty evidence becomes an "evidence_shape" note. A real
    # distill uses the judge (04 §10); this is the substrate fallback.
    lessons = [{:"profile.last_run", "verified run: #{summary}"}]

    if evidence != "",
      do: lessons ++ [{:"profile.evidence_shape", "evidence produced: #{evidence}"}],
      else: lessons
  end

  ## server

  @impl true
  def init(_), do: {:ok, %{entries: %{}, by_scope: %{}}}

  @impl true
  def handle_call({:write, scope, key, content, opts}, _from, st) do
    track = Keyword.get(opts, :track, :ephemeral)
    stance = Keyword.get(opts, :stance)
    id = Mitos.id("mem")
    now = Mitos.ts()

    # contradiction-supersession: a new entry at (scope,key) on the long-term
    # track supersedes the prior non-superseded entry at that key.
    superseded =
      if track == :long_term do
        find_current(st, scope, key)
      else
        nil
      end

    entry = %__MODULE__{
      id: id,
      scope: scope,
      track: track,
      key: key,
      content: content,
      stance: stance,
      ts: now,
      superseded_by: nil
    }

    st =
      if superseded do
        mark_superseded(st, superseded.id, id)
      else
        st
      end

    st = put_in(st, [:entries, id], entry)
    st = update_in(st, [:by_scope, scope], fn list -> [entry | list || []] end)
    {:reply, entry, st}
  end

  def handle_call({:retrieve, scope, query, opts}, _from, st) do
    track = Keyword.get(opts, :track)
    limit = Keyword.get(opts, :limit, 8)
    q = String.downcase(to_string(query))

    slices =
      st
      |> entries_for(scope)
      |> Enum.reject(& &1.superseded_by)
      |> maybe_filter_track(track)
      |> Enum.filter(&(track == :ephemeral or ranked?(&1, q)))
      |> Enum.sort_by(& &1.ts, {:desc, Date})
      |> Enum.take(limit)
      |> Enum.map(& &1.content)

    {:reply, slices, st}
  end

  def handle_call({:profile, scope}, _from, st) do
    entries =
      st
      |> entries_for(scope)
      |> Enum.reject(& &1.superseded_by)
      |> Enum.sort_by(& &1.key)

    {:reply, entries, st}
  end

  def handle_call(:clear, _from, _st), do: {:reply, :ok, %{entries: %{}, by_scope: %{}}}

  defp entries_for(st, scope) do
    st.entries
    |> Map.values()
    |> Enum.filter(&(&1.scope == scope))
  end
  defp maybe_filter_track(entries, nil), do: entries
  defp maybe_filter_track(entries, track), do: Enum.filter(entries, &(&1.track == track))
  defp ranked?(%{content: c, key: k}, q), do: String.contains?(String.downcase(c), q) or String.contains?(to_string(k), q)

  defp find_current(st, scope, key) do
    st
    |> entries_for(scope)
    |> Enum.find(&(&1.key == key and is_nil(&1.superseded_by) and &1.track == :long_term))
  end

  defp mark_superseded(st, old_id, new_id) do
    update_in(st, [:entries, old_id], fn e -> %{e | superseded_by: new_id} end)
  end
end
