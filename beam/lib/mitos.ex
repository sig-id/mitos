defmodule Mitos do
  @moduledoc """
  Mitos — a substrate that runs packaged capabilities through agent-driven
  pipelines: an agent decides policy, deterministic code records every decision
  and enforces every limit (spec/01_ARCHITECTURE §0).

  This module holds small shared helpers; the canonical contracts live in:

    * `Mitos.Event.Decision` / `Mitos.Event.Effect` — 01 §8.1/8.2
    * `Mitos.Capability.Manifest` — 01 §8.3
    * `Mitos.SandboxProvider` — 01 §8.4
    * `Mitos.Harness` — 01 §8.6
    * `Mitos.Task` — 01 §8.7
    * `Mitos.App.Manifest` — 01 §8.8
  """

  def data_dir, do: Application.fetch_env!(:mitos, :data_dir)

  def run_dir(run_id) do
    # run_id is tenant-stamped: "<org>/<id>" (09 §4). Nested dirs keep the
    # log tenant-partitioned on disk from day one.
    Path.join([data_dir(), "runs", run_id])
  end

  @doc "ISO-8601 UTC timestamp."
  def ts, do: DateTime.utc_now() |> DateTime.to_iso8601()

  @doc "Lowercase hex sha256."
  def sha256(bin) when is_binary(bin) do
    :crypto.hash(:sha256, bin) |> Base.encode16(case: :lower)
  end

  @doc "Short random id (url-safe)."
  def id(prefix \\ nil) do
    rand = :crypto.strong_rand_bytes(9) |> Base.url_encode64(padding: false)
    if prefix, do: "#{prefix}_#{rand}", else: rand
  end

  @doc "Encode a term as canonical JSON (sorted object keys) — used for content identity."
  def canonical_json(term), do: term |> enc() |> IO.iodata_to_binary()

  defp enc(%_{} = struct), do: struct |> Map.from_struct() |> enc()

  defp enc(%{} = map) do
    inner =
      map
      |> Enum.map(fn {k, v} -> {to_string(k), v} end)
      |> Enum.sort_by(&elem(&1, 0))
      |> Enum.map(fn {k, v} -> [enc(k), ":", enc(v)] end)
      |> Enum.intersperse(",")

    ["{", inner, "}"]
  end

  defp enc(list) when is_list(list),
    do: ["[", list |> Enum.map(&enc/1) |> Enum.intersperse(","), "]"]

  defp enc(nil), do: "null"
  defp enc(b) when is_boolean(b), do: to_string(b)
  defp enc(n) when is_integer(n), do: Integer.to_string(n)
  defp enc(f) when is_float(f), do: Float.to_string(f)
  defp enc(s) when is_binary(s), do: :json.encode(s)
  defp enc(a) when is_atom(a), do: :json.encode(Atom.to_string(a))
end
