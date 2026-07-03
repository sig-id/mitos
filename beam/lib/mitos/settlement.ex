defmodule Mitos.Settlement do
  @moduledoc """
  The meter's external rail (08 §7): fold → split → transfer, over x402
  (HTTP 402 + stablecoin) so a stranger with a wallet can pay an endpoint with
  no account. This behaviour is the seam; `Mitos.Settlement.Mock` implements it
  in-memory for the MVP/test loop, a USDC facilitator client implements it for
  production. The quote math, the split math, and delivery judgment all live
  OFF this seam (in `Mitos.Meter`/`Mitos.Facilitator`) — settlement only moves
  what the fold decided.
  """

  @type quote_map :: %{
          quote_id: String.t(),
          amount_usdc: number(),
          pay_to: String.t()
        }

  @doc "Verify a buyer's X-PAYMENT proof against a quote. No payment ⇒ no run (08 §7)."
  @callback verify(payment :: String.t(), quote_map) ::
              {:ok, receipt :: map()} | {:error, term()}

  @doc "Move funds out of escrow: platform take, seller payout, or buyer refund."
  @callback transfer(from_escrow :: String.t(), to :: String.t(), amount :: number(), memo :: map()) ::
              {:ok, tx_ref :: String.t()} | {:error, term()}

  def impl, do: Application.get_env(:mitos, :settlement, Mitos.Settlement.Mock)
end
