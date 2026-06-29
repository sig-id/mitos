defmodule MitosWeb.ConsoleLive.Dashboard do
  @moduledoc """
  Run & Watch (outer view): the application strip + the kanban of runs. Ported
  from `Dashboard.svelte`.
  """
  use MitosWeb, :live_view
  use MitosWeb.ConsoleLive.Shared

  alias MitosWeb.Format

  @stages [
    %{"id" => "running", "name" => "Running", "tone" => "live"},
    %{"id" => "queued", "name" => "Queued", "tone" => "muted"},
    %{"id" => "done", "name" => "Done", "tone" => "good"}
  ]

  @impl true
  def handle_params(_params, _url, socket) do
    {:noreply,
     socket
     |> assign(active_nav: "dashboard")
     |> assign_new(:app_filter, fn -> "all" end)}
  end

  defp refresh_page(socket) do
    runs = MitosWeb.Console.list_runs()
    assign(socket, runs: runs, run_count: length(runs))
  end

  @impl true
  def handle_event("open-run", %{"id" => id}, socket) do
    {:noreply, push_navigate(socket, to: ~p"/console/runs/#{id}")}
  end

  def handle_event("filter", %{"id" => id}, socket) do
    {:noreply, assign(socket, app_filter: id)}
  end

  defp filtered_runs(runs, "all"), do: runs

  defp filtered_runs(runs, app_id) do
    Enum.filter(runs, fn r ->
      String.starts_with?(r["run_id"] || "", app_id) or r["provider"] == "app"
    end)
  end

  defp stage_of(%{"status" => status}) do
    s = String.downcase(status || "")

    cond do
      String.contains?(s, "run") and not String.contains?(s, "done") -> "running"
      String.contains?(s, "queue") or String.contains?(s, "sched") -> "queued"
      Enum.any?(["done", "completed", "finished"], &String.contains?(s, &1)) -> "done"
      true -> "running"
    end
  end

  defp stage_of(_), do: "running"

  @impl true
  def render(assigns) do
    runs = filtered_runs(assigns.runs, assigns.app_filter)

    assigns =
      assign(assigns,
        stages: @stages,
        col_items: Enum.map(@stages, fn col -> {col, Enum.filter(runs, &(&1 |> stage_of() == col["id"]))} end)
      )

    ~H"""
    <div class="dash">
      <section class="loop">
        <div class="loop-h">
          <div>
            <h2>Applications</h2>
            <p>Agentic applications installed in this control plane.</p>
          </div>
          <button class="new" phx-click="open-launch"><span class="plus">＋</span> Run agent</button>
        </div>

        <div class="loop-strip">
          <%= if @apps == [] do %>
            <div class="loop-empty">No applications installed yet.</div>
          <% else %>
            <%= for a <- @apps do %>
              <button class="loop-card" phx-click="nav-app" phx-value-id={a["id"]}>
                <div class="top">
                  <div class="ic"><%= length(a["entrypoints"] || []) || "·" %></div>
                  <span class="badge"><%= length(a["pipelines"] || []) %> pipelines</span>
                </div>
                <div class="nm"><%= a["name"] %></div>
                <div class="ag"><%= a["version"] || "local" %></div>
                <div class="conns">
                  <%= for b <- Enum.take(a["io"] && a["io"]["inputs"] || [], 4) do %>
                    <MitosWeb.Components.connector_chip id={b["connector"] || "manual"} size={18} />
                  <% end %>
                  <%= for b <- Enum.take(a["io"] && a["io"]["outputs"] || [], 2) do %>
                    <span class="sink"><%= b["sink"] || "web" %></span>
                  <% end %>
                </div>
              </button>
            <% end %>
          <% end %>
        </div>
      </section>

      <div class="filters">
        <div class="row" style="gap:8px;flex-wrap:wrap">
          <button class={"f #{if @app_filter == "all", do: "on"}"} phx-click="filter" phx-value-id="all">All runs</button>
          <%= for a <- @apps do %>
            <button class={"f #{if @app_filter == a["id"], do: "on"}"} phx-click="filter" phx-value-id={a["id"]}>
              <%= a["name"] %>
            </button>
          <% end %>
        </div>
      </div>

      <section class="kanban">
        <%= for {col, items} <- @col_items do %>
          <div class="col">
            <div class={"col-h tone-#{col["tone"]}"}>
              <span class="dot"></span>
              <span class="cn"><%= col["name"] %></span>
              <span class="ct"><%= length(items) %></span>
            </div>
            <div class="col-body">
              <%= if items == [] do %>
                <div class="empty">Nothing here.</div>
              <% else %>
                <%= for r <- items do %>
                  <button class="card" phx-click="open-run" phx-value-id={r["run_id"]}>
                    <div class="row between">
                      <span class="kind"><%= r["provider"] || "run" %></span>
                      <%= if r["live"] do %>
                        <span class="live"><span class="pd"></span>live</span>
                      <% end %>
                    </div>
                    <div class="ttl"><%= r["task"] || r["run_id"] %></div>
                    <div class="foot">
                      <span class="ago"><%= Format.format_relative(r["last_event_ms"] || r["created_at_ms"]) %></span>
                      <span class="ago"><%= r["events"] || 0 %> events</span>
                    </div>
                  </button>
                <% end %>
              <% end %>
            </div>
          </div>
        <% end %>
      </section>
    </div>
    """
  end
end
