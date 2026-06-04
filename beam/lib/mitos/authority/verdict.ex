defmodule Mitos.Authority.Verdict do
  @moduledoc """
  Result for a correlated authority-plane request.
  """

  @enforce_keys [:id, :decision]
  defstruct [
    :id,
    :decision,
    payload: %{},
    reason: nil
  ]

  @type decision :: :authorize | :deny

  @type t :: %__MODULE__{
          id: String.t(),
          decision: decision(),
          payload: map(),
          reason: String.t() | nil
        }
end
