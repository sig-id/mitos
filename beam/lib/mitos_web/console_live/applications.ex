defmodule MitosWeb.ConsoleLive.Applications do
  @moduledoc "Applications grid + add-app form. Ported from `Applications.svelte`."
  use MitosWeb, :live_view
  use MitosWeb.ConsoleLive.Shared

  @impl true
  def handle_params(_params, _url, socket) do
    {:noreply, assign(socket, active_nav: "agents") |> assign_new(:q, fn -> "" end) |> assign_new(:source, fn -> "" end)}
  end

  defp refresh_page(socket), do: socket

  @impl true
  def handle_event("search", %{"value" => q}, socket), do: {:noreply, assign(socket, q: q)}

  def handle_event("source", %{"value" => s}, socket), do: {:noreply, assign(socket, source: s)}

  def handle_event("add-app", %{"source" => source}, socket) do
    source = String.trim(source)

    case source do
      "" ->
        {:noreply, socket}

      _ ->
        case MitosWeb.Console.add_app(source) do
          {:ok, install} ->
            name = get_in(install_name(install), ["app", "name"]) || "application"
            socket =
              socket
              |> assign(source: "", error: nil, toast: "Added #{name}")
              |> assign_defaults()

            {:noreply, socket}

          {:error, reason} ->
            {:noreply, assign(socket, error: format_error(reason))}
        end
    end
  end

  def handle_event("run-app", %{"app" => app_id, "entrypoint" => _ep}, socket) do
    {:noreply, assign(socket, launch_open: true, launch_app: app_id, launch_entrypoint: nil)}
  end

  defp filtered(apps, q) do
    ql = String.downcase(q || "")

    if ql == "" do
      apps
    else
      Enum.filter(apps, fn a ->
        haystack = String.downcase("#{a["name"]} #{a["description"] || ""} #{a["id"] || ""}")
        String.contains?(haystack, ql)
      end)
    end
  end

  defp format_error(reason) when is_exception(reason), do: Exception.message(reason)
  defp format_error(reason), do: inspect(reason)

  # The install result from `Applications.add_source/3` may be a map keyed by
  # "app" (a public app view) or nested under "install". Pull the name out.
  defp install_name(%{"install" => install}), do: install_name(install)
  defp install_name(%{"app" => app}), do: app
  defp install_name(other) when is_map(other), do: other
  defp install_name(_), do: %{}

  @impl true
  def render(assigns) do
    apps = filtered(assigns.apps, assigns.q)
    assigns = assign(assigns, filtered_apps: apps)

    ~H"""
    <div class="page">
      <div class="head">
        <div>
          <h1>Applications</h1>
          <p class="lead">Worker definitions installed in this control plane. Run one to launch an agent.</p>
        </div>
      </div>

      <form phx-submit="add-app" id="add-app-form">
        <div class="app-add">
          <input class="input" name="source" value={@source} phx-change="source"
            placeholder="git URL or local app directory" />
          <button class="btn primary" type="submit">Add</button>
        </div>
      </form>

      <div class="app-bar">
        <input class="input" value={@q} phx-keyup="search" phx-debounce="100" placeholder="Search applications…" />
      </div>

      <div class="app-grid">
        <%= if @filtered_apps == [] do %>
          <div class="empty">No applications. Add one above.</div>
        <% else %>
          <%= for a <- @filtered_apps do %>
            <div class="app-card">
              <div class="ic"><%= length(a["entrypoints"] || []) %></div>
              <div class="nm"><%= a["name"] %></div>
              <div class="ct"><%= a["version"] || "local" %></div>
              <p class="tg"><%= a["description"] || "An agentic application." %></p>
              <div class="row" style="gap:6px;margin-top:10px;flex-wrap:wrap">
                <%= for e <- a["entrypoints"] || [] do %>
                  <span class="pill"><%= e["pipeline"] || e["id"] %></span>
                <% end %>
              </div>
              <div class="conns">
                <%= for b <- Enum.take(a["io"] && a["io"]["inputs"] || [], 4) do %>
                  <MitosWeb.Components.connector_chip id={b["connector"] || "manual"} size={18} />
                <% end %>
              </div>
              <%= if (a["entrypoints"] || []) != [] do %>
                <button class="launch" phx-click="run-app" phx-value-app={a["id"]} phx-value-entrypoint={(hd(a["entrypoints"]))["id"]}>Run →</button>
              <% end %>
            </div>
          <% end %>
        <% end %>
      </div>
    </div>
    """
  end
end
