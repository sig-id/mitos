defmodule MitosWeb.ConsoleLive.Shared do
  @moduledoc """
  Behaviour shared by every console LiveView.

  Injects the shell-level concerns that are identical across pages:
    * default assigns for the sidebar (apps, runs, counts),
    * the periodic refresh tick (4s, matching the SPA's poll cadence),
    * the open/close handlers for the launch wizard (`@launch_open`),
    * the page error/toast banners.

  Pages `use MitosWeb.ConsoleLive.Shared` and add their own `handle_event` /
  `handle_info` clauses — multiple clauses of the same function compose, so the
  page keeps its specific handlers while sharing these.
  """

  defmacro __using__(_opts) do
    quote do
      import MitosWeb.ConsoleLive.Shared, only: [assign_defaults: 1, refresh: 1]

      @refresh_interval_ms 4_000

      @impl Phoenix.LiveView
      def mount(_params, _session, socket) do
        socket =
          socket
          |> assign_defaults()
          |> assign(launch_open: false, launch_app: nil, launch_entrypoint: nil, error: nil, toast: nil)

        if connected?(socket) do
          :timer.send_interval(@refresh_interval_ms, :refresh)
        end

        {:ok, socket}
      end

      # Sidebar "Run agent" button.
      @impl Phoenix.LiveView
      def handle_event("open-launch", %{}, socket) do
        {:noreply, assign(socket, launch_open: true, launch_app: nil, launch_entrypoint: nil)}
      end

      def handle_event("open-launch", params, socket) do
        {:noreply,
         assign(socket,
           launch_open: true,
           launch_app: params["app"],
           launch_entrypoint: params["entrypoint"]
         )}
      end

      def handle_event("close-launch", %{}, socket) do
        {:noreply, assign(socket, launch_open: false)}
      end

      def handle_event("dismiss-error", %{}, socket), do: {:noreply, assign(socket, error: nil)}
      def handle_event("dismiss-toast", %{}, socket), do: {:noreply, assign(socket, toast: nil)}

      # Sidebar navigation — `push_navigate` swaps LiveViews without a full reload.
      def handle_event("nav-dashboard", %{}, socket),
        do: {:noreply, push_navigate(socket, to: ~p"/console")}

      def handle_event("nav-applications", %{}, socket),
        do: {:noreply, push_navigate(socket, to: ~p"/console/applications")}

      def handle_event("nav-connectors", %{}, socket),
        do: {:noreply, push_navigate(socket, to: ~p"/console/connectors")}

      def handle_event("nav-runs", %{}, socket),
        do: {:noreply, push_navigate(socket, to: ~p"/console/runs")}

      def handle_event("nav-app", %{"id" => id}, socket),
        do: {:noreply, push_navigate(socket, to: ~p"/console/applications/#{id}")}

      # The launch wizard (LiveComponent) reports a successful run: close the
      # modal and deep-link to the run trace.
      @impl Phoenix.LiveView
      def handle_info({:launch_launched, run_id}, socket) when is_binary(run_id) do
        socket =
          socket
          |> assign(launch_open: false)
          |> put_flash(:info, "Run started")

        {:noreply, push_navigate(socket, to: ~p"/console/runs/#{run_id}")}
      end

      def handle_info({:launch_launched, _}, socket) do
        {:noreply, assign(socket, launch_open: false)}
      end

      # Shared refresh: reload the shell-level sidebar data, then hand off to
      # the page's `refresh_page/1` to reload its own view data.
      @impl Phoenix.LiveView
      def handle_info(:refresh, socket) do
        {:noreply, socket |> MitosWeb.ConsoleLive.Shared.refresh() |> refresh_page()}
      end

      defp refresh_page(socket), do: socket

      defoverridable mount: 3, refresh_page: 1
    end
  end

  @doc "Loads the sidebar/nav data (apps, runs, connectors, sinks, counts)."
  def assign_defaults(socket) do
    import Phoenix.Component

    apps = MitosWeb.Console.list_apps()
    runs = MitosWeb.Console.list_runs()

    socket
    |> assign_new(:active_nav, fn -> "dashboard" end)
    |> assign(apps: apps, runs: runs, app_count: length(apps), run_count: length(runs))
  end

  @doc "Reload just the sidebar counts (cheap), without disturbing page state."
  def refresh(socket) do
    import Phoenix.Component

    apps = MitosWeb.Console.list_apps()
    runs = MitosWeb.Console.list_runs()
    assign(socket, apps: apps, runs: runs, app_count: length(apps), run_count: length(runs))
  end
end
