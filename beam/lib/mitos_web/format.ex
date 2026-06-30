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
    # `diff` is in milliseconds; the thresholds below are in seconds.
    diff_s = div(abs(diff), 1000)
    past = diff >= 0

    {label, sec} =
      Enum.find(
        [{"y", 31_536_000}, {"d", 86_400}, {"h", 3600}, {"m", 60}, {"s", 1}],
        fn {_l, s} -> diff_s >= s end
      ) || {"s", 1}

    v = div(diff_s, sec)

    cond do
      label == "s" and v < 1 -> "just now"
      past -> "#{v}#{label} ago"
      true -> "in #{v}#{label}"
    end
  end

  def format_time(nil), do: ""

  def format_time(ts) when is_integer(ts) do
    DateTime.from_unix!(div(ts, 1000), :second)
    |> Calendar.strftime("%I:%M %p")
  end

  def format_time(_), do: ""

  # Format an ISO8601 timestamp (as carried on Decision/Effect events) to a
  # short local clock time. Returns "" for anything unparseable.
  def format_iso_time(nil), do: ""

  def format_iso_time(iso) when is_binary(iso) do
    case DateTime.from_iso8601(iso) do
      {:ok, dt, _offset} -> Calendar.strftime(dt, "%H:%M:%S")
      _ -> ""
    end
  end

  def format_iso_time(_), do: ""

  # Human-readable run status for pills / lists.
  def status_label(nil), do: "Idle"
  def status_label(""), do: "Idle"

  def status_label(status) do
    case String.downcase(status) do
      s when s in ["running"] -> "Running"
      s when s in ["scheduled", "queued"] -> "Queued"
      s when s in ["done", "completed"] -> "Done"
      s when s in ["failed", "error"] -> "Failed"
      s when s in ["interrupted"] -> "Interrupted"
      s when s in ["parked"] -> "Waiting"
      s when s in ["empty"] -> "Starting"
      _ -> status
    end
  end

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
