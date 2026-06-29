defmodule MitosWeb.ConsoleLive.Connectors do
  @moduledoc "Connectors & sinks grid. Ported from `Connectors.svelte`."
  use MitosWeb, :live_view
  use MitosWeb.ConsoleLive.Shared

  alias MitosWeb.Catalog

  @impl true
  def handle_params(_params, _url, socket) do
    {:noreply,
     socket
     |> assign(active_nav: "connectors")
     |> assign(connectors: MitosWeb.Console.list_connectors(), sinks: MitosWeb.Console.list_sinks())}
  end

  defp refresh_page(socket) do
    assign(socket, connectors: MitosWeb.Console.list_connectors(), sinks: MitosWeb.Console.list_sinks())
  end

  @impl true
  def render(assigns) do
    ~H"""
    <div class="page">
      <div class="head">
        <h1>Connectors &amp; Sinks</h1>
        <p class="lead">
          The I/O layer. Connectors bring signals in; sinks carry results back out. The web app is
          itself one connector + one sink.
        </p>
      </div>

      <h2>Inputs · Connectors</h2>
      <div class="io-grid">
        <%= if @connectors == [] do %>
          <div class="empty">No connectors registered.</div>
        <% else %>
          <%= for c <- @connectors do %>
            <div class="io-card">
              <div class="row between">
                <MitosWeb.Components.connector_chip id={c["name"]} size={32} />
                <div class="dirs">
                  <%= if c["capabilities"]["ingress"] do %><span class="dir in">IN</span><% end %>
                  <%= if c["capabilities"]["round_trip"] do %><span class="dir rt">RT</span><% end %>
                </div>
              </div>
              <div class="nm"><%= c["name"] %></div>
              <p class="ds"><%= Catalog.describe_connector(c["name"]) %></p>
            </div>
          <% end %>
        <% end %>
      </div>

      <h2>Outputs · Sinks</h2>
      <div class="io-grid">
        <%= if @sinks == [] do %>
          <div class="empty">No sinks registered.</div>
        <% else %>
          <%= for s <- @sinks do %>
            <div class="io-card">
              <div class="row between">
                <span class="sink-glyph">↗</span>
                <div class="dirs">
                  <span class="dir out">OUT</span>
                  <%= if s["capabilities"]["round_trip"] do %><span class="dir rt">RT</span><% end %>
                </div>
              </div>
              <div class="nm"><%= s["name"] %></div>
              <p class="ds"><%= Catalog.describe_sink(s["name"]) %></p>
            </div>
          <% end %>
        <% end %>
      </div>
    </div>
    """
  end
end
