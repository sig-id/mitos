defmodule MitosWeb.ConsoleLive.LaunchComponent do
  @moduledoc """
  Modal wizard that runs one application entrypoint.

  Two steps: pick an **application** (one card per app, not per entrypoint),
  then **configure** it — choose an entrypoint, fill the *required* inputs, pick
  a workspace path, and optionally expand "Advanced" for parameters. Launch is
  fire-and-forget: we pre-generate the run id, close the modal immediately, and
  deep-link to the live run page; the run itself executes under
  `Mitos.ConsoleTaskSupervisor`.
  """

  use MitosWeb, :live_component

  alias MitosWeb.Console

  @steps ["Application", "Configure"]

  @impl true
  def update(assigns, socket) do
    app_id = assigns[:app]

    socket =
      socket
      |> assign(assigns)
      |> assign_new(:step, fn -> if app_id in ["", nil], do: 0, else: 1 end)
      |> assign_new(:error, fn -> nil end)
      |> assign_new(:launching, fn -> false end)
      |> assign_new(:advanced, fn -> false end)
      |> assign_new(:workspace_ref, fn -> Console.workspace_default() end)
      |> assign_new(:input_values, fn -> %{} end)
      |> assign_new(:parameter_values, fn -> %{} end)
      |> assign(
        app_id: app_id,
        entrypoint_id: assigns[:entrypoint],
        workspaces: Console.workspace_candidates()
      )

    # when a specific app is opened, default the entrypoint to its first
    socket =
      if socket.assigns[:entrypoint_id] in ["", nil] do
        case first_entrypoint(socket.assigns[:apps], socket.assigns[:app_id]) do
          nil -> socket
          ep -> prepare_for_entrypoint(socket, socket.assigns[:app_id], ep["id"])
        end
      else
        prepare_for_entrypoint(socket, socket.assigns[:app_id], socket.assigns[:entrypoint_id])
      end

    {:ok, socket}
  end

  defp prepare_for_entrypoint(socket, app_id, entrypoint_id) do
    ep = find_entrypoint(socket.assigns[:apps], app_id, entrypoint_id)

    socket
    |> assign(
      app_id: app_id,
      entrypoint_id: entrypoint_id,
      input_values: default_input_values(ep),
      parameter_values: default_parameter_values(ep)
    )
  end

  @impl true
  def render(assigns) do
    ~H"""
    <div class="backdrop" phx-click-away="close-launch" phx-window-keydown="close-launch" phx-key="escape">
      <div class="modal" style="--w: 640px">
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
                <%= for app <- @apps do %>
                  <button class="launch-app" phx-click="choose" phx-target={@myself} phx-value-app={app["id"]}>
                    <div class="ic"><%= length(app["entrypoints"] || []) %></div>
                    <div class="meta">
                      <div class="nm"><%= app["name"] %></div>
                      <div class="tg"><%= app["description"] || "#{length(app["entrypoints"] || [])} actions" %></div>
                    </div>
                  </button>
                <% end %>
                <%= if @apps == [] do %>
                  <div class="empty">No applications installed. Add one from the Applications page.</div>
                <% end %>
              </div>

            <% _ -> %>
              <% entrypoint = find_entrypoint(@apps, @app_id, @entrypoint_id) %>
              <% app = Enum.find(@apps, &(&1["id"] == @app_id)) %>

              <%= cond do %>
                <% app && (app["entrypoints"] || []) != [] and length(app["entrypoints"] || []) > 1 -> %>
                  <div class="field">
                    <span class="label">Action</span>
                    <select class="input" phx-change="select-entrypoint" phx-target={@myself}>
                      <%= for e <- app["entrypoints"] do %>
                        <option value={e["id"]} selected={e["id"] == @entrypoint_id}>
                          <%= e["title"] || e["id"] %>
                        </option>
                      <% end %>
                    </select>
                  </div>
                <% true -> %>
              <% end %>

              <%= if entrypoint && entrypoint["description"] do %>
                <p class="lead" style="color: var(--mt-text-dim); font-size: 13px; margin: 0 0 16px;"><%= entrypoint["description"] %></p>
              <% end %>

              <%= if input_fields(entrypoint) != [] do %>
                <%= for {name, schema} <- input_fields(entrypoint) do %>
                  <div class="field">
                    <label>
                      <span class="label"><%= friendly_input_name(name) %><%= if to_string(name) in required_inputs(entrypoint), do: " *" %></span>
                      <input class="input" phx-keyup="input-changed" phx-target={@myself}
                        name={"input_" <> name}
                        value={Map.get(@input_values, name, "")}
                        placeholder={input_placeholder(name, schema)} />
                    </label>
                    <%= if hint = input_hint(name, schema) do %><div class="hint"><%= hint %></div><% end %>
                  </div>
                <% end %>
              <% end %>

              <%= if workspace_kind(entrypoint) != "none" do %>
                <div class="field">
                  <label>
                    <span class="label">Workspace</span>
                    <input class="input" list="ws-candidates" phx-keyup="input-changed" phx-target={@myself}
                      name="workspace_ref" value={@workspace_ref} />
                    <datalist id="ws-candidates">
                      <%= for p <- @workspaces do %><option value={p}></option><% end %>
                    </datalist>
                  </label>
                  <div class="hint">The repository to work in. Defaults to this project — pick a sibling or type any local path. Resolved on the server; no upload.</div>
                </div>
              <% end %>

              <%= if entrypoint && (entrypoint["parameters"] || []) != [] do %>
                <button class={"adv-toggle #{if @advanced, do: "on"}"} phx-click="toggle-advanced" phx-target={@myself}>
                  <%= if @advanced, do: "▾ Hide advanced", else: "▸ Advanced options" %>
                </button>
                <%= if @advanced do %>
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
              <% end %>
          <% end %>
        </div>

        <div class="foot">
          <%= if @step > 0 do %>
            <button class="btn ghost" phx-click="back" phx-target={@myself}>Back</button>
          <% end %>
          <button class="btn ghost" phx-click="close-launch">Cancel</button>
          <%= if @step == 0 do %>
            <span class="grow"></span>
          <% else %>
            <button class="btn primary" phx-click="launch" phx-target={@myself} disabled={@launching or not can_launch?(find_entrypoint(@apps, @app_id, @entrypoint_id), @input_values)}>
              <%= if @launching, do: "Starting…", else: "Run agent" %>
            </button>
          <% end %>
        </div>
      </div>
    </div>
    """
  end

  # ----- events -----

  @impl true
  def handle_event("choose", %{"app" => app_id}, socket) do
    case first_entrypoint(socket.assigns.apps, app_id) do
      nil ->
        {:noreply, assign(socket, error: "This application has no entrypoints.")}

      ep ->
        {:noreply,
         socket
         |> prepare_for_entrypoint(app_id, ep["id"])
         |> assign(step: 1, error: nil)}
    end
  end

  def handle_event("select-entrypoint", %{"value" => entrypoint_id}, socket) do
    {:noreply, prepare_for_entrypoint(socket, socket.assigns.app_id, entrypoint_id)}
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

  def handle_event("toggle-advanced", %{}, socket), do: {:noreply, update(socket, :advanced, &(!&1))}
  def handle_event("back", %{}, socket), do: {:noreply, update(socket, :step, &max(&1 - 1, 0))}

  def handle_event("launch", %{}, socket) do
    %{app_id: app_id, entrypoint_id: entrypoint_id, apps: apps} = socket.assigns
    entrypoint = find_entrypoint(apps, app_id, entrypoint_id)

    inputs = cast_inputs(entrypoint, socket.assigns.input_values)
    parameters = cast_parameters(entrypoint, socket.assigns.parameter_values)
    workspace = workspace_payload(entrypoint, socket.assigns.workspace_ref)

    # `diff_command` is marked required by the schema but is conventionally
    # "just review my uncommitted changes" — fill the common default so the
    # user can leave it blank.
    inputs =
      if Map.has_key?(inputs_to_schema(entrypoint), "diff_command") and
           blank?(inputs["diff_command"]) do
        Map.put(inputs, "diff_command", "git diff")
      else
        inputs
      end

    # Pre-generate the run id so we can close the modal and navigate to the
    # live run page immediately. The synchronous AppRunner.run executes under
    # a supervised task; the run's event log fills in as the harness works.
    run_id = "app-" <> Base.url_encode64(:crypto.strong_rand_bytes(8), padding: false)
    parent = self()

    Task.Supervisor.start_child(Mitos.ConsoleTaskSupervisor, fn ->
      Console.run_entrypoint(app_id, entrypoint_id, inputs,
        pool: "interactive",
        source: "connector:web",
        connector: "web",
        parameters: parameters,
        workspace: workspace,
        run_id: run_id
      )

      # result intentionally ignored — fire and forget. The run page polls.
      :ok
    end)

    # Close + navigate now; don't wait for the (minutes-long) run.
    send(parent, {:launch_launched, run_id})
    {:noreply, assign(socket, launching: false, error: nil)}
  end

  # ----- helpers -----

  def steps, do: @steps

  def app_name(apps, app_id) do
    case Enum.find(apps, &(&1["id"] == app_id)) do
      nil -> ""
      app -> app["name"]
    end
  end

  def first_entrypoint(apps, app_id) do
    case Enum.find(apps, &(&1["id"] == app_id)) do
      nil -> nil
      app -> List.first(app["entrypoints"] || [])
    end
  end

  def find_entrypoint(apps, app_id, entrypoint_id) do
    app = Enum.find(apps, &(&1["id"] == app_id))
    ep = Enum.find(app && app["entrypoints"] || [], &(&1["id"] == entrypoint_id))
    ep || first_entrypoint(apps, app_id)
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

  # Friendly names + hints for the otherwise-opaque input keys.
  defp friendly_input_name("diff_command"), do: "Diff to review"
  defp friendly_input_name("objective"), do: "What to build"
  defp friendly_input_name("pr_number"), do: "PR number"
  defp friendly_input_name(name), do: name

  defp input_placeholder("diff_command", _), do: "git diff origin/main...HEAD"
  defp input_placeholder("objective", _), do: "e.g. add a dark-mode toggle to the console"
  defp input_placeholder(_name, %{"description" => d}), do: d
  defp input_placeholder(_, _), do: ""

  defp input_hint("diff_command", _),
    do: "A read-only shell command that prints what changed. Leave empty to review uncommitted changes (git diff)."

  defp input_hint("objective", _),
    do: "Describe the task in plain language — an issue, a feature, a bug. The planner turns this into work."

  defp input_hint(_name, %{"description" => d}) when is_binary(d), do: d
  defp input_hint(_, _), do: nil

  defp param_default(%{"default" => v}) when is_boolean(v), do: if(v, do: "true", else: "false")
  defp param_default(%{"default" => v}) when v != nil, do: to_string(v)
  defp param_default(_), do: ""

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
      ref == nil or String.trim(ref) == "" -> %{"kind" => kind}
      true -> %{"kind" => kind, "ref" => String.trim(ref)}
    end
  end

  defp blank?(nil), do: true
  defp blank?(s) when is_binary(s), do: String.trim(s) == ""
  defp blank?(_), do: false

  defp inputs_to_schema(entrypoint) do
    schema = entrypoint && (get_in(entrypoint, ["inputs", "schema"]) || entrypoint["inputs"]) || %{}

    props =
      cond do
        is_map(schema) and is_map(schema["properties"]) -> schema["properties"]
        is_map(schema) and is_map(schema["schema"]) -> schema["schema"]
        true -> schema
      end

    if is_map(props), do: props, else: %{}
  end

  def can_launch?(entrypoint, values) do
    Enum.all?(required_inputs(entrypoint), fn name ->
      # diff_command may be left blank (defaults to "git diff"); other required
      # inputs must be filled.
      if name == "diff_command" do
        true
      else
        String.trim(Map.get(values, name, "")) != ""
      end
    end)
  end

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
