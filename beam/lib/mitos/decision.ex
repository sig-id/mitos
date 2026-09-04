defmodule Mitos.Decision do
  @moduledoc """
  The decision driver (`15`) — raise a card, deliver it, and make silence mean
  something definite.

  Three behaviours this owns, each of which is a defect if it is missing:

    * **Silence advances or explicitly holds.** A reversible card carries a
      default on a clock; when the clock runs out the run proceeds and the log
      records `decision_defaulted` *distinctly* from a human answer — forever,
      because `decision_precision` (`16 §6`) is computed from that difference
      and is what tells you which escalation rules to delete.
    * **An `external` card never expires.** No timer is armed for one; it parks
      until a person answers. Fail-secure (`15 §4`).
    * **Identical cards collapse.** One `dedupe_key`, one delivery, one answer
      that resolves every member (`15 §5.3`) — the difference between three
      workers hitting the same wall and three pushes to a phone.

  Delivery goes out through the `decide` sink and answers come back through the
  `decision` connector (`10 §5.3a`): the round trip the I/O plane already had,
  with a card as the payload. The transport is untrusted — a card's authority
  lives on the log, and a resolution is re-checked against the run's grants
  before it is applied.
  """

  use GenServer

  alias Mitos.Decision.Card

  # --- client ---

  def start_link(_opts \\ []), do: GenServer.start_link(__MODULE__, %{}, name: __MODULE__)

  @doc """
  Raise a card: append `approval_requested` through the run's single writer,
  deliver it, and arm the TTL if the class allows one.
  """
  def raise_card(%Card{} = card, opts \\ []), do: GenServer.call(__MODULE__, {:raise, card, opts})

  @doc """
  Apply a human resolution. Rejects an unauthenticated or out-of-tenant
  resolver with `decision_rejected` (never an application), and is idempotent on
  `decision_id` — the first valid answer wins and later ones are recorded.
  """
  def resolve(decision_id, resolution), do: GenServer.call(__MODULE__, {:resolve, decision_id, resolution})

  @doc "Open cards, newest last. `run_id` filters to one run."
  def open(run_id \\ nil), do: GenServer.call(__MODULE__, {:open, run_id})

  @doc "Fetch one open card."
  def fetch(decision_id), do: GenServer.call(__MODULE__, {:fetch, decision_id})

  @doc "Cards below the escalation floor, accumulated for the digest (15 §5.3)."
  def digest(tenant \\ nil), do: GenServer.call(__MODULE__, {:digest, tenant})

  def clear, do: GenServer.call(__MODULE__, :clear)

  # --- server ---

  @impl true
  def init(_), do: {:ok, %{cards: %{}, dedupe: %{}, digest: [], timers: %{}}}

  @impl true
  def handle_call({:raise, card, opts}, _from, st) do
    case Map.get(st.dedupe, card.dedupe_key) do
      # 15 §5.3: an identical open card already exists. Join it rather than
      # pushing again; its resolution will resolve this member too.
      [_ | _] = members ->
        st = put_in(st.dedupe[card.dedupe_key], members ++ [card.decision_id])
        st = put_in(st.cards[card.decision_id], %{card: card, opts: opts, joined: true})
        record(card, %{"deduped_into" => hd(members)})
        {:reply, {:ok, :deduped}, st}

      _ ->
        st = do_raise(st, card, opts)
        {:reply, {:ok, card.decision_id}, st}
    end
  end

  def handle_call({:resolve, decision_id, resolution}, _from, st) do
    case Map.fetch(st.cards, decision_id) do
      :error ->
        {:reply, {:error, :unknown_decision}, st}

      {:ok, %{card: card}} ->
        case authorize(card, resolution) do
          :ok ->
            {:reply, :ok, apply_resolution(st, card, normalize_resolution(resolution))}

          {:error, reason} ->
            emit(card, :decision_rejected, %{
              "reason" => to_string(reason),
              "resolver" => resolution[:resolver] || resolution["resolver"]
            })

            {:reply, {:error, reason}, st}
        end
    end
  end

  def handle_call({:open, run_id}, _from, st) do
    cards =
      st.cards
      |> Map.values()
      |> Enum.map(& &1.card)
      |> Enum.filter(&(is_nil(run_id) or &1.run_id == run_id))
      |> Enum.sort_by(& &1.raised_at)

    {:reply, cards, st}
  end

  def handle_call({:fetch, id}, _from, st) do
    {:reply, (Map.get(st.cards, id) || %{})[:card], st}
  end

  def handle_call({:digest, tenant}, _from, st) do
    items = Enum.filter(st.digest, &(is_nil(tenant) or org(&1.tenant) == org(tenant)))
    {:reply, Enum.reverse(items), st}
  end

  def handle_call(:clear, _from, st) do
    Enum.each(st.timers, fn {_id, ref} -> Process.cancel_timer(ref) end)
    {:reply, :ok, %{st | cards: %{}, dedupe: %{}, digest: [], timers: %{}}}
  end

  @impl true
  def handle_info({:expire, decision_id}, st) do
    case Map.fetch(st.cards, decision_id) do
      :error ->
        {:noreply, st}

      {:ok, %{card: card}} ->
        # 15 §4: the default fires, and the log keeps the fact that nobody
        # answered — `decision_defaulted`, never dressed up as a resolution.
        emit(card, :decision_defaulted, %{
          "decision_id" => card.decision_id,
          "choice" => card.default,
          "ttl_ms" => card.ttl_ms
        })

        {:noreply,
         apply_resolution(st, card, %{
           resolver: ":default_expired",
           choice: card.default,
           via: ":default_expired",
           note: nil,
           value: nil
         })}
    end
  end

  def handle_info(_msg, st), do: {:noreply, st}

  # --- internals ---

  defp do_raise(st, card, opts) do
    record(card, %{})

    st = put_in(st.cards[card.decision_id], %{card: card, opts: opts, joined: false})
    st = put_in(st.dedupe[card.dedupe_key], [card.decision_id])

    st =
      if push?(card) do
        deliver(card, opts)
        st
      else
        # Below the floor: it accumulates into a digest to read, not a push to
        # answer (15 §5.3).
        %{st | digest: [card | st.digest]}
      end

    arm_ttl(st, card)
  end

  # An `external` card gets no timer at all — the absence of the timer IS the
  # fail-secure guarantee, not a check somewhere that a timer should be ignored.
  defp arm_ttl(st, %Card{ttl_ms: nil}), do: st

  defp arm_ttl(st, %Card{} = card) do
    ref = Process.send_after(self(), {:expire, card.decision_id}, card.ttl_ms)
    put_in(st.timers[card.decision_id], ref)
  end

  defp apply_resolution(st, card, res) do
    members = Map.get(st.dedupe, card.dedupe_key, [card.decision_id])

    Enum.each(members, fn id ->
      case Map.fetch(st.cards, id) do
        {:ok, %{card: member}} ->
          emit(member, :approval_resolved, %{
            "decision_id" => member.decision_id,
            "resolver" => res.resolver,
            "choice" => res.choice,
            "value" => res.value,
            "note" => res.note,
            "via" => res.via,
            "resolved_at" => Mitos.ts()
          })

        :error ->
          :ok
      end
    end)

    Enum.each(members, fn id ->
      case Map.get(st.timers, id) do
        nil -> :ok
        ref -> Process.cancel_timer(ref)
      end
    end)

    %{
      st
      | cards: Map.drop(st.cards, members),
        dedupe: Map.delete(st.dedupe, card.dedupe_key),
        timers: Map.drop(st.timers, members)
    }
  end

  # The transport is untrusted (15 §5.2): the card's authority is on the log,
  # so a resolution is checked against the run's tenant before it is applied.
  defp authorize(card, resolution) do
    resolver = resolution[:resolver] || resolution["resolver"]
    choice = to_string(resolution[:choice] || resolution["choice"] || "")

    cond do
      is_nil(resolver) or resolver == "" -> {:error, :unauthenticated_resolver}
      not tenant_match?(card, resolution) -> {:error, :out_of_tenant}
      choice == "" -> {:error, :no_choice}
      card.options != [] and not Enum.any?(card.options, &(&1.id == choice)) -> {:error, :not_an_option}
      true -> :ok
    end
  end

  defp tenant_match?(card, resolution) do
    case resolution[:tenant] || resolution["tenant"] do
      nil -> true
      t -> org(t) == org(card.tenant)
    end
  end

  defp normalize_resolution(r) do
    %{
      resolver: r[:resolver] || r["resolver"],
      choice: to_string(r[:choice] || r["choice"]),
      value: r[:value] || r["value"],
      note: r[:note] || r["note"],
      via: to_string(r[:via] || r["via"] || "unknown")
    }
  end

  defp push?(%Card{reversibility: :external}), do: true

  defp push?(%Card{} = card) do
    case Mitos.Decision.Policy.escalation_floor(card.tenant) do
      :internal -> true
      :recoverable -> card.reversibility != :recoverable
      _ -> true
    end
  end

  # Every card and every resolution is an Effect on the run's own log — no
  # parallel ledger (01 §4).
  defp record(card, extra) do
    append(card, :approval_requested, Map.merge(Card.to_payload(card), extra))
  end

  defp emit(card, type, payload), do: append(card, type, payload)

  # A card outlives the run process that raised it: an `external` card parks
  # indefinitely and a TTL can fire long after the run released its writer. So
  # route through the run while it holds the writer, and append detached when it
  # does not — losing these records would destroy `decision_precision` (16 §6),
  # which is the one metric that says which interruptions to stop making.
  defp append(card, type, payload) do
    fields = %{
      type: type,
      caused_by: card.caused_by,
      ref: %{"decision_id" => card.decision_id, "stage_id" => card.stage_id},
      payload: payload
    }

    # `Run.append_effect/2` routes to the live writer and falls back to a
    # detached append when the run has released it — a card outlives the run
    # process that raised it, and an `external` card parks indefinitely.
    Mitos.Run.append_effect(card.run_id, fields)
  end

  defp deliver(card, opts) do
    sink = opts[:sink] || Mitos.Sink.Decide
    binding = opts[:binding] || %{to: opts[:to] || "operator"}

    with {:ok, handle} <- sink.bind(binding),
         {:ok, payload} <- sink.render(%{card: card, cut: opts[:cut] || :dev}, binding),
         {:ok, receipt} <- sink.deliver(handle, payload) do
      emit(card, :decision_delivered, %{
        "sink" => inspect(sink),
        "dest" => to_string(binding[:to]),
        "receipt" => inspect(receipt)
      })
    else
      {:error, reason} ->
        emit(card, :decision_delivered, %{"error" => inspect(reason)})
    end
  end

  defp org(nil), do: nil
  defp org(%{"org" => o}), do: o
  defp org(%{org: o}), do: o
  defp org(other), do: other
end
