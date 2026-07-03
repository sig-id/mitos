defmodule Mitos.JSON do
  @moduledoc """
  Thin wrapper over OTP 27+ `:json` with atom-tolerant encoding and Elixir
  `nil` ⇄ JSON `null` translation (Erlang's json convention is the `null`
  atom; untranslated, `nil` round-trips as the string `"nil"`).
  """

  def encode!(term), do: term |> normalize() |> :json.encode() |> IO.iodata_to_binary()

  def decode!(bin) when is_binary(bin), do: bin |> :json.decode() |> denull()

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
  def normalize(nil), do: :null

  def normalize(atom) when is_atom(atom) and not is_boolean(atom), do: to_string(atom)
  def normalize(other), do: other

  defp denull(:null), do: nil
  defp denull(%{} = map), do: Map.new(map, fn {k, v} -> {k, denull(v)} end)
  defp denull(list) when is_list(list), do: Enum.map(list, &denull/1)
  defp denull(other), do: other
end
