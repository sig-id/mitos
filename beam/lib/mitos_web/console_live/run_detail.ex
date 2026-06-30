defmodule MitosWeb.ConsoleLive.RunDetail do
  @moduledoc """
  Run trace — the outer pipeline timeline + the inner harness trace + steering.
  Ported from `RunDetail.svelte`; here it is a deep-linkable LiveView page.
  """
  use MitosWeb, :live_view
  use MitosWeb.ConsoleLive.Shared

  alias MitosWeb.Format

  @inner_types ~w(assistant_text tool_use tool_result token_usage status)

  @impl true
  def handle_params(%{"id" => id}, _url, socket) do
    socket =
      socket
      |> assign(active_nav: "runs", run_id: id, advanced: false, steer_text: "", error: nil)
      |> load_run(id)

    {:noreply, socket}
  end

  defp load_run(socket, id) do
    case MitosWeb.Console.fetch_run(id) do
      {:ok, run} ->
        events = Enum.map(run["events"] || [], &normalize_event/1)
        rows = Enum.map(events, &timeline_row/1)
        inner = Enum.filter(events, fn e -> Map.get(e, "type") in @inner_types end)

        socket
        |> assign(
          run: run,
          run_not_found: false,
          starting: false,
          events: events,
          timeline_rows: rows,
          inner_rows: inner
        )

      {:error, :not_found} ->
        # A run we just launched may not have written its trace dir yet. Keep
        # the page mounted in a "starting" state; the 4s refresh picks it up.
        starting = String.starts_with?(id, "app-") or String.starts_with?(id, "agent-")

        socket
        |> assign(
          run: nil,
          run_not_found: not starting,
          starting: starting,
          events: [],
          timeline_rows: [],
          inner_rows: []
        )
    end
  end

  defp refresh_page(socket) do
    if socket.assigns[:run_id], do: load_run(socket, socket.assigns.run_id), else: socket
  end

  defp normalize_event(event), do: Map.get(event, "event", event)

  defp timeline_row(event) do
    type = event["type"] || event["event_type"] || "event"
    payload = event["payload"] || event["params"] || event

    %{
      seq: event["seq"] || event["sequence"] || "?",
      type: type,
      time: Format.format_iso_time(event["ts"]) || Format.format_time(event["timestamp_ms"]),
      label: label_for(type, payload),
      detail:
        event["rationale"] || payload["reason"] || payload["summary"] ||
          payload["status"] || "",
      status: status_for(type, payload)
    }
  end

  defp label_for("task_enqueued", _p), do: "Queued"
  defp label_for("task_scheduled", _p), do: "Scheduled"
  defp label_for("task_running", _p), do: "Started"
  defp label_for("task_status_changed", p), do: Format.humanize(p["status"] || "updated")
  defp label_for("task_done", _p), do: "Completed"

  defp label_for("capability_started", p),
    do: "#{Format.humanize(p["capability_id"] || p["capability_ref"] || "step")} running"

  defp label_for("capability_finished", p),
    do: "#{Format.humanize(p["capability_id"] || p["capability_ref"] || "step")} done"

  defp label_for("signal_received", _p), do: "Run requested"
  defp label_for("delivery_sent", p), do: "Delivered to #{p["sink"] || "sink"}"
  defp label_for("delivery_failed", p), do: "Delivery to #{p["sink"] || "sink"} failed"
  defp label_for("verification_recorded", _p), do: "Verification"
  defp label_for("finish", _p), do: "Completed"
  defp label_for("abort", p), do: "Aborted#{if p["cause"], do: " · #{p["cause"]}"}"
  defp label_for(type, _p), do: Format.humanize(type)

  defp status_for(type, p) do
    cond do
      String.contains?(type, "failed") or String.contains?(type, "denied") or p["status"] == "failed" ->
        "error"

      String.contains?(type, "approval") or String.contains?(type, "parked") or p["status"] == "parked" ->
        "parked"

      String.contains?(type, "finished") or String.contains?(type, "done") or String.contains?(type, "sent") ->
        "done"

      String.contains?(type, "started") or String.contains?(type, "running") ->
        "running"

      true ->
        "idle"
    end
  end

  @impl true
  def handle_event("toggle-advanced", %{}, socket) do
    {:noreply, update(socket, :advanced, &(!&1))}
  end

  def handle_event("steer-text", %{"value" => v}, socket) do
    {:noreply, assign(socket, steer_text: v)}
  end

  def handle_event("steer", %{"interaction" => interaction}, socket) do
    %{run_id: run_id, steer_text: text} = socket.assigns
    opts = if text && text != "", do: [text: String.trim(text)], else: []

    case MitosWeb.Console.steer(run_id, String.to_existing_atom(interaction), opts) do
      :ok ->
        {:noreply, assign(socket, steer_text: "", error: nil) |> load_run(run_id)}

      {:error, reason} ->
        {:noreply, assign(socket, error: format_error(reason))}
    end
  end

  defp format_error(reason) when is_exception(reason), do: Exception.message(reason)
  defp format_error(reason), do: inspect(reason)

  defp run_title(nil, run_id), do: Format.compact_id(run_id)

  defp run_title(run, _run_id) do
    cond do
      get_in(run, ["manifest", "task"]) not in ["", nil] -> run["manifest"]["task"]
      true -> Format.compact_id(run["run_id"])
    end
  end

  @impl true
  def render(assigns) do
    ~H"""
    <div class="page" style="max-width: 880px;">
      <%= if @run_not_found do %>
        <div class="empty">Run not found.</div>
      <% else %>
        <% run = @run %>
        <div class="run-hd">
          <div class="grow">
            <div class="run-ttl"><%= run_title(run, @run_id) %></div>
            <div class="row" style="gap:10px;margin-top:6px;flex-wrap:wrap">
              <%= if run do %>
                <span class="k-chip"><%= Format.status_label(run["status"]) %></span>
                <span class="ago-chip"><%= run["event_count"] || length(@events) %> events</span>
              <% else %>
                <span class="k-chip">Starting</span>
              <% end %>
              <span class="ago-chip mono" title={@run_id}><%= Format.compact_id(@run_id) %></span>
            </div>
          </div>
        </div>

        <div class="run-sec">
          <div class="sec-h">
            <span>Outer pipeline timeline</span>
            <button class={"adv #{if @advanced, do: "on"}"} phx-click="toggle-advanced">
              <%= if @advanced, do: "Hide steering", else: "Steer" %>
            </button>
          </div>
          <div class="trace">
            <%= if @starting and @timeline_rows == [] do %>
              <div class="empty">Starting the run — events appear here as the agent works.</div>
            <% else %>
              <%= if @timeline_rows == [] do %>
                <div class="empty">No events yet.</div>
              <% else %>
                <%= for r <- @timeline_rows do %>
                  <div class={"t-step #{if r.status == "running", do: "live"}"}>
                    <div class="rail">
                      <span class={"dot #{cond do
                        r.status == "done" -> "ok"
                        r.status == "parked" -> "warn"
                        r.status == "error" -> "err"
                        true -> ""
                      end}"}></span>
                      <span class="line"></span>
                    </div>
                    <div class="body">
                      <div class="row between">
                        <span class="lbl"><%= r.label %></span>
                        <span class="seq mono"><%= if r.time != "", do: r.time, else: "#" <> to_string(r.seq) %></span>
                      </div>
                      <%= if r.detail not in ["", nil] do %>
                        <div class="ph"><%= r.detail %></div>
                      <% end %>
                    </div>
                  </div>
                <% end %>
              <% end %>
            <% end %>
          </div>
        </div>

        <%= if @advanced do %>
          <div class="run-sec steer">
            <div class="sec-h"><span>Steer the agent</span></div>
            <%= if @error do %><div class="err"><%= @error %></div><% end %>
            <div class="steer-btns">
              <button class="btn ghost sm" phx-click="steer" phx-value-interaction="approve">Approve</button>
              <button class="btn ghost sm" phx-click="steer" phx-value-interaction="interrupt">Stop</button>
            </div>
            <textarea rows="2" phx-keyup="steer-text" placeholder="Answer or instruction for the agent"><%= @steer_text %></textarea>
            <div class="row" style="justify-content:flex-end;margin-top:8px">
              <button class="btn primary sm" phx-click="steer" phx-value-interaction="prompt" disabled={String.trim(@steer_text) == ""}>Send</button>
            </div>
          </div>
        <% end %>

        <%= if @inner_rows != [] do %>
          <div class="run-sec">
            <div class="sec-h"><span>Inner harness trace</span></div>
            <div class="inner">
              <%= for e <- @inner_rows do %>
                <pre class="mono"><%= Jason.encode!(e, pretty: true) %></pre>
              <% end %>
            </div>
          </div>
        <% end %>
      <% end %>
    </div>
    """
  end
end
