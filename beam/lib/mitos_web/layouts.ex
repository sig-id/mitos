defmodule MitosWeb.Layouts do
  @moduledoc false
  use MitosWeb, :html

  def root(assigns) do
    ~H"""
    <!DOCTYPE html>
    <html lang="en" data-theme="dark">
      <head>
        <meta charset="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1" />
        <meta name="color-scheme" content="dark light" />
        <.live_title suffix=" · Mitos">
          <%= assigns[:page_title] || "Mitos" %>
        </.live_title>
        <link phx-track-static rel="stylesheet" href={~p"/assets/app.css"} />
        <%= csrf_meta_tag() %>
        <script defer phx-track-static type="text/javascript" src={~p"/assets/phoenix.js"}></script>
        <script defer phx-track-static type="text/javascript" src={~p"/assets/phoenix_live_view.js"}></script>
        <script defer phx-track-static type="text/javascript" src={~p"/assets/app.js"}></script>
      </head>
      <body>
        <%= @inner_content %>
      </body>
    </html>
    """
  end

  defp csrf_meta_tag do
    {:safe,
     """
     <meta name="csrf-token" content="#{Plug.CSRFProtection.get_csrf_token()}" />
     """}
  end

  def app(assigns) do
    ~H"""
    <.shell active_nav={@active_nav} apps={@apps} run_count={@run_count} app_count={@app_count}
      launch_open={@launch_open} error={@error} toast={@toast}>
      <%= @inner_content %>
    </.shell>

    <%= if @launch_open do %>
      <.live_component module={MitosWeb.ConsoleLive.LaunchComponent} id="launch"
        apps={@apps} app={@launch_app} entrypoint={@launch_entrypoint} />
    <% end %>
    """
  end

  attr(:active_nav, :string, default: "dashboard")
  attr(:apps, :list, default: [])
  attr(:run_count, :integer, default: 0)
  attr(:app_count, :integer, default: 0)
  attr(:launch_open, :boolean, default: false)
  attr(:error, :any, default: nil)
  attr(:toast, :any, default: nil)
  slot(:inner_block, required: true)

  defp shell(assigns) do
    ~H"""
    <div class="shell">
      <aside class="side">
        <div class="brand">
          <div class="logo">◇</div>
          <div>
            <div class="bn">Mitos</div>
            <div class="bs">Control plane</div>
          </div>
        </div>

        <nav>
          <button class={"nav #{if @active_nav == "dashboard", do: "on"}"} phx-click="nav-dashboard">
            <span class="ni">▦</span> Dashboard
          </button>
          <button class={"nav #{if @active_nav == "agents", do: "on"}"} phx-click="nav-applications">
            <span class="ni">✦</span> Applications
          </button>
          <button class={"nav #{if @active_nav == "connectors", do: "on"}"} phx-click="nav-connectors">
            <span class="ni">⇄</span> Connectors
          </button>
          <button class={"nav #{if @active_nav == "runs", do: "on"}"} phx-click="nav-runs">
            <span class="ni">⟳</span> Runs
          </button>
        </nav>

        <div class="section-label">Applications</div>
        <div class="app-list">
          <%= for a <- @apps do %>
            <button class="app" phx-click="nav-app" phx-value-id={a["id"]}>
              <span class="ad"></span>
              <span class="an"><%= a["name"] %></span>
            </button>
          <% end %>
        </div>

        <div class="side-foot">
          <button class="launch" phx-click="open-launch">
            <span>＋</span> Run agent
          </button>
          <div class="status">
            <span class="ok"></span>
            <span class="muted"><%= @app_count %> apps · <%= @run_count %> runs</span>
          </div>
        </div>
      </aside>

      <main class="main">
        <%= if @error do %>
          <div class="banner danger">
            <strong>Request failed</strong>
            <span><%= @error %></span>
            <button phx-click="dismiss-error">Dismiss</button>
          </div>
        <% end %>
        <%= if @toast do %>
          <div class="banner success">
            <strong><%= @toast %></strong>
            <button phx-click="dismiss-toast">Dismiss</button>
          </div>
        <% end %>

        <%= render_slot(@inner_block) %>
      </main>
    </div>
    """
  end
end
