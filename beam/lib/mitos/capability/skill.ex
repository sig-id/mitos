defmodule Mitos.Capability.Skill do
  @moduledoc """
  The per-model skill-adaptation seam (03 §2.1):

      resolved_skill = base_skill ⊕ per_model_adapt(model)

  `⊕` is a pure, deterministic merge (no model call). Overlay directives address
  base anchors:

      @adapt replace skill:procedure   …new body…
      @adapt append  skill:rules       …extra rules…
      @adapt prepend skill:success     …guard…

  **Identity law (MUST hold):** `base ⊕ ∅ = base`. Anchors are RECOMMENDED, not
  REQUIRED (03 §2, rev 2026-07-02): a plain markdown skill runs verbatim and the
  overlay degrades to document-scope append/prepend (no `replace` — no anchor to
  address). This builds the seam; the overlay *generator* is out of scope (03 §2.1).

  Anchors look like: `<!-- @skill:role -->`.
  """

  @anchor_re ~r/<!--\s*@skill:(?<name>[a-zA-Z0-9_-]+)\s*-->/

  @doc """
  Resolve a base skill against an optional overlay. Returns
  `{resolved_text, sha256}`. The sha256 is recorded per-run as an Effect so the
  exact instructions are replayable (03 §2.1, §9).
  """
  def resolve(base, overlay \\ nil, _model \\ nil)

  def resolve(base, nil, _model), do: finalize(base)
  def resolve(base, "", _model), do: finalize(base)

  def resolve(base, overlay, _model) when is_binary(overlay) do
    directives = parse_overlay(overlay)

    if directives == [] do
      # ∅ overlay ⇒ identity law
      finalize(base)
    else
      base
      |> apply_directives(directives)
      |> finalize()
    end
  end

  defp finalize(text), do: {text, Mitos.sha256(text)}

  # --- overlay parsing ---

  # A directive block starts with `@adapt <op> skill:<anchor>` and its body runs
  # until the next `@adapt` line or EOF.
  defp parse_overlay(overlay) do
    overlay
    |> String.split(~r/^@adapt\s+/m, trim: true)
    |> Enum.map(&String.trim_trailing/1)
    |> Enum.reject(&(&1 == ""))
    |> Enum.flat_map(fn chunk ->
      case Regex.run(~r/\A(replace|append|prepend)\s+skill:(?<anchor>[a-zA-Z0-9_-]+)\s*\n?/, chunk,
             capture: :all
           ) do
        [matched, op, anchor] ->
          body = chunk |> String.replace_prefix(matched, "") |> String.trim()
          [%{op: String.to_atom(op), anchor: anchor, body: body}]

        _ ->
          []
      end
    end)
  end

  # --- application ---

  defp apply_directives(base, directives) do
    if Regex.match?(@anchor_re, base) do
      apply_anchored(base, directives)
    else
      apply_document_scope(base, directives)
    end
  end

  # Section-addressed merge. Split base into ordered {anchor, body} sections.
  defp apply_anchored(base, directives) do
    sections = split_sections(base)

    merged =
      Enum.reduce(directives, sections, fn d, secs ->
        idx = Enum.find_index(secs, fn {name, _} -> name == d.anchor end)

        case {idx, d.op} do
          {nil, :replace} ->
            # no anchor to address ⇒ ignore replace (03 §2)
            secs

          {nil, _} ->
            # append/prepend to an absent anchor ⇒ document-scope, add a section
            secs ++ [{d.anchor, d.body}]

          {i, :replace} ->
            List.update_at(secs, i, fn {name, _} -> {name, d.body} end)

          {i, :append} ->
            List.update_at(secs, i, fn {name, body} -> {name, body <> "\n" <> d.body} end)

          {i, :prepend} ->
            List.update_at(secs, i, fn {name, body} -> {name, d.body <> "\n" <> body} end)
        end
      end)

    render_sections(base, merged)
  end

  # No anchors in base ⇒ only append/prepend at document scope; replace dropped.
  defp apply_document_scope(base, directives) do
    Enum.reduce(directives, base, fn
      %{op: :append, body: b}, acc -> acc <> "\n\n" <> b
      %{op: :prepend, body: b}, acc -> b <> "\n\n" <> acc
      %{op: :replace}, acc -> acc
    end)
  end

  # Preserve any preamble before the first anchor, then each section with its anchor.
  defp split_sections(base) do
    parts = Regex.split(@anchor_re, base, include_captures: true, trim: false)
    # parts alternates: [preamble, "<!-- @skill:x -->", body, "<!-- @skill:y -->", body, ...]
    do_pairs(parts, [])
  end

  defp do_pairs([_preamble | rest], acc), do: collect(rest, acc)

  defp collect([], acc), do: Enum.reverse(acc)

  defp collect([anchor_comment, body | rest], acc) do
    name = Regex.named_captures(@anchor_re, anchor_comment)["name"]
    collect(rest, [{name, String.trim_leading(body, "\n")} | acc])
  end

  defp collect([_leftover], acc), do: Enum.reverse(acc)

  defp render_sections(_base, sections) do
    sections
    |> Enum.map(fn {name, body} -> "<!-- @skill:#{name} -->\n#{String.trim(body)}" end)
    |> Enum.join("\n\n")
    |> Kernel.<>("\n")
  end
end
