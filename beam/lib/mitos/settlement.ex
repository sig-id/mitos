defmodule Mitos.Settlement do
  @moduledoc """
  Minimal x402-style admission and settlement math for flat-priced Gentix runs.

  This module deliberately keeps the external rail pluggable: it verifies a
  payment proof shape from `X-PAYMENT`, gates execution, and computes the split
  that a real USDC settlement worker would transfer.
  """

  @currency "USDC"
  @default_escrow "usdc:escrow:dev"

  @doc "Return true when a workload is published as a paid endpoint."
  def paid?(%Mitos.Workload{pricing: pricing}), do: is_map(pricing)

  @doc "Build the quote a buyer must satisfy before a paid run is admitted."
  def payment_quote(%Mitos.Workload{} = workload, request_body \\ %{}) do
    pricing = workload.pricing || %{}
    amount_usdc = pricing["amount_usdc"]
    pay_to = escrow_address()

    quote = %{
      "schema_id" => "x402.payment_required",
      "schema_version" => 1,
      "quote_id" => quote_id(workload, request_body, pay_to),
      "workload_id" => workload.id,
      "amount_usdc" => amount_usdc,
      "currency" => @currency,
      "pay_to" => pay_to,
      "resource" => "/capabilities/#{workload.id}/runs"
    }

    if workload.payout_address do
      Map.put(quote, "seller_payout_address", workload.payout_address)
    else
      quote
    end
  end

  @doc """
  Verify the request's payment proof.

  A missing or invalid proof returns a 402-ready quote. The accepted proof can be
  JSON or base64-encoded JSON and must bind to this exact request quote.
  """
  def admit(%Mitos.Workload{} = workload, request_body, payment_header) do
    cond do
      not paid?(workload) ->
        {:error, :payment_not_configured}

      payment_header in [nil, ""] ->
        {:error, {:payment_required, payment_quote(workload, request_body)}}

      true ->
        quote = payment_quote(workload, request_body)

        with {:ok, proof} <- decode_payment(payment_header),
             :ok <- verify_payment(proof, quote) do
          {:ok, receipt(proof, quote)}
        else
          {:error, reason} -> {:error, {:payment_invalid, quote, reason}}
          {:invalid, reason} -> {:error, {:payment_invalid, quote, reason}}
        end
    end
  end

  @doc "Compute the paid-run split from the proof metering fold."
  def settle(%Mitos.Workload{} = workload, receipt, proof) when is_map(receipt) do
    pricing = workload.pricing || %{}
    escrowed = amount_micros!(pricing["amount_usdc"])
    take_rate_bps = pricing["take_rate_bps"] || 1500
    take_floor = take_floor_micros!(pricing)
    compute_surcharge = pricing |> compute_surcharge_micros() |> min(escrowed)
    status = delivery_status(proof)

    buyer_charge =
      if status == "settled",
        do: buyer_charge_micros(pricing, proof),
        else: compute_surcharge

    buyer_charge = min(buyer_charge, escrowed)

    platform_take =
      if status == "settled",
        do:
          buyer_charge
          |> Kernel.-(compute_surcharge)
          |> max(0)
          |> platform_take_micros(take_rate_bps, take_floor),
        else: 0

    seller_net = max(buyer_charge - platform_take - compute_surcharge, 0)

    %{
      "schema_id" => "gentix.settlement",
      "schema_version" => 1,
      "status" => status,
      "quote_id" => receipt["quote_id"],
      "settlement_tx" => receipt["settlement_tx"],
      "currency" => @currency,
      "escrowed_usdc" => format_micros(escrowed),
      "buyer_charge_usdc" => format_micros(buyer_charge),
      "refund_due_usdc" => format_micros(max(escrowed - buyer_charge, 0)),
      "compute_surcharge_usdc" => format_micros(compute_surcharge),
      "platform_take_usdc" => format_micros(platform_take),
      "seller_net_usdc" => format_micros(seller_net),
      "seller_payout_address" => workload.payout_address,
      "pay_to" => receipt["pay_to"],
      "proof_hash" => proof["effect_log_sha256"]
    }
  end

  @doc "Decision budget grant for an admitted paid run."
  def budget_from_receipt(receipt) when is_map(receipt) do
    amount =
      receipt["amount_usdc"] || receipt["paid_usdc"] || receipt[:amount_usdc] ||
        receipt[:paid_usdc]

    micros = amount_micros!(amount)
    usd = micros / 1_000_000

    %{
      "tokens" => 0,
      "usd" => usd,
      "wall_ms" => 0,
      "remaining" => %{"usd" => usd}
    }
  end

  @doc """
  Persist the authoritative money line for a paid run.

  `budget_charged` is the canonical billing-ledger Effect (`08 §2`). Recording
  is idempotent for a quote so response retries do not double-count the charge.
  """
  def record_charge!(run_id, %Mitos.Workload{} = workload, receipt, opts \\ [])
      when is_binary(run_id) and is_map(receipt) do
    events = Mitos.Trace.read_events(run_id, opts)
    quote_id = receipt["quote_id"]
    proof = Mitos.Proof.from_events(events)
    buyer_charge_micros = ledger_charge_micros(workload.pricing || %{}, proof)
    settlement = settlement_ledger_payload(workload, receipt, proof)

    unless charge_recorded?(events, quote_id) do
      trace =
        Mitos.Trace.open!(
          [
            run_id: run_id,
            launch_spec: %{"provider" => workload.provider, "task" => workload.id}
          ]
          |> maybe_put(:base_dir, opts[:base_dir])
        )

      {trace, caused_by} =
        settlement_decision(trace, events, workload, receipt, buyer_charge_micros)

      Mitos.Trace.append_effect(trace, %{
        type: "budget_charged",
        caused_by: caused_by,
        ref: %{
          "quote_id" => quote_id,
          "resource" => "/capabilities/#{workload.id}/runs",
          "settlement_tx" => receipt["settlement_tx"],
          "pricing_type" => workload.pricing["type"] || "flat"
        },
        payload: %{
          "billing_basis" => billing_basis(workload.pricing),
          "currency" => @currency,
          "tokens" => 0,
          "wall_ms" => 0,
          "usd" => buyer_charge_micros / 1_000_000,
          "usd_micros" => buyer_charge_micros,
          "amount_usdc" => format_micros(buyer_charge_micros),
          "settlement" => settlement
        }
      })
    end

    :ok
  end

  @doc false
  def amount_micros(value) when is_integer(value) and value >= 0, do: {:ok, value * 1_000_000}

  def amount_micros(value) when is_float(value) and value >= 0 do
    {:ok, round(value * 1_000_000)}
  end

  def amount_micros(value) when is_binary(value) do
    value = String.trim(value)

    with [whole, fraction] <- decimal_parts(value),
         {whole_int, ""} <- Integer.parse(whole),
         true <- whole_int >= 0,
         {:ok, fraction_int} <- parse_fraction(fraction) do
      {:ok, whole_int * 1_000_000 + fraction_int}
    else
      _ -> {:error, :invalid_amount}
    end
  end

  def amount_micros(_value), do: {:error, :invalid_amount}

  defp verify_payment(proof, quote) do
    cond do
      field(proof, "quote_id") != quote["quote_id"] ->
        {:invalid, "quote_id mismatch"}

      field(proof, "currency") not in [nil, @currency] ->
        {:invalid, "currency mismatch"}

      field(proof, "pay_to") != quote["pay_to"] ->
        {:invalid, "pay_to mismatch"}

      not settled?(proof) ->
        {:invalid, "payment is not settled"}

      settlement_tx(proof) in [nil, ""] ->
        {:invalid, "settlement transaction is required"}

      true ->
        with {:ok, paid} <- amount_micros(field(proof, "amount_usdc") || field(proof, "amount")),
             {:ok, required} <- amount_micros(quote["amount_usdc"]) do
          if paid >= required, do: :ok, else: {:invalid, "amount is below quote"}
        end
    end
  end

  defp receipt(proof, quote) do
    paid = field(proof, "amount_usdc") || field(proof, "amount")

    %{
      "schema_id" => "x402.payment_receipt",
      "schema_version" => 1,
      "quote_id" => quote["quote_id"],
      "amount_usdc" => quote["amount_usdc"],
      "paid_usdc" => to_string(paid),
      "currency" => @currency,
      "pay_to" => quote["pay_to"],
      "settlement_tx" => settlement_tx(proof),
      "verified" => true
    }
  end

  defp decode_payment(header) when is_binary(header) do
    header = String.trim(header)

    case Jason.decode(header) do
      {:ok, %{} = proof} ->
        {:ok, proof}

      _ ->
        with {:ok, decoded} <- Base.decode64(header),
             {:ok, %{} = proof} <- Jason.decode(decoded) do
          {:ok, proof}
        else
          _ -> {:error, "X-PAYMENT must be JSON or base64 JSON"}
        end
    end
  end

  defp decode_payment(_header), do: {:error, "X-PAYMENT must be a string"}

  defp quote_id(%Mitos.Workload{} = workload, request_body, pay_to) do
    material = %{
      workload_id: workload.id,
      amount_usdc: workload.pricing["amount_usdc"],
      currency: @currency,
      pay_to: pay_to,
      request_hash: sha256(canonical_json(request_body || %{}))
    }

    "quote_" <> String.slice(sha256(canonical_json(material)), 7, 24)
  end

  defp escrow_address do
    Application.get_env(:mitos, :x402_escrow_address) ||
      System.get_env("MITOS_X402_ESCROW_ADDRESS") ||
      @default_escrow
  end

  defp delivery_status(%{"delivery" => %{"result" => %{"synthetic" => true}}}), do: "refund_due"

  defp delivery_status(%{"delivery" => %{"result" => %{} = result}}) when map_size(result) > 0,
    do: "settled"

  defp delivery_status(_proof), do: "refund_due"

  defp ledger_charge_micros(pricing, proof) do
    escrowed = amount_micros!(pricing["amount_usdc"])
    compute_surcharge = pricing |> compute_surcharge_micros() |> min(escrowed)

    if delivery_status(proof) == "settled" do
      pricing
      |> buyer_charge_micros(proof)
      |> min(escrowed)
    else
      compute_surcharge
    end
  end

  defp settlement_ledger_payload(workload, receipt, proof) do
    workload
    |> settle(receipt, proof)
    |> Map.delete("proof_hash")
    |> Map.put("basis_proof_hash", proof["effect_log_sha256"])
  end

  defp settled?(proof) do
    field(proof, "verified") == true or
      field(proof, "status") in ["settled", "paid", "verified"]
  end

  defp settlement_tx(proof) do
    field(proof, "settlement_tx") ||
      field(proof, "tx_id") ||
      field(proof, "transaction_hash") ||
      field(proof, "payment_id")
  end

  defp amount_micros!(value) do
    case amount_micros(value) do
      {:ok, micros} -> micros
      {:error, reason} -> raise ArgumentError, "invalid amount #{inspect(value)}: #{reason}"
    end
  end

  defp charge_recorded?(events, quote_id) do
    Enum.any?(events, fn
      %{"kind" => "effect", "type" => "budget_charged", "ref" => %{"quote_id" => ^quote_id}} ->
        true

      _event ->
        false
    end)
  end

  defp settlement_decision(trace, events, workload, receipt, buyer_charge_micros) do
    case settlement_decision_seq(events, receipt["quote_id"]) do
      seq when is_integer(seq) ->
        {trace, seq}

      nil ->
        case Mitos.Trace.append_decision_checked(trace, %{
               actor: "orchestrator",
               type: "finish",
               target: nil,
               params: %{
                 "result_ref" => "proof:#{receipt["quote_id"]}",
                 "success_criteria_met" => true,
                 "settlement_quote_id" => receipt["quote_id"],
                 "resource" => "/capabilities/#{workload.id}/runs"
               },
               inputs_hash: settlement_inputs_hash(events, receipt),
               rationale: "Finalize paid capability settlement",
               budget: budget_from_receipt(receipt)
             }) do
          {:ok, trace, %{"seq" => seq}} ->
            ensure_charge_within_budget!(
              Mitos.Trace.read_events(trace.run_id),
              seq,
              buyer_charge_micros
            )

            {trace, seq}

          {:abort, _trace, abort} ->
            raise ArgumentError, "settlement decision was refused: #{inspect(abort["params"])}"
        end
    end
  end

  defp settlement_decision_seq(events, quote_id) do
    Enum.find_value(events, fn
      %{
        "kind" => "decision",
        "type" => "finish",
        "seq" => seq,
        "params" => %{"settlement_quote_id" => ^quote_id}
      }
      when is_integer(seq) ->
        seq

      _event ->
        nil
    end)
  end

  defp settlement_inputs_hash(events, receipt) do
    material = %{
      quote_id: receipt["quote_id"],
      settlement_tx: receipt["settlement_tx"],
      effect_log_sha256:
        events
        |> Mitos.Proof.from_events()
        |> Map.get("effect_log_sha256")
    }

    sha256(canonical_json(material))
  end

  defp ensure_charge_within_budget!(events, decision_seq, charge_micros) do
    case decision_budget_micros(events, decision_seq) do
      nil ->
        :ok

      budget_micros when charge_micros <= budget_micros ->
        :ok

      budget_micros ->
        raise ArgumentError,
              "budget charge #{format_micros(charge_micros)} USDC exceeds decision budget #{format_micros(budget_micros)} USDC"
    end
  end

  defp decision_budget_micros(events, decision_seq) do
    events
    |> Enum.find(fn
      %{"kind" => "decision", "seq" => ^decision_seq} -> true
      _event -> false
    end)
    |> case do
      %{"budget" => %{} = budget} -> budget_usd_micros(budget)
      _event -> nil
    end
  end

  defp budget_usd_micros(%{} = budget) do
    budget
    |> field("usd")
    |> amount_micros()
    |> case do
      {:ok, micros} when micros > 0 -> micros
      _ -> nil
    end
  end

  defp buyer_charge_micros(%{"type" => "metered"} = pricing, proof) do
    base = amount_micros!(pricing["base_usdc"] || "0")
    unit_prices = pricing["unit_prices_usdc"] || %{}
    metering = proof["metering"] || %{}

    unit_total =
      unit_prices
      |> Enum.map(fn {unit, price} -> amount_micros!(price) * metered_units(unit, metering) end)
      |> Enum.sum()
      |> round()

    base + unit_total
  end

  defp buyer_charge_micros(%{"type" => "flat"} = pricing, _proof) do
    amount_micros!(pricing["amount_usdc"])
  end

  defp buyer_charge_micros(pricing, proof) do
    buyer_charge_micros(Map.put(pricing, "type", pricing["type"] || "flat"), proof)
  end

  defp metered_units("token", metering),
    do: numeric(get_in(metering, ["budget_charged", "tokens"]))

  defp metered_units("egress", metering), do: numeric(metering["egress_allowed_units"])
  defp metered_units("tool", metering), do: numeric(metering["tool_brokered_units"])
  defp metered_units("artifact", metering), do: numeric(metering["artifact_registered_units"])
  defp metered_units("ms", metering), do: numeric(metering["compute_ms"])
  defp metered_units(_unit, _metering), do: 0

  defp billing_basis(%{"type" => "metered"}), do: "metered_price"
  defp billing_basis(_pricing), do: "flat_price"

  defp compute_surcharge_micros(%{} = pricing),
    do: amount_micros!(pricing["compute_surcharge_usdc"] || "0")

  defp take_floor_micros!(%{"take_floor_usdc" => take_floor})
       when take_floor not in [nil, ""] do
    amount_micros!(take_floor)
  end

  defp take_floor_micros!(_pricing),
    do: raise(ArgumentError, "take_floor_usdc is required")

  defp platform_take_micros(0, _take_rate_bps, _take_floor), do: 0

  defp platform_take_micros(amount, take_rate_bps, take_floor) do
    max(div(amount * take_rate_bps, 10_000), min(take_floor, amount))
  end

  defp numeric(value) when is_integer(value) or is_float(value), do: value

  defp numeric(value) when is_binary(value) do
    case Float.parse(value) do
      {number, ""} -> number
      _ -> 0
    end
  end

  defp numeric(_value), do: 0

  defp decimal_parts(value) do
    case String.split(value, ".", parts: 2) do
      [whole] -> [whole, ""]
      [whole, fraction] -> [whole, fraction]
    end
  end

  defp parse_fraction(fraction) when byte_size(fraction) <= 6 do
    if Regex.match?(~r/^\d*$/, fraction) do
      padded = String.pad_trailing(fraction, 6, "0")
      {int, ""} = Integer.parse(if(padded == "", do: "0", else: padded))
      {:ok, int}
    else
      {:error, :invalid_amount}
    end
  end

  defp parse_fraction(_fraction), do: {:error, :invalid_amount}

  defp format_micros(micros) do
    whole = div(micros, 1_000_000)
    fraction = micros |> rem(1_000_000) |> Integer.to_string() |> String.pad_leading(6, "0")

    "#{whole}.#{fraction}"
  end

  defp field(%{} = map, key), do: Map.get(map, key, Map.get(map, String.to_atom(key)))
  defp field(_map, _key), do: nil

  defp maybe_put(opts, _key, nil), do: opts
  defp maybe_put(opts, key, value), do: Keyword.put(opts, key, value)

  defp sha256(bytes) when is_binary(bytes) do
    digest =
      :crypto.hash(:sha256, bytes)
      |> Base.encode16(case: :lower)

    "sha256:" <> digest
  end

  defp canonical_json(%{} = map) do
    body =
      map
      |> Enum.sort_by(fn {key, _value} -> to_string(key) end)
      |> Enum.map_join(",", fn {key, value} ->
        Jason.encode!(to_string(key)) <> ":" <> canonical_json(value)
      end)

    "{" <> body <> "}"
  end

  defp canonical_json(values) when is_list(values) do
    "[" <> Enum.map_join(values, ",", &canonical_json/1) <> "]"
  end

  defp canonical_json(value), do: Jason.encode!(value)
end
