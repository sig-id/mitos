defmodule Mitos.Eval do
  @moduledoc """
  Replay-as-eval (`16 §6.1`) — the recorded run corpus as a regression suite.

  Failure attribution (model vs. brief vs. environment) is unanswerable without
  replay, and replay is nearly free here because the log is already
  content-addressed and re-drivable (`01 §4`, `04 §7`). So a change to an
  escalation threshold, a reversibility rule, or a classifier is checked against
  what it *would have done* on runs that already happened, before it ships.

  This adds one reader to `02 §1`'s list — the buyer reads a proof, the platform
  reads a meter, the seller reads attribution, the auditor reads the record, and
  **mitos's own CI reads the corpus**. It is the reader no single-tenant
  framework grows, because it needs runs worth replaying and a log designed for
  strangers to read.

  Nothing here spends a token: every question is answered by recomputing a
  deterministic policy over recorded Effects and comparing it to what the log
  says actually happened.
  """

  alias Mitos.Decision.{Card, Policy}
  alias Mitos.Event.Effect
  alias Mitos.Run.State

  @doc "Every run on this control plane, oldest first — the corpus."
  def corpus, do: Mitos.Runs.list() |> Enum.map(& &1.run_id) |> Enum.reverse()

  @doc """
  Replay one run's decision points against the *current* policy.

  Returns `%{run_id, cards, divergences}` where a divergence is a card whose
  reversibility class would be different today — i.e. a rule change that would
  have altered whether, and how hard, a human was interrupted.
  """
  def replay(run_id) do
    st = State.fold(run_id)

    cards =
      st.events
      |> Enum.filter(&match?(%Effect{type: :approval_requested}, &1))
      |> Enum.map(&Card.from_payload(run_id, &1.payload, &1.caused_by))

    divergences =
      cards
      |> Enum.map(&{&1, recomputed_class(&1)})
      |> Enum.reject(fn {card, now} -> is_nil(now) or now == card.reversibility end)
      |> Enum.map(fn {card, now} ->
        %{
          decision_id: card.decision_id,
          question: card.question,
          was: card.reversibility,
          now: now,
          # The direction matters more than the count: `external -> internal`
          # means a rule change would now let silence authorize something a
          # human was once asked about (15 §4).
          direction: direction(card.reversibility, now)
        }
      end)

    %{run_id: run_id, cards: length(cards), divergences: divergences}
  end

  @doc """
  Replay a corpus. `strict: true` treats any loosening divergence as a failure —
  the shape a CI gate wants, since a rule change that quietly stops asking is
  the one that costs something irreversible.
  """
  def replay_all(run_ids \\ nil, opts \\ []) do
    results = Enum.map(run_ids || corpus(), &replay/1)
    divergences = Enum.flat_map(results, & &1.divergences)
    loosened = Enum.filter(divergences, &(&1.direction == :loosened))

    %{
      runs: length(results),
      cards: Enum.reduce(results, 0, &(&1.cards + &2)),
      divergences: divergences,
      loosened: loosened,
      ok: if(opts[:strict], do: loosened == [], else: true),
      per_run: results
    }
  end

  @doc """
  The interrupt distribution (`02 §6.1`) — what fraction of human touches were
  approve-the-boring, missing information, genuine judgment, or a rescue from
  thrash.

  This is build-order item 0: measure before building. The distribution decides
  whether the next thing to build is mostly a gate, a broker, or a supervisor,
  and it is computable from the log that already exists.
  """
  def interrupt_distribution(run_ids \\ nil) do
    events =
      (run_ids || corpus())
      |> Enum.flat_map(&Mitos.Log.read/1)
      |> Enum.filter(&match?(%Effect{}, &1))

    raised = Enum.filter(events, &(&1.type == :approval_requested))
    defaulted = Enum.count(events, &(&1.type == :decision_defaulted))

    buckets =
      Enum.reduce(raised, %{}, fn e, acc ->
        Map.update(acc, bucket(e.payload), 1, &(&1 + 1))
      end)

    total = length(raised)

    %{
      total: total,
      # A card always resolved as its default should not have been raised, so
      # this bucket is the first place to look for rules to delete (15 §7).
      defaulted: defaulted,
      buckets: buckets,
      shares: Map.new(buckets, fn {k, v} -> {k, share(v, total)} end),
      # What the distribution implies about what to build next.
      implication: implication(buckets, total)
    }
  end

  @doc "One-line report for a CI log."
  def report(%{runs: runs, cards: cards, divergences: divs, loosened: loose}) do
    """
    corpus: #{runs} runs, #{cards} decision points
    divergences: #{length(divs)} (#{length(loose)} would now ask LESS)
    #{Enum.map_join(loose, "\n", &"  LOOSENED #{&1.was} -> #{&1.now}: #{&1.question}")}
    """
  end

  # --- internals ---

  # Recompute the class from what the card recorded about its own blast radius.
  # A card carries destinations and paths, not the original act, so this can
  # only speak where the record is rich enough — and says nothing where it is
  # not, rather than inventing a comparison.
  defp recomputed_class(%Card{blast_radius: %{destinations: [_ | _] = dests}}),
    do: Policy.classify(%{kind: :egress, dest: hd(dests)})

  defp recomputed_class(%Card{kind: kind}) when kind in [:amend_brief, :rewind, :abort], do: nil

  defp recomputed_class(%Card{blast_radius: %{paths: [_ | _] = paths}}),
    do: Policy.classify(%{kind: :workspace_write, paths: paths})

  defp recomputed_class(_card), do: nil

  defp direction(was, now) do
    rank = %{internal: 0, recoverable: 1, external: 2}

    cond do
      rank[now] < rank[was] -> :loosened
      rank[now] > rank[was] -> :tightened
      true -> :same
    end
  end

  defp bucket(payload) do
    blocked_by = to_string(get_in(payload, ["context", "blocked_by"]) || "")

    cond do
      payload["default"] != nil -> :approve_the_boring
      payload["kind"] == "supply_value" -> :missing_information
      String.contains?(blocked_by, "exhausted") -> :rescue_from_thrash
      payload["reversibility"] == "external" -> :genuine_judgment
      true -> :other
    end
  rescue
    _ -> :other
  end

  defp share(_v, 0), do: 0.0
  defp share(v, total), do: Float.round(v / total, 3)

  defp implication(_buckets, 0), do: :no_data

  defp implication(buckets, total) do
    frac = fn key -> Map.get(buckets, key, 0) / total end

    cond do
      frac.(:approve_the_boring) > 0.5 -> :build_a_gate
      frac.(:missing_information) > 0.4 -> :build_a_broker
      frac.(:rescue_from_thrash) > 0.3 -> :build_a_supervisor
      true -> :mixed
    end
  end
end
