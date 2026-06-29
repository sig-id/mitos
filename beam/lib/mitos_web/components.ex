defmodule MitosWeb.Components do
  @moduledoc """
  Shared function components for the console — ported from the Svelte SPA's
  `StatusPill`, `ConnectorChip`, and `HarnessBadge`.
  """
  use MitosWeb, :html

  alias MitosWeb.Catalog

  @doc "Status pill — live/done/parked/error states with a pulsing dot."
  attr :status, :string, default: "idle"

  def status_pill(assigns) do
    status = assigns[:status] || "idle"

    label =
      cond do
        String.match?(status || "", ~r/run/i) -> "Live"
        status == "idle" -> "Idle"
        status in ["done", "completed"] -> "Done"
        status in ["error", "failed"] -> "Error"
        status == "parked" -> "Parked"
        true -> status || "idle"
      end

    assigns = assign(assigns, status: status || "idle", label: label)

    ~H"""
    <span class={"status-pill s-#{@status}"}>
      <span class="pulse"></span>
      <span class="t">{@label}</span>
    </span>
    """
  end

  @doc "Connector glyph chip with brand color."
  attr :id, :string, default: nil
  attr :size, :integer, default: 16
  attr :label, :string, default: ""

  def connector_chip(assigns) do
    id = assigns[:id]

    assigns =
      assign(assigns,
        color: Catalog.connector_color(id),
        glyph: Catalog.connector_glyph(id)
      )

    ~H"""
    <span
      class="connector"
      style={"--c:#{@color}; --s:#{@size}px"}
      title={@label || @id || ""}
      aria-label={@label || @id || ""}
    >
      <span class="g">{@glyph}</span>
    </span>
    """
  end

  @doc "Harness badge with vendor icon."
  attr :id, :string, default: nil

  def harness_badge(assigns) do
    case Catalog.harness(assigns[:id]) do
      nil ->
        ~H""

      h ->
        assigns = assign(assigns, h: h)

        ~H"""
        <span class="harness" style={"--c:#{@h["color"]}"}>
          <span class="dot">{@h["icon"]}</span>
          <span class="n">{@h["name"]}</span>
        </span>
        """
    end
  end

  @doc "A button phx-click helper that also works as a plain link via `to`."
  attr :class, :string, default: "btn primary"
  attr :rest, :global
  slot :inner_block

  def ui_button(assigns) do
    ~H"""
    <button class={@class} {@rest}>
      {render_slot(@inner_block)}
    </button>
    """
  end
end
