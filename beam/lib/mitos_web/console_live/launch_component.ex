defmodule MitosWeb.ConsoleLive.LaunchComponent do
  @moduledoc """
  Modal wizard that runs one application entrypoint. Ported 1:1 from the Svelte
  `LaunchWizard.svelte` — three steps (application, inputs, review), typed
  input/parameter fields derived from the entrypoint's schema, and a launch
  that calls the backend op directly.
  """
  use MitosWeb, :live_component

  alias MitosWeb.Console

  @steps ["Application", "Inputs", "Review"]

  @impl true
  def update(assigns, socket) do
    app_id = assigns[:app]
    entrypoint_id = assigns[:entrypoint]

    socket =
      socket
      |> assign(assigns)
      |> assign_new(:step, fn -> if app_id in ["", nil], do: 0, else: 1 end)
      |> assign_new(:error, fn -> nil end)
      |> assign_new(:launching, fn -> false end)
      |> assign_new(:workspace_ref, fn -> "" end)
      |> assign_new(:input_values, fn -> %{} end)
      |> assign_new(:parameter_values, fn -> %{} end)
      |> assign(app_id: app_id, entrypoint_id: entrypoint_id)

    {:ok, socket}
  end

  @impl true
  def render(assigns) do
    ~H"""
    <div class="backdrop" phx-click-away="close-launch" phx-window-keydown="close-launch" phx-key="escape">
      <div class="modal" style="--w: 720px">
        <div class="head">
          <h3><%= if @step == 0, do: "Run an agent", else: "Run · #{app_name(@apps, @app_id)}" %></h3>
          <button class="x" phx-click="close-launch" aria-label="Close">✕</button>
        </div>

        <div class="body">
          <%= if @step > 0 do %>
            <div class="steps">
              <%= for {label, i} <- Enum.with_index(steps()) do %>
                <div class={"step #{if i < @step, do: "done", else: (if i == @step, do: "active")}>"}>
                  <span class="num"><%= if i < @step, do: "✓", else: i + 1 %></span>
                  <span class="lbl"><%= label %></span>
                </div>
                <%= if i < length(steps()) - 1 do %><span class="sep"></span><% end %>
              <% end %>
            </div>
          <% end %>

          <%= if @error do %><div class="err"><%= @error %></div><% end %>

          <%= case @step do %>
            <% 0 -> %>
              <div class="launch-grid">
                <%= for app <- @apps, entrypoint <- app["entrypoints"] || [] do %>
                  <button class="launch-app" phx-click="choose" phx-target={@myself}
                    phx-value-app={app["id"]} phx-value-entrypoint={entrypoint["id"]}>
                    <div class="ic"><%= length(app["entrypoints"] || []) %></div>
                    <div class="meta">
                      <div class="nm"><%= app["name"] %></div>
                      <div class="tg"><%= entrypoint["title"] || entrypoint["id"] %></div>
                      <div class="row" style="gap:6px;margin-top:8px">
                        <span class="pill"><%= entrypoint["pipeline"] || entrypoint["id"] %></span>
                        <span class="pill"><%= MitosWeb.Format.humanize(workspace_kind(entrypoint)) %></span>
                      </div>
                    </div>
                  </button>
                <% end %>
                <%= if @apps == [] do %>
                  <div class="empty">No applications installed. Add one from the Applications page.</div>
                <% end %>
              </div>

            <% 1 -> %>
              <div class="field">
                <span class="label">Application</span>
                <div class="readonly"><%= app_name(@apps, @app_id) %> · <%= entrypoint_title(@apps, @app_id, @entrypoint_id) %></div>
              </div>

              <% entrypoint = find_entrypoint(@apps, @app_id, @entrypoint_id) %>
              <%= if entrypoint && entrypoint["description"] do %>
                <p class="lead" style="color: var(--mt-text-dim); font-size: 13px; margin: 0 0 16px;"><%= entrypoint["description"] %></p>
              <% end %>

              <%= if workspace_kind(entrypoint) != "none" do %>
                <div class="field">
                  <label>
                    <span class="label">
                      Workspace<%= if workspace_kind(entrypoint) == "dataset", do: " / dataset", else: " repository" %>
                    </span>
                    <input class="input" phx-keyup="input-changed" phx-target={@myself}
                      name="workspace_ref" value={@workspace_ref}
                      placeholder="git URL or local path (resolved server-side)" />
                  </label>
                  <div class="hint">Cloned and checked out by the backend — no git login needed here.</div>
                </div>
              <% end %>

              <%= if input_fields(entrypoint) != [] do %>
                <div class="sub2">Inputs</div>
                <%= for {name, schema} <- input_fields(entrypoint) do %>
                  <div class="field">
                    <label>
                      <span class="label"><%= name %><%= if to_string(name) in required_inputs(entrypoint), do: " *" %></span>
                      <.typed_field kind={field_kind(schema["type"])} name={"input_" <> name}
                        value={Map.get(@input_values, name, "")} placeholder={schema["description"]} myself={@myself} />
                    </label>
                  </div>
                <% end %>
              <% end %>

              <%= if entrypoint && (entrypoint["parameters"] || []) != [] do %>
                <div class="sub2">Parameters</div>
                <%= for p <- entrypoint["parameters"] do %>
                  <div class="field">
                    <label>
                      <span class="label"><%= p["name"] %></span>
                      <.typed_field kind={field_kind(p["type"])} name={"param_" <> p["name"]}
                        value={Map.get(@parameter_values, p["name"], "")} placeholder={p["description"]} myself={@myself} />
                    </label>
                    <%= if p["description"] do %><div class="hint"><%= p["description"] %></div><% end %>
                  </div>
                <% end %>
              <% end %>

            <% _ -> %>
              <% app = Enum.find(@apps, &(&1["id"] == @app_id)) %>
              <% entrypoint = find_entrypoint(@apps, @app_id, @entrypoint_id) %>
              <div class="rv-card">
                <div>
                  <div class="rv-name"><%= app && app["name"] %></div>
                  <div class="rv-sub"><%= entrypoint && (entrypoint["title"] || entrypoint["id"]) %></div>
                </div>
              </div>
              <div class="rv-rows">
                <div class="rv-row"><span>Pipeline</span><b><%= entrypoint && entrypoint["pipeline"] %></b></div>
                <div class="rv-row"><span>Workspace</span><b><%= workspace_kind(entrypoint) %></b></div>
                <%= if app && app["requirements"] && app["requirements"]["harnesses"] do %>
                  <div class="rv-row">
                    <span>Harnesses</span>
                    <b class="row" style="gap:6px">
                      <%= for h <- app["requirements"]["harnesses"] do %>
                        <MitosWeb.Components.harness_badge id={h} />
                      <% end %>
                    </b>
                  </div>
                <% end %>
                <%= if app && app["io"] && app["io"]["inputs"] do %>
                  <div class="rv-row">
                    <span>Inputs</span>
                    <b class="row" style="gap:6px">
                      <%= for b <- app["io"]["inputs"] do %>
                        <MitosWeb.Components.connector_chip id={b["connector"] || "manual"} size={20} />
                      <% end %>
                    </b>
                  </div>
                <% end %>
                <%= if app && app["io"] && app["io"]["outputs"] do %>
                  <div class="rv-row">
                    <span>Outputs</span>
                    <b class="row" style="gap:6px">
                      <%= for b <- app["io"]["outputs"] do %>
                        <span class="kv"><%= b["sink"] || "web" %></span>
                      <% end %>
                    </b>
                  </div>
                <% end %>
              </div>
          <% end %>
        </div>

        <div class="foot">
          <%= if @step > 0 do %>
            <button class="btn ghost" phx-click="back" phx-target={@myself}>Back</button>
          <% end %>
          <button class="btn ghost" phx-click="close-launch">Cancel</button>
          <%= if @step < 2 do %>
            <button class="btn primary" phx-click="next" phx-target={@myself}
              disabled={not can_next?(@step, find_entrypoint(@apps, @app_id, @entrypoint_id), @input_values)}>
              Continue
            </button>
          <% else %>
            <button class="btn primary" phx-click="launch" phx-target={@myself} disabled={@launching}>
              <%= if @launching, do: "Running…", else: "Run agent" %>
            </button>
          <% end %>
        </div>
      </div>
    </div>
    """
  end

  @impl true
  def handle_event("choose", %{"app" => app_id, "entrypoint" => entrypoint_id}, socket) do
    entrypoint = find_entrypoint(socket.assigns.apps, app_id, entrypoint_id)

    socket =
      socket
      |> assign(
        app_id: app_id,
        entrypoint_id: entrypoint_id,
        workspace_ref: "",
        input_values: default_input_values(entrypoint),
        parameter_values: default_parameter_values(entrypoint),
        step: 1
      )

    {:noreply, socket}
  end

  def handle_event("input-changed", params, socket) do
    socket =
      Enum.reduce(params, socket, fn {key, value}, acc ->
        cond do
          String.starts_with?(key, "input_") ->
            update(acc, :input_values, &Map.put(&1, String.slice(key, 6..-1//1), value))

          String.starts_with?(key, "param_") ->
            update(acc, :parameter_values, &Map.put(&1, String.slice(key, 6..-1//1), value))

          key == "workspace_ref" ->
            assign(acc, workspace_ref: value)

          true ->
            acc
        end
      end)

    {:noreply, socket}
  end

  def handle_event("next", %{}, socket), do: {:noreply, update(socket, :step, &min(&1 + 1, 2))}
  def handle_event("back", %{}, socket), do: {:noreply, update(socket, :step, &max(&1 - 1, 0))}

  def handle_event("launch", %{}, socket) do
    %{app_id: app_id, entrypoint_id: entrypoint_id, apps: apps} = socket.assigns
    entrypoint = find_entrypoint(apps, app_id, entrypoint_id)

    inputs = cast_inputs(entrypoint, socket.assigns.input_values)
    parameters = cast_parameters(entrypoint, socket.assigns.parameter_values)
    workspace = workspace_payload(entrypoint, socket.assigns.workspace_ref)

    case Console.run_entrypoint(app_id, entrypoint_id, inputs,
           pool: "interactive",
           source: "connector:web",
           connector: "web",
           parameters: parameters,
           workspace: workspace
         ) do
      {:ok, result} ->
        run_id = result["app_run_id"] || result["run_id"]
        send(self(), {:launch_launched, run_id})
        {:noreply, assign(socket, launching: false, error: nil)}

      {:error, reason} ->
        {:noreply, assign(socket, launching: false, error: format_error(reason))}
    end
  end

  # ----- helpers (ported from LaunchWizard.svelte) -----

  def steps, do: @steps

  def app_name(apps, app_id) do
    case Enum.find(apps, &(&1["id"] == app_id)) do
      nil -> ""
      app -> app["name"]
    end
  end

  def entrypoint_title(apps, app_id, entrypoint_id) do
    case find_entrypoint(apps, app_id, entrypoint_id) do
      nil -> ""
      ep -> ep["title"] || ep["id"]
    end
  end

  def find_entrypoint(apps, app_id, entrypoint_id) do
    app = Enum.find(apps, &(&1["id"] == app_id))
    ep = Enum.find(app && app["entrypoints"] || [], &(&1["id"] == entrypoint_id))
    ep || (app && app["entrypoints"] && List.first(app["entrypoints"]))
  end

  def default_input_values(entrypoint) do
    Enum.into(input_fields(entrypoint), %{}, fn {name, _schema} -> {name, ""} end)
  end

  def default_parameter_values(entrypoint) do
    Enum.into(entrypoint["parameters"] || [], %{}, fn p ->
      {p["name"], param_default(p)}
    end)
  end

  def input_fields(entrypoint) do
    schema = entrypoint && (get_in(entrypoint, ["inputs", "schema"]) || entrypoint["inputs"]) || %{}

    props =
      cond do
        is_map(schema) and is_map(schema["properties"]) -> schema["properties"]
        is_map(schema) and is_map(schema["schema"]) -> schema["schema"]
        is_map(schema) and not Map.has_key?(schema, "type") and not Map.has_key?(schema, "required") ->
          schema

        true ->
          %{}
      end

    props
    |> Map.drop(["type", "required", "additionalProperties", "description", "title"])
    |> Enum.to_list()
  end

  def required_inputs(entrypoint) do
    schema = entrypoint && (get_in(entrypoint, ["inputs", "schema"]) || entrypoint["inputs"]) || %{}
    (schema["required"] || []) |> Enum.map(&to_string/1)
  end

  def param_default(%{"default" => v}) when is_boolean(v), do: if(v, do: "true", else: "false")
  def param_default(%{"default" => v}) when v != nil, do: to_string(v)
  def param_default(_), do: ""

  def field_kind(type) do
    cond do
      type in ["integer", "int", "number"] -> :number
      type in ["boolean", "bool"] -> :boolean
      true -> :text
    end
  end

  def cast_inputs(entrypoint, values) do
    Enum.reduce(input_fields(entrypoint), %{}, fn {name, schema}, acc ->
      case cast(Map.get(values, name, ""), schema["type"]) do
        nil -> acc
        v -> Map.put(acc, name, v)
      end
    end)
  end

  def cast_parameters(entrypoint, values) do
    Enum.reduce(entrypoint["parameters"] || [], %{}, fn p, acc ->
      case cast(Map.get(values, p["name"], ""), p["type"]) do
        nil -> acc
        v -> Map.put(acc, p["name"], v)
      end
    end)
  end

  def cast(raw, type) do
    case field_kind(type) do
      :number ->
        case String.trim(raw) do
          "" -> nil
          s ->
            case Float.parse(s) do
              {n, _} -> n
              :error -> nil
            end
        end

      :boolean ->
        raw in ["true", "1"]

      :text ->
        raw
    end
  end

  def workspace_kind(entrypoint) do
    case entrypoint && entrypoint["workspace"] do
      nil -> "none"
      kind when is_binary(kind) -> kind
      %{"kind" => kind} -> kind || "none"
      _ -> "none"
    end
  end

  def workspace_payload(entrypoint, ref) do
    kind = workspace_kind(entrypoint)

    cond do
      kind == "none" -> nil
      String.trim(ref) == "" -> %{"kind" => kind}
      true -> %{"kind" => kind, "ref" => String.trim(ref)}
    end
  end

  def can_next?(step, entrypoint, values) do
    cond do
      step == 0 -> false
      step == 1 ->
        Enum.all?(required_inputs(entrypoint), &(String.trim(Map.get(values, &1, "")) != ""))
      true -> true
    end
  end

  def format_error(reason) when is_exception(reason), do: Exception.message(reason)
  def format_error(reason), do: inspect(reason)

  # ----- typed input field (function component) -----

  attr(:kind, :atom, default: :text)
  attr(:name, :string, required: true)
  attr(:value, :string, default: "")
  attr(:placeholder, :string, default: nil)
  attr(:myself, :any, default: nil)

  def typed_field(assigns) do
    ~H"""
    <%= case @kind do %>
      <% :number -> %>
        <input type="number" class="input" phx-keyup="input-changed" phx-target={@myself}
          name={@name} value={@value} placeholder={@placeholder} />
      <% :boolean -> %>
        <select class="input" phx-change="input-changed" phx-target={@myself} name={@name}>
          <option value="false" selected={@value != "true"}>false</option>
          <option value="true" selected={@value == "true"}>true</option>
        </select>
      <% _ -> %>
        <input class="input" phx-keyup="input-changed" phx-target={@myself}
          name={@name} value={@value} placeholder={@placeholder} />
    <% end %>
    """
  end
end
