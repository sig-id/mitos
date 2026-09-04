defmodule Mitos.Observation.Vt do
  @moduledoc """
  A VT/ANSI terminal state machine (14 §3.1) — fed **every** byte, emitting
  **nothing**.

  This is the half of the terminal channel that the prior implementation got
  wrong. A PTY carrying a TUI is a *display* protocol: parsing it continuously
  and acting on what streams past yields torn frames, escape sequences split
  across read boundaries, and no way to tell new information from a redraw. So
  the parser here is deliberately mute — it maintains a grid and answers
  questions only when someone calls `render/1`, and only when `stable?/1` says
  the byte stream is at a consistent boundary.

  `feed/2` is a **pure function of the byte prefix it has seen**, which is what
  makes a later `Terminal.replay/3` recompute an identical grid and verify a
  recorded digest (14 §3.1). Anything that breaks that purity breaks the audit
  property the whole channel rests on.

  Coverage is the TUI subset a modal-state classifier needs (14 §3.2): cursor
  motion, erase, scroll region, line/char insert-delete, alt-screen, and cursor
  visibility. SGR (colour/attribute) is parsed and dropped — the classifier
  reads text, not styling.
  """

  defstruct rows: 24,
            cols: 80,
            # %{{row, col} => grapheme}; absent = blank
            cells: %{},
            cursor: {0, 0},
            saved: {0, 0},
            alt_screen: false,
            cursor_visible: true,
            # {top, bottom} scroll region, 0-indexed inclusive
            region: nil,
            # trailing incomplete sequence (escape or multi-byte UTF-8)
            pending: "",
            # total bytes consumed, including `pending`
            offset: 0

  @type t :: %__MODULE__{}

  def new(rows \\ 24, cols \\ 80), do: %__MODULE__{rows: rows, cols: cols}

  @doc """
  Feed bytes. Never returns events. A trailing incomplete escape or UTF-8
  sequence is retained in `pending` and re-parsed on the next call, so a read
  boundary can never split a sequence into two mis-parsed halves.
  """
  def feed(%__MODULE__{} = vt, bytes) when is_binary(bytes) do
    {vt, pending} = run(%{vt | pending: ""}, vt.pending <> bytes)
    %{vt | pending: pending, offset: vt.offset + byte_size(bytes)}
  end

  @doc """
  Is the parser at a consistent boundary? A snapshot taken when this is false
  would render a half-applied sequence — so `Terminal.snapshot/1` refuses
  instead (14 §3.1).
  """
  def stable?(%__MODULE__{pending: ""}), do: true
  def stable?(%__MODULE__{}), do: false

  @doc "The grid as a list of row strings, trailing blanks trimmed per row."
  def render(%__MODULE__{} = vt) do
    for r <- 0..(vt.rows - 1) do
      0..(vt.cols - 1)
      |> Enum.map(&Map.get(vt.cells, {r, &1}, " "))
      |> Enum.join()
      |> String.trim_trailing()
    end
  end

  @doc "Content digest of the rendered grid + cursor + alt-screen flag."
  def digest(%__MODULE__{} = vt) do
    {row, col} = vt.cursor

    Mitos.sha256(
      Enum.join(render(vt), "\n") <>
        "\ncursor=#{row},#{col} alt=#{vt.alt_screen} visible=#{vt.cursor_visible}"
    )
  end

  # --- the byte loop ---

  defp run(vt, <<>>), do: {vt, ""}

  # A lone ESC (or an unterminated sequence) is incomplete: hold it.
  defp run(vt, <<0x1B>>), do: {vt, <<0x1B>>}

  defp run(vt, <<0x1B, ?[, rest::binary>> = all) do
    case scan_csi(rest, "") do
      {:ok, params, final, tail} -> run(csi(vt, params, final), tail)
      :incomplete -> {vt, all}
    end
  end

  # OSC: consume through BEL or ST. Window titles and the like affect no cell.
  defp run(vt, <<0x1B, ?], rest::binary>> = all) do
    case scan_osc(rest) do
      {:ok, tail} -> run(vt, tail)
      :incomplete -> {vt, all}
    end
  end

  # Charset designators: ESC ( C, ESC ) C, ESC * C, ESC + C
  defp run(vt, <<0x1B, d, rest::binary>>) when d in [?(, ?), ?*, ?+] do
    case rest do
      <<_c, tail::binary>> -> run(vt, tail)
      <<>> -> {vt, <<0x1B, d>>}
    end
  end

  defp run(vt, <<0x1B, ?7, rest::binary>>), do: run(%{vt | saved: vt.cursor}, rest)
  defp run(vt, <<0x1B, ?8, rest::binary>>), do: run(%{vt | cursor: vt.saved}, rest)
  defp run(vt, <<0x1B, ?M, rest::binary>>), do: run(reverse_index(vt), rest)
  defp run(vt, <<0x1B, ?D, rest::binary>>), do: run(index(vt), rest)
  defp run(vt, <<0x1B, ?E, rest::binary>>), do: run(index(cr(vt)), rest)
  defp run(vt, <<0x1B, ?c, rest::binary>>), do: run(%{new(vt.rows, vt.cols) | offset: vt.offset}, rest)
  defp run(vt, <<0x1B, _other, rest::binary>>), do: run(vt, rest)

  defp run(vt, <<?\r, rest::binary>>), do: run(cr(vt), rest)
  defp run(vt, <<?\n, rest::binary>>), do: run(index(vt), rest)
  defp run(vt, <<?\b, rest::binary>>), do: run(backspace(vt), rest)
  defp run(vt, <<?\t, rest::binary>>), do: run(tab(vt), rest)
  defp run(vt, <<0x07, rest::binary>>), do: run(vt, rest)
  defp run(vt, <<b, rest::binary>>) when b < 0x20, do: run(vt, rest)
  defp run(vt, <<0x7F, rest::binary>>), do: run(vt, rest)

  defp run(vt, bin) do
    case bin do
      <<c::utf8, rest::binary>> ->
        run(put_char(vt, <<c::utf8>>), rest)

      <<_b, rest::binary>> ->
        # Either a truncated multi-byte sequence at the read boundary (hold it)
        # or a genuinely invalid byte (drop it) — never a mis-decoded cell.
        if incomplete_utf8?(bin), do: {vt, bin}, else: run(vt, rest)
    end
  end

  defp incomplete_utf8?(<<b, _::binary>> = bin) do
    need =
      cond do
        b >= 0xF0 -> 4
        b >= 0xE0 -> 3
        b >= 0xC0 -> 2
        true -> 1
      end

    need > 1 and byte_size(bin) < need
  end

  # --- CSI scanning ---

  # Parameter bytes 0x30..0x3F, intermediates 0x20..0x2F, final 0x40..0x7E.
  defp scan_csi(<<b, rest::binary>>, acc) when b >= 0x30 and b <= 0x3F,
    do: scan_csi(rest, acc <> <<b>>)

  defp scan_csi(<<b, rest::binary>>, acc) when b >= 0x20 and b <= 0x2F,
    do: scan_csi(rest, acc <> <<b>>)

  defp scan_csi(<<b, rest::binary>>, acc) when b >= 0x40 and b <= 0x7E,
    do: {:ok, acc, b, rest}

  defp scan_csi(<<>>, _acc), do: :incomplete
  defp scan_csi(<<_b, rest::binary>>, acc), do: scan_csi(rest, acc)

  defp scan_osc(<<0x07, rest::binary>>), do: {:ok, rest}
  defp scan_osc(<<0x1B, ?\\, rest::binary>>), do: {:ok, rest}
  defp scan_osc(<<>>), do: :incomplete
  defp scan_osc(<<0x1B>>), do: :incomplete
  defp scan_osc(<<_b, rest::binary>>), do: scan_osc(rest)

  # --- CSI dispatch ---

  defp csi(vt, params, final) do
    private = String.starts_with?(params, "?")
    nums = parse_params(params)

    case {private, final} do
      {true, ?h} -> Enum.reduce(nums, vt, &set_mode(&2, &1, true))
      {true, ?l} -> Enum.reduce(nums, vt, &set_mode(&2, &1, false))
      {false, ?H} -> move_to(vt, at(nums, 0, 1) - 1, at(nums, 1, 1) - 1)
      {false, ?f} -> move_to(vt, at(nums, 0, 1) - 1, at(nums, 1, 1) - 1)
      {false, ?A} -> move_by(vt, -at(nums, 0, 1), 0)
      {false, ?B} -> move_by(vt, at(nums, 0, 1), 0)
      {false, ?C} -> move_by(vt, 0, at(nums, 0, 1))
      {false, ?D} -> move_by(vt, 0, -at(nums, 0, 1))
      {false, ?E} -> move_to(vt, elem(vt.cursor, 0) + at(nums, 0, 1), 0)
      {false, ?F} -> move_to(vt, elem(vt.cursor, 0) - at(nums, 0, 1), 0)
      {false, ?G} -> move_to(vt, elem(vt.cursor, 0), at(nums, 0, 1) - 1)
      {false, ?d} -> move_to(vt, at(nums, 0, 1) - 1, elem(vt.cursor, 1))
      {false, ?J} -> erase_display(vt, at(nums, 0, 0))
      {false, ?K} -> erase_line(vt, at(nums, 0, 0))
      {false, ?L} -> insert_lines(vt, at(nums, 0, 1))
      {false, ?M} -> delete_lines(vt, at(nums, 0, 1))
      {false, ?P} -> delete_chars(vt, at(nums, 0, 1))
      {false, ?@} -> insert_chars(vt, at(nums, 0, 1))
      {false, ?X} -> erase_chars(vt, at(nums, 0, 1))
      {false, ?S} -> scroll_up(vt, at(nums, 0, 1))
      {false, ?T} -> scroll_down(vt, at(nums, 0, 1))
      {false, ?r} -> set_region(vt, nums)
      {false, ?s} -> %{vt | saved: vt.cursor}
      {false, ?u} -> %{vt | cursor: vt.saved}
      # SGR and everything else affect no cell content.
      _ -> vt
    end
  end

  defp parse_params(params) do
    params
    |> String.replace_prefix("?", "")
    |> String.split(";")
    |> Enum.map(fn
      "" -> nil
      s -> case Integer.parse(s) do
             {n, _} -> n
             :error -> nil
           end
    end)
  end

  defp at(nums, i, default) do
    case Enum.at(nums, i) do
      nil -> default
      0 when default == 1 -> 1
      n -> n
    end
  end

  # 1049/47/1047 = alternate screen; 25 = cursor visibility. Alt-screen matters
  # to the classifier: a full-screen TUI and a scrollback prompt are different
  # modal worlds (14 §3.2).
  defp set_mode(vt, mode, on) when mode in [1049, 47, 1047] do
    if on == vt.alt_screen do
      vt
    else
      %{vt | alt_screen: on, cells: %{}, cursor: {0, 0}}
    end
  end

  defp set_mode(vt, 25, on), do: %{vt | cursor_visible: on}
  defp set_mode(vt, _mode, _on), do: vt

  # --- cell / cursor operations ---

  defp put_char(vt, ch) do
    {row, col} = vt.cursor

    if col >= vt.cols do
      vt |> cr() |> index() |> put_char(ch)
    else
      %{vt | cells: Map.put(vt.cells, {row, col}, ch), cursor: {row, col + 1}}
    end
  end

  defp cr(vt), do: %{vt | cursor: {elem(vt.cursor, 0), 0}}

  defp backspace(vt) do
    {row, col} = vt.cursor
    %{vt | cursor: {row, max(col - 1, 0)}}
  end

  defp tab(vt) do
    {row, col} = vt.cursor
    %{vt | cursor: {row, min(div(col, 8) * 8 + 8, vt.cols - 1)}}
  end

  defp index(vt) do
    {row, col} = vt.cursor
    {_top, bottom} = region(vt)

    if row >= bottom, do: %{scroll_up(vt, 1) | cursor: {bottom, col}}, else: %{vt | cursor: {row + 1, col}}
  end

  defp reverse_index(vt) do
    {row, col} = vt.cursor
    {top, _bottom} = region(vt)

    if row <= top, do: %{scroll_down(vt, 1) | cursor: {top, col}}, else: %{vt | cursor: {row - 1, col}}
  end

  defp move_to(vt, row, col),
    do: %{vt | cursor: {clamp(row, 0, vt.rows - 1), clamp(col, 0, vt.cols - 1)}}

  defp move_by(vt, drow, dcol) do
    {row, col} = vt.cursor
    move_to(vt, row + drow, col + dcol)
  end

  defp erase_display(vt, 2), do: %{vt | cells: %{}}
  defp erase_display(vt, 3), do: %{vt | cells: %{}}

  defp erase_display(vt, 0) do
    {row, col} = vt.cursor
    cells = Enum.reject(vt.cells, fn {{r, c}, _} -> r > row or (r == row and c >= col) end)
    %{vt | cells: Map.new(cells)}
  end

  defp erase_display(vt, 1) do
    {row, col} = vt.cursor
    cells = Enum.reject(vt.cells, fn {{r, c}, _} -> r < row or (r == row and c <= col) end)
    %{vt | cells: Map.new(cells)}
  end

  defp erase_display(vt, _), do: vt

  defp erase_line(vt, mode) do
    {row, col} = vt.cursor

    keep = fn {{r, c}, _} ->
      cond do
        r != row -> true
        mode == 0 -> c < col
        mode == 1 -> c > col
        mode == 2 -> false
        true -> true
      end
    end

    %{vt | cells: vt.cells |> Enum.filter(keep) |> Map.new()}
  end

  defp erase_chars(vt, n) do
    {row, col} = vt.cursor
    cells = Enum.reject(vt.cells, fn {{r, c}, _} -> r == row and c >= col and c < col + n end)
    %{vt | cells: Map.new(cells)}
  end

  defp delete_chars(vt, n) do
    {row, col} = vt.cursor
    shift_row(vt, row, col, -n)
  end

  defp insert_chars(vt, n) do
    {row, col} = vt.cursor
    shift_row(vt, row, col, n)
  end

  defp shift_row(vt, row, from, delta) do
    {on_row, rest} = Enum.split_with(vt.cells, fn {{r, _c}, _} -> r == row end)

    moved =
      on_row
      |> Enum.reject(fn {{_r, c}, _} -> delta < 0 and c >= from and c < from - delta end)
      |> Enum.map(fn {{r, c}, ch} -> if c >= from, do: {{r, c + delta}, ch}, else: {{r, c}, ch} end)
      |> Enum.reject(fn {{_r, c}, _} -> c < 0 or c >= vt.cols end)

    %{vt | cells: Map.new(rest ++ moved)}
  end

  defp insert_lines(vt, n), do: shift_lines(vt, elem(vt.cursor, 0), n)
  defp delete_lines(vt, n), do: shift_lines(vt, elem(vt.cursor, 0), -n)
  defp scroll_up(vt, n), do: shift_lines(vt, elem(region(vt), 0), -n)
  defp scroll_down(vt, n), do: shift_lines(vt, elem(region(vt), 0), n)

  # Move rows within the scroll region by `delta`, dropping what falls out.
  defp shift_lines(vt, from, delta) do
    {top, bottom} = region(vt)
    from = max(from, top)

    {inside, outside} =
      Enum.split_with(vt.cells, fn {{r, _c}, _} -> r >= from and r <= bottom end)

    moved =
      inside
      |> Enum.map(fn {{r, c}, ch} -> {{r + delta, c}, ch} end)
      |> Enum.reject(fn {{r, _c}, _} -> r < from or r > bottom end)

    %{vt | cells: Map.new(outside ++ moved)}
  end

  defp set_region(vt, nums) do
    top = (Enum.at(nums, 0) || 1) - 1
    bottom = (Enum.at(nums, 1) || vt.rows) - 1

    if top >= 0 and bottom > top and bottom <= vt.rows - 1 do
      %{vt | region: {top, bottom}}
    else
      %{vt | region: nil}
    end
  end

  defp region(%{region: nil, rows: rows}), do: {0, rows - 1}
  defp region(%{region: r}), do: r

  defp clamp(v, lo, hi), do: v |> max(lo) |> min(hi)
end
