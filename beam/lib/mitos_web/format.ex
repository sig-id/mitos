defmodule MitosWeb.Format do
  @moduledoc """
  Display helpers ported from the Svelte SPA's `lib/format.ts`.
  """

  def format_relative(nil), do: ""
  def format_relative(""), do: ""

  def format_relative(ts) when is_integer(ts) do
    diff = System.system_time(:millisecond) - ts
    format_diff(diff)
  end

  def format_relative(ts) when is_binary(ts) do
    case Integer.parse(ts) do
      {n, ""} -> format_relative(n)
      _ -> ts
    end
  end

  defp format_diff(diff) do
    abs = abs(diff)
    past = diff >= 0

    {label, sec} =
      Enum.find(
        [{"y", 31_536_000}, {"d", 86_400}, {"h", 3600}, {"m", 60}, {"s", 1}],
        fn {_l, s} -> abs >= s end
      ) || {"s", 1}

    v = div(abs, sec)

    if past, do: "#{v}#{label} ago", else: "in #{v}#{label}"
  end

  def format_time(nil), do: ""

  def format_time(ts) when is_integer(ts) do
    DateTime.from_unix!(div(ts, 1000), :second)
    |> Calendar.strftime("%I:%M %p")
  end

  def format_time(_), do: ""

  def compact_id(nil), do: "n/a"

  def compact_id(value) when is_binary(value) do
    if byte_size(value) > 22 do
      String.slice(value, 0, 10) <> "…" <> String.slice(value, -6, 6)
    else
      value
    end
  end

  def humanize(nil), do: ""
  def humanize(""), do: ""

  def humanize(value) when is_binary(value) do
    value
    |> String.replace(["-", "_"], " ")
    |> String.replace(~r/\b\w/, &String.upcase/1)
  end
end
