defmodule MitosWeb.ConsoleLive.Runs do
  @moduledoc "Run list table. Ported from `Runs.svelte`."
  use MitosWeb, :live_view
  use MitosWeb.ConsoleLive.Shared

  alias MitosWeb.Format

  @impl true
  def handle_params(_params, _url, socket) do
    {:noreply, assign(socket, active_nav: "runs")}
  end

  defp refresh_page(socket) do
    runs = MitosWeb.Console.list_runs()
    assign(socket, runs: runs, run_count: length(runs))
  end

  @impl true
  def handle_event("open-run", %{"id" => id}, socket) do
    {:noreply, push_navigate(socket, to: ~p"/console/runs/#{id}")}
  end

  @impl true
  def render(assigns) do
    ~H"""
    <div class="page">
      <div class="head">
        <h1>Runs</h1>
        <p class="lead">All runs — manual launches, connector signals, and scheduled triggers.</p>
      </div>
      <div class="runs-table">
        <div class="thr">
          <span>Run</span>
          <span>Status</span>
          <span>State</span>
          <span>Last event</span>
          <span>Events</span>
        </div>
        <%= if @runs == [] do %>
          <div class="empty">No runs yet.</div>
        <% else %>
          <%= for r <- @runs do %>
            <button class="trow" phx-click="open-run" phx-value-id={r["run_id"]}>
              <span class="nm">
                <%= r["title"] || r["subtitle"] || Format.compact_id(r["run_id"]) %>
                <%= if r["subtitle"] do %><span class="muted" style="font-size:11px"> · <%= r["subtitle"] %></span><% end %>
              </span>
              <span class="muted"><%= Format.status_label(r["status"]) %></span>
              <span><MitosWeb.Components.status_pill status={r["status"]} /></span>
              <span class="muted"><%= Format.format_relative(r["last_event_ms"] || r["created_at_ms"]) %></span>
              <span class="muted"><%= r["events"] || 0 %></span>
            </button>
          <% end %>
        <% end %>
      </div>
    </div>
    """
  end
end
