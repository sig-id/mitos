defmodule MitosWeb.ConsoleLive.AppDetail do
  @moduledoc "One application: overview, I/O bindings, entrypoints, recent runs. Ported from `AppDetail.svelte`."
  use MitosWeb, :live_view
  use MitosWeb.ConsoleLive.Shared

  alias MitosWeb.Format

  @impl true
  def handle_params(%{"id" => id}, _url, socket) do
    socket =
      socket
      |> assign(active_nav: "agents", app_id: id)
      |> load_app(id)

    {:noreply, socket}
  end

  defp load_app(socket, id) do
    case MitosWeb.Console.fetch_app(id) do
      {:ok, app} ->
        app_runs =
          Enum.filter(socket.assigns[:runs] || [], fn r ->
            app_id = app["id"] || "\0"
            String.contains?(r["task"] || r["run_id"] || "", app_id)
          end)

        assign(socket, app: app, app_not_found: false, app_runs: app_runs)

      {:error, :not_found} ->
        assign(socket, app: nil, app_not_found: true, app_runs: [])
    end
  end

  defp refresh_page(socket) do
    if socket.assigns[:app_id], do: load_app(socket, socket.assigns.app_id), else: socket
  end

  @impl true
  def handle_event("run-entrypoint", %{}, socket) do
    {:noreply, assign(socket, launch_open: true, launch_app: socket.assigns.app_id)}
  end

  def handle_event("open-run", %{"id" => id}, socket) do
    {:noreply, push_navigate(socket, to: ~p"/console/runs/#{id}")}
  end

  @impl true
  def render(assigns) do
    ~H"""
    <div class="page" style="max-width: 1100px;">
      <%= if @app_not_found do %>
        <div class="missing" style="padding:60px;text-align:center;color:var(--mt-text-muted);">
          Application not found. <a href="/console">Back</a>
        </div>
      <% else %>
        <% app = @app %>
        <div class="detail-head">
          <button class="back" phx-click="nav-dashboard">← Dashboard</button>
          <div class="ic"><%= length(app["entrypoints"] || []) %></div>
          <div class="grow">
            <h1><%= app["name"] %></h1>
            <div class="sub"><%= app["description"] || Format.compact_id(app["app_id"]) %></div>
          </div>
          <button class="btn primary" phx-click="run-entrypoint">Run entrypoint</button>
        </div>

        <div class="detail-grid">
          <div class="card">
            <div class="ch">Overview</div>
            <div class="cb">
              <div class="kv-row"><span>Version</span><b><%= app["version"] || "local" %></b></div>
              <div class="kv-row"><span>Pipelines</span><b><%= length(app["pipelines"] || []) %></b></div>
              <div class="kv-row"><span>Capabilities</span><b><%= length(app["capabilities"] || []) %></b></div>
              <div class="kv-row"><span>Trust tier</span><b><%= app["requirements"] && app["requirements"]["trust_tier"] || "—" %></b></div>
            </div>
          </div>

          <div class="card">
            <div class="ch">I/O bindings</div>
            <div class="cb">
              <% inputs = app["io"] && app["io"]["inputs"] || [] %>
              <div class="sub2">Inputs</div>
              <%= if inputs == [] do %>
                <div class="muted2">Manual / web only.</div>
              <% else %>
                <%= for b <- inputs do %>
                  <div class="conn">
                    <MitosWeb.Components.connector_chip id={b["connector"] || "manual"} size={22} />
                    <span class="ci"><%= b["connector"] %> → <%= b["entrypoint"] || "—" %></span>
                  </div>
                <% end %>
              <% end %>
              <% outputs = app["io"] && app["io"]["outputs"] || [] %>
              <div class="sub2">Outputs</div>
              <%= if outputs == [] do %>
                <div class="muted2">Web receipt.</div>
              <% else %>
                <%= for b <- outputs do %>
                  <div class="conn">
                    <span class="sink-pill"><%= b["sink"] || "web" %></span>
                    <span class="ci">on <%= b["on"] || "run.done" %></span>
                  </div>
                <% end %>
              <% end %>
            </div>
          </div>

          <div class="card span2">
            <div class="ch">Harnesses</div>
            <div class="cb">
              <% harnesses = (app["requirements"] && app["requirements"]["harnesses"]) || [] %>
              <%= if harnesses == [] do %>
                <div class="muted2">Not declared.</div>
              <% else %>
                <%= for h <- harnesses do %>
                  <div class="h-row"><MitosWeb.Components.harness_badge id={h} /></div>
                <% end %>
              <% end %>
            </div>
          </div>
        </div>

        <div class="card">
          <div class="ch">Entrypoints</div>
          <div class="ent-list">
            <%= for e <- app["entrypoints"] || [] do %>
              <div class="ent">
                <div class="grow">
                  <div class="et"><%= e["title"] || e["id"] %></div>
                  <div class="ed muted"><%= e["description"] || "An application entrypoint." %></div>
                  <div class="meta">
                    <span class="pill">pipeline: <%= e["pipeline"] %></span>
                    <span class="pill">workspace: <%= Format.humanize(workspace_kind(e)) %></span>
                  </div>
                </div>
                <button class="btn ghost sm" phx-click="run-entrypoint">Run</button>
              </div>
            <% end %>
          </div>
        </div>

        <div class="card">
          <div class="ch">Recent runs (<%= length(@app_runs) %>)</div>
          <div class="run-list">
            <%= if @app_runs == [] do %>
              <div class="muted2">No runs yet.</div>
            <% else %>
              <%= for r <- Enum.take(@app_runs, 12) do %>
                <button class="run-row" phx-click="open-run" phx-value-id={r["run_id"]}>
                  <span class="rt mono"><%= r["task"] || Format.compact_id(r["run_id"]) %></span>
                  <MitosWeb.Components.status_pill status={r["status"]} />
                  <span class="muted"><%= Format.format_relative(r["last_event_ms"] || r["created_at_ms"]) %></span>
                </button>
              <% end %>
            <% end %>
          </div>
        </div>
      <% end %>
    </div>
    """
  end

  defp workspace_kind(%{"workspace" => %{"kind" => kind}}), do: kind || "none"
  defp workspace_kind(%{"workspace" => kind}) when is_binary(kind), do: kind
  defp workspace_kind(_), do: "none"
end
