defmodule Mitos.Facilitator do
  @moduledoc """
  The Gentix facilitator (08) — the read side of the execution log, made into a
  business: publish a capability as a metered paid endpoint (§8), gate
  admission on x402 payment (§7), execute in the buyer's isolation context
  (§3.1), then settle the meter's fold (§4.2) and render the proof (§5).

  MVP posture (09 §3, flagged judgment call): sellers are operator-vetted and
  runs execute at `config :mitos, :marketplace_tier` (T1, local provider); true
  T2 remote isolation gates OPEN seller onboarding, post-gate (P4). The
  facilitator itself never touches key material — `scope` routes the broker to
  the seller's vault entries (08 §3.2).
  """
  use Agent

  alias Mitos.{Capability, Catalog, Meter, Settlement}

  def start_link(_opts \\ []) do
    Agent.start_link(fn -> %{quotes: %{}, purchases: %{}, settlements: %{}} end, name: __MODULE__)
  end

  # --- seller onboarding (08 §8): recipe → metered endpoint ---

  @doc """
  Publish a capability directory with a flat-price offer:
  `%{price_usd, payout_addr, seller}`. Returns `{:ok, capability_id}`. Identity
  is content (03 §9); the offer is facilitator-side and mutable — repricing
  never mints a new id (12 §8).
  """
  def publish(cap_dir, offer) do
    with {:ok, manifest} <- Capability.Manifest.load(cap_dir) do
      Catalog.put_capability(manifest)

      Catalog.put_offer(manifest.capability_id, %{
        price_usd: offer[:price_usd] || 0.0,
        payout_addr: offer[:payout_addr],
        seller: offer[:seller] || "seller",
        dir: cap_dir,
        name: manifest.name
      })

      {:ok, manifest.capability_id}
    end
  end

  # --- x402 admission (08 §7) ---

  @doc "Step 1: the 402 quote. Flat price up front; the quote is the escrow ceiling."
  def quote(capability_id) do
    case Catalog.get_offer(capability_id) do
      nil ->
        {:error, :not_published}

      offer ->
        q = %{
          quote_id: Mitos.id("quote"),
          amount_usdc: offer.price_usd,
          pay_to: "escrow:gentix",
          capability_id: capability_id
        }

        Agent.update(__MODULE__, &put_in(&1, [:quotes, q.quote_id], q))
        {:ok, q}
    end
  end

  @doc """
  Step 2: verified payment admits the run — no buyer payment ⇒ no run (08 §7).
  The purchase becomes a Task (source `purchase:<id>`, 01 §8.7) executed as a
  pipeline run in the buyer's context with the seller's scope at the broker.
  """
  def purchase(capability_id, payment, inputs) do
    with {:ok, quote} <- find_quote(payment, capability_id),
         offer when not is_nil(offer) <- Catalog.get_offer(capability_id) || {:error, :not_published},
         {:ok, receipt} <- Settlement.impl().verify(payment, quote) do
      buyer_org = "buyer_" <> short(receipt.payer)

      task =
        Mitos.WorkPool.enqueue(
          Mitos.Task.new(
            tenant: %{"org" => buyer_org, "project" => "purchases"},
            source: "purchase:" <> quote.quote_id,
            pool: "paid",
            pipeline: "sell-" <> offer.name,
            payload: %{
              "intent" => "fulfill purchased capability #{offer.name}",
              "inputs" => inputs,
              "workspace" => %{"kind" => "none"}
            },
            budget: %{"usd" => offer.price_usd}
          )
        )

      {:ok, run_id} =
        Mitos.Run.start_run(
          tenant: %{"org" => buyer_org, "project" => "purchases"},
          task_id: task.id,
          pipeline: sale_pipeline(offer),
          capabilities: %{offer.name => offer.dir},
          inputs: inputs,
          # the buyer's prepaid amount is the run's hard ceiling (08 §3.1)
          budget: %{"usd" => offer.price_usd},
          scope: offer.seller,
          trust_tier: Application.get_env(:mitos, :marketplace_tier, "T1"),
          offer_key: quote.quote_id
        )

      Mitos.WorkPool.update_status(task.id, :running)

      Agent.update(
        __MODULE__,
        &put_in(&1, [:purchases, quote.quote_id], %{
          quote: quote,
          receipt: receipt,
          capability_id: capability_id,
          run_id: run_id,
          task_id: task.id
        })
      )

      {:ok, %{run_id: run_id, task_id: task.id, quote_id: quote.quote_id}}
    else
      {:error, reason} -> {:error, reason}
      nil -> {:error, :not_published}
    end
  end

  # --- settlement: fold → split → transfer (08 §7), called at run end ---

  def settle(run_id, quote_id, _final_state) do
    case Agent.get(__MODULE__, &get_in(&1, [:purchases, quote_id])) do
      nil ->
        :ok

      purchase ->
        offer = Catalog.get_offer(purchase.capability_id)
        charge = Meter.charge(run_id, offer)
        rail = Settlement.impl()
        txs = []

        txs =
          if charge.seller_net > 0 do
            {:ok, tx} =
              rail.transfer(quote_id, offer.payout_addr, charge.seller_net, %{
                run_id: run_id,
                kind: :seller_payout
              })

            [%{kind: :seller_payout, tx: tx, amount: charge.seller_net} | txs]
          else
            txs
          end

        txs =
          if charge.platform_take > 0 do
            {:ok, tx} =
              rail.transfer(quote_id, "gentix:take", charge.platform_take, %{
                run_id: run_id,
                kind: :platform_take
              })

            [%{kind: :platform_take, tx: tx, amount: charge.platform_take} | txs]
          else
            txs
          end

        txs =
          if charge.buyer_refund > 0 do
            {:ok, tx} =
              rail.transfer(quote_id, purchase.receipt.payer, charge.buyer_refund, %{
                run_id: run_id,
                kind: :buyer_refund
              })

            [%{kind: :buyer_refund, tx: tx, amount: charge.buyer_refund} | txs]
          else
            txs
          end

        record = %{
          charge: charge,
          transfers: Enum.reverse(txs),
          quote_id: quote_id,
          capability_id: purchase.capability_id,
          seller: offer.seller
        }

        Agent.update(__MODULE__, &put_in(&1, [:settlements, run_id], record))
        :ok
    end
  rescue
    # Settlement must never take the run process down with it; the log is
    # intact and the fold is re-runnable (08 §4: metering is a pure read).
    e ->
      require Logger
      Logger.error("settlement failed for #{run_id}: #{Exception.message(e)}")
      :error
  end

  def settlement(run_id), do: Agent.get(__MODULE__, &get_in(&1, [:settlements, run_id]))

  @doc "All settlements, keyed by run_id — the seller attribution read (08 §6)."
  def settlements, do: Agent.get(__MODULE__, & &1.settlements)

  def purchase_for_run(run_id) do
    Agent.get(__MODULE__, fn st ->
      Enum.find_value(st.purchases, fn {_qid, p} -> if p.run_id == run_id, do: p end)
    end)
  end

  def clear,
    do: Agent.update(__MODULE__, fn _ -> %{quotes: %{}, purchases: %{}, settlements: %{}} end)

  # --- internals ---

  defp find_quote(payment, capability_id) do
    quote_id =
      case Mitos.JSON.decode(payment) do
        {:ok, %{"quote_id" => qid}} ->
          qid

        _ ->
          case Base.decode64(payment) do
            {:ok, raw} ->
              case Mitos.JSON.decode(raw) do
                {:ok, %{"quote_id" => qid}} -> qid
                _ -> nil
              end

            :error ->
              nil
          end
      end

    case quote_id && Agent.get(__MODULE__, &get_in(&1, [:quotes, quote_id])) do
      %{capability_id: ^capability_id} = q -> {:ok, q}
      _ -> {:error, :no_quote}
    end
  end

  # The generated one-stage pipeline for a flat-price sale (08 §9: single
  # capability proves the loop). A user always runs a pipeline, never a bare
  # capability (01 §8.8).
  defp sale_pipeline(offer) do
    """
    # Pipeline: sell-#{offer.name}

    Goal: fulfill one purchased #{offer.name} job for the buyer using the
    provided inputs, producing the declared RESULT and evidence.

    ## Stages
    1. **#{offer.name}** — execute the capability against the buyer's inputs.
    """
  end

  defp short(payer) do
    payer |> to_string() |> String.replace(~r/[^a-zA-Z0-9]/, "") |> String.slice(0, 12)
  end
end
