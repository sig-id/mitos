defmodule Mitos.JSON do
  @moduledoc "Thin wrapper over OTP 27+ `:json` with atom-tolerant encoding."

  def encode!(term), do: term |> normalize() |> :json.encode() |> IO.iodata_to_binary()

  def decode!(bin) when is_binary(bin), do: :json.decode(bin)

  def decode(bin) when is_binary(bin) do
    {:ok, decode!(bin)}
  rescue
    e -> {:error, e}
  end

  @doc "Recursively convert atom keys/values to strings (except booleans/nil)."
  def normalize(%_{} = struct), do: struct |> Map.from_struct() |> normalize()

  def normalize(%{} = map),
    do: Map.new(map, fn {k, v} -> {to_string(k), normalize(v)} end)

  def normalize(list) when is_list(list), do: Enum.map(list, &normalize/1)
  def normalize(atom) when is_atom(atom) and not is_boolean(atom) and not is_nil(atom), do: to_string(atom)
  def normalize(other), do: other
end
