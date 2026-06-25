defmodule Mitos.Sinks.Web do
  @moduledoc """
  The `web` sink — the web app surface (`spec/10_IO_PLANE.md` §6, §5.2).

  The web app is itself an I/O surface: the live run trace, receipts, and result
  cards are the delivery. This sink performs **no external network call** — the
  receipt is materialized on the event log as a `delivery_sent` Effect and the
  frontend projects it. It is the reference no-op sink and the default output
  when an app declares no `io.outputs`.
  """

  @behaviour Mitos.Sink

  @impl true
  def capabilities, do: %{egress: true, round_trip?: false, retries: 0}

  @impl true
  def bind(binding) when is_map(binding), do: {:ok, binding}

  @impl true
  def deliver(_handle, %{"sink" => sink, "destination" => destination}) do
    {:ok,
     %{
       "sink" => sink || "web",
       "destination" => destination || "web",
       "status" => "sent",
       "channel" => "web"
     }}
  end

  def deliver(_handle, _payload) do
    {:ok, %{"sink" => "web", "destination" => "web", "status" => "sent", "channel" => "web"}}
  end
end
