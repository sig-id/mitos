defmodule Mitos.Connectors.Manual do
  @moduledoc """
  The `manual`/`web` connector — ingress from an operator UI, the CLI, or a
  direct API call (`spec/10_IO_PLANE.md` §3.1, §6: "the web app is one
  connector"). It performs no authentication or transformation: the caller has
  already produced the explicit mitos intake shape.
  """

  @behaviour Mitos.Connector

  @impl true
  def capabilities, do: %{ingress: true, round_trip?: false, auth: :operator}

  @impl true
  def receive(_handle, signal, _opts), do: {:ok, signal}

  @impl true
  def normalize(signal, _opts) when is_map(signal) do
    intake =
      %{
        "intent" => signal["intent"],
        "inputs" => signal["inputs"],
        "parameters" => signal["parameters"],
        "workspace" => signal["workspace"],
        "reply_ref" => signal["reply_ref"],
        "idempotency_key" => signal["idempotency_key"]
      }
      |> Enum.reject(fn {_key, value} -> is_nil(value) end)
      |> Map.new()

    {:ok, intake}
  end

  def normalize(_signal, _opts), do: {:error, {:invalid_signal, "expected JSON object"}}

  @impl true
  def ack(_handle, _signal, _outcome), do: :ok
end
