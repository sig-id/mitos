defmodule Mitos.Settlement.Mock do
  @moduledoc """
  In-memory x402/USDC settlement (08 §7) for the MVP and tests: escrow deposits
  on verified payment, recorded transfers on split/refund. The X-PAYMENT proof
  is a JSON blob `{"quote_id", "amount_usdc", "payer"}` — a real facilitator
  verifies an on-chain USDC transfer instead; nothing upstream of the
  `Mitos.Settlement` seam changes.
  """
  @behaviour Mitos.Settlement

  use Agent

  def start_link(_opts \\ []) do
    Agent.start_link(fn -> %{escrow: %{}, transfers: []} end, name: __MODULE__)
  end

  @impl true
  def verify(payment, quote) do
    with {:ok, %{} = proof} <- decode(payment),
         true <- proof["quote_id"] == quote.quote_id || {:error, :quote_mismatch},
         true <- (proof["amount_usdc"] || 0) >= quote.amount_usdc || {:error, :underpaid} do
      receipt = %{
        quote_id: quote.quote_id,
        payer: proof["payer"] || "anonymous",
        amount_usdc: proof["amount_usdc"],
        escrow: quote.pay_to
      }

      Agent.update(__MODULE__, fn st ->
        update_in(st, [:escrow, quote.quote_id], &((&1 || 0) + receipt.amount_usdc))
      end)

      {:ok, receipt}
    else
      {:error, reason} -> {:error, reason}
      _ -> {:error, :invalid_payment}
    end
  end

  @impl true
  def transfer(from_escrow, to, amount, memo) do
    tx = "mocktx_" <> Mitos.id()

    Agent.update(__MODULE__, fn st ->
      st
      |> update_in([:escrow, from_escrow], &max((&1 || 0) - amount, 0))
      |> Map.update!(:transfers, &[%{tx: tx, from: from_escrow, to: to, amount: amount, memo: memo} | &1])
    end)

    {:ok, tx}
  end

  def transfers, do: Agent.get(__MODULE__, &Enum.reverse(&1.transfers))

  def escrow(quote_id), do: Agent.get(__MODULE__, &get_in(&1, [:escrow, quote_id])) || 0

  def clear, do: Agent.update(__MODULE__, fn _ -> %{escrow: %{}, transfers: []} end)

  defp decode(payment) when is_binary(payment) do
    case Mitos.JSON.decode(payment) do
      {:ok, %{} = m} ->
        {:ok, m}

      _ ->
        # tolerate base64-wrapped JSON (typical x402 header encoding)
        case Base.decode64(payment) do
          {:ok, raw} -> Mitos.JSON.decode(raw)
          :error -> {:error, :invalid_payment}
        end
    end
  end

  defp decode(_), do: {:error, :invalid_payment}
end
