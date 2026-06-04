defmodule Mitos.Authority.Request do
  @moduledoc """
  Correlated authority-plane request.

  `from` is intentionally opaque. For socket-origin requests it is the
  `GenServer.from` that must be replied to when a verdict arrives; terminal
  scrape requests use `nil` because their reply route is a sidecar CONTROL
  frame.
  """

  @enforce_keys [:method, :action, :context, :origin]
  defstruct [
    :id,
    :method,
    :action,
    :context,
    :origin,
    :from
  ]

  @type origin :: :terminal_scrape | :authority_socket | :codec

  @type t :: %__MODULE__{
          id: String.t() | nil,
          method: String.t(),
          action: String.t(),
          context: map() | String.t(),
          origin: origin(),
          from: GenServer.from() | nil
        }
end
