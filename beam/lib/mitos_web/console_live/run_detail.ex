defmodule MitosWeb.ConsoleLive.RunDetail do
  @moduledoc """
  Run inspector.

  An application run is a thin envelope (requested → queued → running →
  done/failed); the real per-capability work — and the structured RESULT each
  capability writes — lives in the run's child `wl-*` step traces. This page
  surfaces that hierarchy: a header with the run's status and (on failure) a
  human-readable reason, a vertical list of pipeline steps each rendered with
  its verdict and RESULT body (findings, selection, evidence), and the raw
  envelope timeline collapsed under "Raw events".
  """

  use MitosWeb, :live_view
  use MitosWeb.ConsoleLive.Shared

  alias MitosWeb.Format

  @impl true
  def handle_params(%{"id" => id}, _url, socket) do
    socket =
      socket
      |> assign(
        active_nav: "runs",
        run_id: id,
        show_raw: false,
        steer_text: "",
        error: nil
      )
      |> load_run(id)

    {:noreply, socket}
  end

  defp load_run(socket, id) do
    case MitosWeb.Console.fetch_run(id) do
      {:ok, run} ->
        events = Enum.map(run["events"] || [], &normalize_event/1)
        rows = Enum.map(events, &timeline_row/1)
        steps = Mitos.Runs.step_traces(id)
        reason = run_reason(events)

        socket
        |> assign(
          run: run,
          run_not_found: false,
          starting: false,
          events: events,
          timeline_rows: rows,
          steps: steps,
          reason: reason
        )

      {:error, :not_found} ->
        starting = String.starts_with?(id, "app-") || String.starts_with?(id, "agent-")

        socket
        |> assign(
          run: nil,
          run_not_found: not starting,
          starting: starting,
          events: [],
          timeline_rows: [],
          steps: [],
          reason: nil
        )
    end
  end

  defp refresh_page(socket) do
    if socket.assigns[:run_id], do: load_run(socket, socket.assigns.run_id), else: socket
  end

  defp normalize_event(event), do: Map.get(event, "event", event)

  defp run_reason(events) do
    case Enum.find(events, &(&1["type"] == "task_failed")) do
      %{"payload" => %{"reason" => reason}} when is_binary(reason) -> reason
      _ -> nil
    end
  end

  defp timeline_row(event) do
    type = event["type"] || event["event_type"] || "event"
    payload = event["payload"] || event["params"] || event

    %{
      seq: event["seq"] || event["sequence"] || "?",
      type: type,
      time: Format.format_iso_time(event["ts"]) || Format.format_time(event["timestamp_ms"]),
      label: label_for(type, payload),
      detail: event["rationale"] || payload["reason"] || payload["status"] || ""
    }
  end

  defp label_for("task_enqueued", _p), do: "Queued"
  defp label_for("task_scheduled", _p), do: "Scheduled"
  defp label_for("task_running", _p), do: "Started"
  defp label_for("task_done", _p), do: "Completed"
  defp label_for("task_failed", _p), do: "Failed"
  defp label_for("signal_received", _p), do: "Run requested"
  defp label_for("delivery_sent", p), do: "Delivered to #{p["sink"] || "sink"}"
  defp label_for(type, _p), do: Format.humanize(type)

  # ----- events -----

  @impl true
  def handle_event("toggle-raw", %{}, socket), do: {:noreply, update(socket, :show_raw, &(!&1))}

  def handle_event("steer-text", %{"value" => v}, socket) do
    {:noreply, assign(socket, steer_text: v)}
  end

  def handle_event("steer", %{"interaction" => interaction}, socket) do
    %{run_id: run_id, steer_text: text} = socket.assigns
    opts = if text && text != "", do: [text: String.trim(text)], else: []

    case MitosWeb.Console.steer(run_id, safe_atom(interaction), opts) do
      :ok ->
        {:noreply, assign(socket, steer_text: "", error: nil) |> load_run(run_id)}

      {:error, reason} ->
        {:noreply, assign(socket, error: format_error(reason))}
    end
  end

  # `String.to_existing_atom/1` crashes the LiveView on unknown client values;
  # validate against an allow-list instead.
  @steer_atoms ~w(approve interrupt prompt answer select_option)a
  defp safe_atom(name) do
    atom = if is_binary(name), do: String.to_atom(name), else: nil
    if atom in @steer_atoms, do: atom, else: :prompt
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

  # step status -> {tone, label}
  defp step_status_view("completed"), do: {"ok", "Done"}
  defp step_status_view("failed"), do: {"err", "Failed"}
  defp step_status_view("running"), do: {"live", "Running"}
  defp step_status_view("scheduled"), do: {"warn", "Scheduled"}
  defp step_status_view(_), do: {"muted", "Queued"}

  # verdict -> tone
  defp verdict_tone("PASS"), do: "ok"
  defp verdict_tone("FAIL"), do: "err"
  defp verdict_tone("FIX"), do: "warn"
  defp verdict_tone("SKIP"), do: "muted"
  defp verdict_tone(_), do: "muted"

  defp sev_tone("BLOCKER"), do: "err"
  defp sev_tone("CRITICAL"), do: "err"
  defp sev_tone("HIGH"), do: "err"
  defp sev_tone("MAJOR"), do: "warn"
  defp sev_tone("MEDIUM"), do: "warn"
  defp sev_tone("LOW"), do: "info"
  defp sev_tone("INFO"), do: "muted"
  defp sev_tone("MINOR"), do: "info"
  defp sev_tone(_), do: "muted"

  @impl true
  def render(assigns) do
    ~H"""
    <div class="page run-detail">
      <%= if @run_not_found do %>
        <div class="rd-empty">Run not found.</div>
      <% else %>
        <% run = @run %>

        <header class="rd-head">
          <div class="rd-title-row">
            <div class="grow">
              <h1 class="rd-title"><%= run_title(run, @run_id) %></h1>
              <div class="rd-meta">
                <%= if run do %>
                  <span class={"rd-status tone-#{status_tone(run["status"])}"}>
                    <span class="dot"></span><%= Format.status_label(run["status"]) %>
                  </span>
                  <span class="rd-chip"><%= run["event_count"] || length(@events) %> events</span>
                  <%= if @steps != [] do %><span class="rd-chip"><%= length(@steps) %> steps</span><% end %>
                <% else %>
                  <span class="rd-status tone-live"><span class="dot pulse"></span>Starting</span>
                <% end %>
                <span class="rd-id mono" title={@run_id}><%= Format.compact_id(@run_id) %></span>
              </div>
            </div>
          </div>
        </header>

        <%= if @starting && @events == [] do %>
          <div class="rd-starting">
            <div class="spinner"></div>
            <p>Starting the run — steps will appear here as the agent works.</p>
          </div>
        <% end %>

        <%= if @reason do %>
          <% fr = Mitos.Runs.format_reason(@reason) %>
          <div class="rd-error">
            <div class="rd-error-h">
              <span class="rd-error-icon">✕</span>
              <strong><%= fr && fr[:summary] || "Run failed" %></strong>
            </div>
            <%= if fr && fr[:detail] not in ["", nil] do %>
              <div class="rd-error-detail mono"><%= fr[:detail] %></div>
            <% end %>
          </div>
        <% end %>

        <%= if @steps != [] do %>
          <section class="rd-section">
            <h2 class="rd-sec-h">Pipeline</h2>
            <ol class="rd-steps">
              <%= for {s, i} <- Enum.with_index(@steps) do %>
                <li class={"rd-step is-#{s[:status]}"}>
                  <div class="rd-step-rail">
                    <span class="rd-step-num"><%= i + 1 %></span>
                  </div>
                  <div class="rd-step-body">
                    <div class="rd-step-top">
                      <span class="rd-step-name"><%= Format.humanize(s[:name]) %></span>
                      <span class={"rd-step-status tone-#{elem(step_status_view(s[:status]), 0)}"}>
                        <span class="dot"></span><%= elem(step_status_view(s[:status]), 1) %>
                      </span>
                    </div>

                    <%= if s[:reason] && s[:status] == "failed" do %>
                      <% fr = Mitos.Runs.format_reason(s[:reason]) %>
                      <div class="rd-step-error">
                        <%= if fr, do: fr[:summary], else: s[:reason] %>
                      </div>
                    <% end %>

                    <%= if s[:result] do %>
                      <.result_card result={s[:result]} />
                    <% else %>
                      <%= if s[:status] in ["completed"] do %>
                        <div class="rd-step-nosult">No RESULT artifact.</div>
                      <% end %>
                    <% end %>

                    <div class="rd-step-foot mono">
                      <span><%= s[:wl_id] %></span>
                      <span><%= s[:events] %> events</span>
                    </div>
                  </div>
                </li>
              <% end %>
            </ol>
          </section>
        <% end %>

        <section class="rd-section">
          <button class="rd-collapse" phx-click="toggle-raw">
            <%= if @show_raw, do: "▾ Hide raw events", else: "▸ Raw events" %>
          </button>
          <%= if @show_raw do %>
            <div class="rd-raw">
              <%= if @timeline_rows == [] do %>
                <div class="rd-empty-inline">No events.</div>
              <% else %>
                <%= for r <- @timeline_rows do %>
                  <div class="rd-raw-row">
                    <span class="rd-raw-time mono"><%= r.time %></span>
                    <span class="rd-raw-type mono"><%= r.type %></span>
                    <span class="rd-raw-label"><%= r.label %></span>
                  </div>
                <% end %>
              <% end %>
            </div>
          <% end %>
        </section>
      <% end %>
    </div>
    """
  end

  # ----- structured RESULT renderer -----

  defp status_tone("running"), do: "live"
  defp status_tone(status) when status in ["done", "completed"], do: "ok"
  defp status_tone(status) when status in ["failed", "error"], do: "err"
  defp status_tone("parked"), do: "warn"
  defp status_tone(_), do: "muted"

  attr(:result, :map, required: true)

  defp result_card(assigns) do
    %{result: r} = assigns
    verdict = r["verdict"]
    summary = r["summary"]
    findings = List.wrap(r["findings"])
    selection = r["selection"]
    evidence = List.wrap(r["evidence"])
    stats = r["stats"] || %{}

    assigns =
      assign(assigns,
        verdict: verdict,
        summary: summary,
        findings: findings,
        selection: selection,
        evidence: evidence,
        stats: stats
      )

    ~H"""
    <div class="rd-result">
      <div class="rd-result-top">
        <%= if @verdict do %>
          <span class={"rd-verdict v-#{verdict_tone(@verdict)}"}><%= @verdict %></span>
        <% end %>
        <%= if @summary do %><p class="rd-result-summary"><%= @summary %></p><% end %>
      </div>

      <%= if @selection do %>
        <div class="rd-result-block">
          <div class="rd-result-k">Scope</div>
          <div class="rd-result-v">
            <span class="rd-pill"><%= @selection["scope_type"] %></span>
            <%= if @selection["scope_target"] do %><span class="rd-pill"><%= @selection["scope_target"] %></span><% end %>
            <%= if @selection["concern"] do %>
              <span class="rd-concern"><%= @selection["concern"] %></span>
            <% end %>
          </div>
        </div>
      <% end %>

      <%= if @findings != [] do %>
        <div class="rd-result-block">
          <div class="rd-result-k"><%= length(@findings) %> findings</div>
          <ul class="rd-findings">
            <%= for f <- @findings do %>
              <li class="rd-finding">
                <div class="rd-finding-h">
                  <% sev = f["severity"] %>
                  <%= if sev do %>
                    <span class={"rd-sev s-#{sev_tone(sev)}"}><%= sev %></span>
                  <% end %>
                  <%= if f["id"] do %><span class="rd-finding-id mono"><%= f["id"] %></span><% end %>
                  <%= if f["path"] do %>
                    <span class="rd-loc mono">
                      <%= f["path"] %><%= if f["line"], do: ":#{f["line"]}" %>
                    </span>
                  <% end %>
                </div>
                <%= if f["problem"] || f["issue"] do %>
                  <p class="rd-finding-issue"><%= f["problem"] || f["issue"] %></p>
                <% end %>
                <%= if f["suggested_fix"] || f["fix"] do %>
                  <p class="rd-finding-fix"><span class="rd-fix-k">Fix:</span> <%= f["suggested_fix"] || f["fix"] %></p>
                <% end %>
                <%= if f["why_it_matters"] do %>
                  <p class="rd-finding-why"><span class="rd-fix-k">Why:</span> <%= f["why_it_matters"] %></p>
                <% end %>
                <%= if f["code"] do %><pre class="rd-code mono"><%= f["code"] %></pre><% end %>
              </li>
            <% end %>
          </ul>
        </div>
      <% end %>

      <%= if @evidence != [] do %>
        <details class="rd-result-block rd-evidence">
          <summary>Evidence (<%= length(@evidence) %>)</summary>
          <ul>
            <%= for e <- @evidence do %><li class="mono"><%= e %></li><% end %>
          </ul>
        </details>
      <% end %>
    </div>
    """
  end
end
