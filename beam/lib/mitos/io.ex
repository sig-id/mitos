defmodule Mitos.IO do
  @moduledoc """
  The I/O plane (10) — connectors bring the world in; sinks send results back
  out; the web app is just one connector + one sink (10 §6).

  This module owns:

    * the **bindings registry** (10 §4) — per-tenant `(org, project)` wirings of
      connectors (ingress) and sinks (egress) around a pipeline, declared *around*
      an application so the same recipe can be triggered by webhook today and by
      chat tomorrow without editing it;
    * **ingress** (`ingest/3`) — connector → normalize → **idempotent dedup** →
      pipeline selection (10 §3.3, `explicit` default) → Task → run, emitting
      `signal_received` + `task_enqueued` Effects on the run log (10 §7);
    * **egress** (`deliver_for/1`) — the idempotent delivery driver: a terminal
      Effect on the log fires every matched sink; `render` folds the log +
      artifacts; `deliver` performs the crossing; each attempt/sent/failed is an
      Effect, at-least-once with idempotency keyed on `(run_id, sink, dest)`
      (10 §5.3).

  Invariants (01 §4) hold by construction: I/O adds **no parallel ledger** —
  ingress and egress are folds/Effects on the same run log as everything else,
  so they fold into the same billing/audit/proof reads (08 §4). Credentials live
  only in the control-plane vault (`01 §7`), never in the sandbox.
  """

  use GenServer

  defmodule Binding do
    @moduledoc """
    One I/O binding (10 §4): a connector (input) or sink (output) wired around a
    pipeline, per-tenant scoped (`06 §12`, `08 §3.1`). Bindings live at the
    application/service level, never inside a capability (10 §3.4).
    """

    @enforce_keys [:tenant, :name, :direction, :module]
    defstruct tenant: nil,
              # binding name (unique within (tenant, direction))
              name: nil,
              # :input | :output
              direction: :input,
              # :webhook | :manual | :chat | :notify | :deliver | :reply | :web
              kind: nil,
              # the Connector/Sink implementation module
              module: nil,
              # connector/sink config (dest, url, vault cred refs, …)
              config: %{},
              # outputs: the Effect type that triggers delivery (:capability_finished | :run_finished | …)
              on: nil,
              # inputs: %{mode: :explicit, pipeline: markdown, capabilities: %{name => dir}, app: …}
              select: nil,
              # outputs: redaction cut (:commercial | :dev) — commercial folds Effects only
              cut: :commercial

    @type t :: %__MODULE__{}
  end

  # --- GenServer: bindings registry + ingress dedup ---

  def start_link(_opts \\ []), do: GenServer.start_link(__MODULE__, %{}, name: __MODULE__)

  @impl true
  def init(_) do
    {:ok, %{bindings: %{}, seen: MapSet.new()}}
  end

  # Name → module registries for the connector/sink classes mitos ships (10 §3.4:
  # generic ingress/egress are platform-provided). The manifest `io` block names
  # these by string; an app-shipped usage-specific connector would register here.
  @connectors %{
    "webhook" => Mitos.Connector.Webhook,
    "manual" => Mitos.Connector.Manual,
    "web" => Mitos.Connector.Manual,
    # 15 §5.2 — the return half of the decision round trip. Unlike every other
    # connector it produces no Task: it applies a resolution to a parked run.
    "decision" => Mitos.Connector.Decision
  }
  @sinks %{
    "notify" => Mitos.Sink.Notify,
    "deliver" => Mitos.Sink.Deliver,
    "reply" => Mitos.Sink.Reply,
    "web" => Mitos.Sink.Web,
    # 15 §5.1 — a decision card is a payload, not a new plane (10 §5.3a).
    "decide" => Mitos.Sink.Decide
  }

  def connector_module(name), do: @connectors[to_string(name)]
  def sink_module(name), do: @sinks[to_string(name)]
  def connector_classes, do: Map.keys(@connectors)
  def sink_classes, do: Map.keys(@sinks)

  @doc "`on:` label from a manifest (10 §4) → the Effect atom a sink matches on."
  def normalize_on(label) when is_atom(label), do: label

  def normalize_on(label) when is_binary(label) do
    case label do
      "capability_finished" -> :capability_finished
      "run_finished" -> :run_finished
      "run.done" -> :run_finished
      "run:done" -> :run_finished
      other -> String.to_atom(other)
    end
  end

  @doc "Register an I/O binding (idempotent per (tenant, direction, name))."
  def register(%Binding{} = b), do: GenServer.call(__MODULE__, {:register, b})

  def bindings, do: GenServer.call(__MODULE__, :bindings)

  @doc "Bindings for a tenant (matched on org)."
  def bindings(tenant), do: GenServer.call(__MODULE__, {:bindings, tenant})

  @doc "Is there at least one output binding for this tenant? (drives delivery scheduling)"
  def any_output_bindings?(tenant), do: GenServer.call(__MODULE__, {:any_output?, tenant})

  def clear, do: GenServer.call(__MODULE__, :clear)

  @impl true
  def handle_call({:register, b}, _from, st) do
    key = {tenant_org(b.tenant), b.direction, b.name}
    {:reply, :ok, put_in(st, [:bindings, key], b)}
  end

  def handle_call(:bindings, _from, st), do: {:reply, Map.values(st.bindings), st}

  def handle_call({:bindings, tenant}, _from, st) do
    org = tenant_org(tenant)
    matches = Enum.filter(Map.values(st.bindings), fn b -> tenant_org(b.tenant) == org end)
    {:reply, matches, st}
  end

  def handle_call({:any_output?, tenant}, _from, st) do
    org = tenant_org(tenant)
    any = Enum.any?(st.bindings, fn {{o, dir, _}, _} -> o == org and dir == :output end)
    {:reply, any, st}
  end

  def handle_call(:clear, _from, st), do: {:reply, :ok, %{st | bindings: %{}, seen: MapSet.new()}}

  def handle_call({:seen?, key}, _from, st) do
    if MapSet.member?(st.seen, key) do
      {:reply, true, st}
    else
      {:reply, false, %{st | seen: MapSet.put(st.seen, key)}}
    end
  end

  def handle_call({:lookup, org, direction, name}, _from, st),
    do: {:reply, st.bindings[{org, direction, name}], st}

  # --- ingress (10 §3): connector → normalize → dedup → select → Task → run ---

  @doc """
  Ingest an inbound delivery through the named input connector. Dedups on the
  intake's idempotency key (re-delivery MUST NOT create a duplicate Task, 10
  §3.2), selects the pipeline (`explicit` mode, 10 §3.3), enqueues a Task, and
  starts the run with the ingress carried on its config (so `signal_received`
  + `task_enqueued` land on its log).

  Returns `{:ok, run_id}` on first delivery, or `{:ok, :duplicate}` on a
  re-delivery (the caller acks accordingly).
  """
  def ingest(tenant, connector_name, raw) do
    org = tenant_org(tenant)

    with %{direction: :input, module: mod, config: config, select: select} <-
           lookup_input(org, connector_name),
         {:ok, handle} <- mount(mod, config),
         {:ok, signals} <- mod.receive(handle, raw),
         {:ok, run_id} <- ingest_signals(tenant, connector_name, mod, handle, signals, select) do
      {:ok, run_id}
    else
      nil -> {:error, :no_binding}
      {:error, _} = e -> e
    end
  end

  defp ingest_signals(tenant, connector, mod, handle, signals, select) do
    results = Enum.reduce_while(signals, [], fn signal, acc ->
      with {:ok, intake} <- mod.normalize(signal),
           key = {tenant_org(tenant), connector, intake.idempotency_key},
           :ok <- dedup(key),
           {:ok, run_id} <- start_from_intake(tenant, connector, select, intake) do
        mod.ack(handle, signal, {:ok, run_id})
        {:cont, [run_id | acc]}
      else
        {:dup, _} ->
          mod.ack(handle, signal, :duplicate)
          {:cont, acc}

        {:error, reason} = e ->
          mod.ack(handle, signal, {:error, reason})
          {:halt, e}
      end
    end)

    case results do
      {:error, _} = e -> e
      [] -> {:ok, :duplicate}
      [run_id | _] -> {:ok, run_id}
    end
  end

  # Dedup against the in-process seen set (10 §6.3 "dedup on enqueue"). A hosted
  # deployment persists this; the MVP dedups in-memory — the run's own
  # signal_received Effect is the durable record that a re-drive can also check.
  defp dedup(key) do
    case GenServer.call(__MODULE__, {:seen?, key}) do
      true -> {:dup, key}
      false -> :ok
    end
  end

  defp start_from_intake(tenant, connector, select, intake) do
    task =
      Mitos.WorkPool.enqueue(
        Mitos.Task.new(
          tenant: tenant,
          source: "connector:#{connector}",
          pool: "io",
          pipeline: select[:pipeline] && select[:pipeline].name,
          payload: %{
            "intent" => intake.intent,
            "inputs" => intake.inputs,
            "workspace" => intake.workspace,
            "reply_ref" => intake.reply_ref
          }
        )
      )

    Mitos.Run.start_run(
      tenant: tenant,
      task_id: task.id,
      pipeline: pipeline_body(select),
      capabilities: select[:capabilities] || %{},
      workspace: intake.workspace,
      inputs: intake.inputs,
      ingress: %{connector: connector, intake: intake},
      trust_tier: select[:trust_tier]
    )
  end

  defp pipeline_body(%{pipeline: %{body: body}}), do: body
  defp pipeline_body(%{pipeline: %{markdown: md}}), do: md
  defp pipeline_body(%{pipeline: pipeline}) when is_binary(pipeline), do: pipeline
  defp pipeline_body(_), do: ""

  defp mount(mod, config) do
    if function_exported?(mod, :mount, 1) do
      mod.mount(config)
    else
      {:ok, nil}
    end
  end

  # --- egress (10 §5): terminal Effect → matched sinks → delivery_* Effects ---

  @doc """
  The idempotent egress driver for a run. Folds the log, and for every output
  binding whose triggering Effect is present and whose sink claims it, renders
  the (redacted) payload, delivers it, and appends a `delivery_attempted` then
  `delivery_sent`/`delivery_failed` Effect. A `delivery_sent` keyed on
  `(sink, dest)` suppresses re-delivery (10 §5.3), so this is safe to call on
  finalize, on resume, or from the CLI.

  Honors single-writer-per-run (01 §4): if the run process is alive it routes
  appends through it; otherwise it briefly re-opens the log writer (the run has
  released it) and closes it when done.
  """
  def deliver_for(run_id) do
    # Serialize per-run: the finalize-triggered driver and an explicit re-drive
    # (CLI / API) can otherwise race past the idempotency read. Single-writer on
    # the log is the invariant; the lock makes concurrent drivers agree.
    :global.trans({__MODULE__, {:deliver, run_id}}, fn -> do_deliver_for(run_id) end)
  end

  defp do_deliver_for(run_id) do
    with {:ok, config} <- Mitos.Run.Config.load(run_id) do
      state = Mitos.Run.State.fold(run_id)
      charge = charge_for(run_id)
      reply_ref = reply_ref_of(config.ingress)
      terminal = terminal_seq(state)
      delivered = delivered_keys(state)
      bindings = output_bindings(config.tenant)

      own_writer? = not run_alive?(run_id)

      if own_writer?, do: open_owned_writer(run_id)

      try do
        results =
          Enum.flat_map(bindings, fn b ->
            deliver_one(run_id, b, state, charge, reply_ref, delivered, terminal)
          end)

        {:ok, results}
      after
        if own_writer?, do: close_owned_writer()
      end
    else
      _ -> {:error, :no_run}
    end
  end

  defp deliver_one(run_id, %Binding{module: mod, config: cfg, kind: kind, on: on} = b,
        state, charge, reply_ref, delivered, terminal) do
    trigger = Enum.find(state.effects, &(&1.type == on))

    cond do
      is_nil(trigger) ->
        []

      not sink_match?(mod, trigger) ->
        []

      true ->
        dest = dest_of(b, reply_ref)

        if {to_string(kind), dest} in delivered do
          # at-least-once idempotency: already delivered to this (sink, dest)
          []
        else
          run_delivery(run_id, b, mod, cfg, state, charge, reply_ref, dest, terminal)
        end
    end
  end

  defp run_delivery(run_id, b, mod, cfg, state, charge, reply_ref, dest, terminal) do
    ctx = %{run_id: run_id, state: state, charge: charge, cut: b.cut, reply_ref: reply_ref}

    append_delivery(run_id,
      type: :delivery_attempted,
      caused_by: terminal,
      ref: %{"sink" => b.kind, "dest" => dest},
      payload: %{"cut" => b.cut}
    )

    handle = if function_exported?(mod, :bind, 1), do: ok!(mod.bind(cfg)), else: nil

    with {:ok, payload} <- mod.render(ctx, cfg),
         {:ok, receipt} <- mod.deliver(handle, payload) do
      append_delivery(run_id,
        type: :delivery_sent,
        caused_by: terminal,
        ref: %{"sink" => b.kind, "dest" => dest, "receipt" => receipt},
        payload: %{}
      )

      [{:sent, b.kind, dest, receipt}]
    else
      {:error, reason} ->
        append_delivery(run_id,
          type: :delivery_failed,
          caused_by: terminal,
          ref: %{"sink" => b.kind, "dest" => dest},
          payload: %{"reason" => inspect(reason)}
        )

        [{:failed, b.kind, dest, reason}]
    end
  end

  # --- delivery read model (a pure fold over delivery_* Effects) ---

  @doc "The deliveries for a run: `%{sent: [...], failed: [...]}`, a fold of Effects."
  def deliveries(run_id) do
    run_id
    |> Mitos.Log.read()
    |> Enum.filter(&match?(%Mitos.Event.Effect{type: t} when t in [:delivery_sent, :delivery_failed], &1))
    |> Enum.group_by(& &1.type)
    |> then(fn m ->
      %{
        sent: Map.get(m, :delivery_sent, []) |> Enum.map(&delivery_view/1),
        failed: Map.get(m, :delivery_failed, []) |> Enum.map(&delivery_view/1)
      }
    end)
  end

  defp delivery_view(%Mitos.Event.Effect{ref: ref, payload: p, ts: ts}),
    do: %{"sink" => ref["sink"], "dest" => ref["dest"], "receipt" => ref["receipt"], "reason" => p["reason"], "ts" => ts}

  # --- helpers ---

  defp lookup_input(org, name) do
    GenServer.call(__MODULE__, {:lookup, org, :input, name})
  end

  defp output_bindings(tenant) do
    GenServer.call(__MODULE__, {:bindings, tenant})
    |> Enum.filter(&(&1.direction == :output))
  end

  defp sink_match?(mod, effect), do: mod.match(effect)

  defp dest_of(%Binding{kind: :deliver, config: cfg}, _), do: cfg[:url] || cfg["url"] || "deliver"
  defp dest_of(%Binding{kind: :notify, config: cfg}, _), do: cfg[:to] || cfg["to"] || "notify"
  defp dest_of(%Binding{kind: :reply}, reply_ref), do: reply_ref || "reply"
  defp dest_of(%Binding{kind: :web}, _), do: "web"
  defp dest_of(%Binding{config: cfg}, _), do: cfg[:dest] || cfg["dest"] || "default"

  defp delivered_keys(state) do
    state.effects
    |> Enum.filter(&(&1.type == :delivery_sent))
    |> Enum.map(&{&1.ref["sink"], &1.ref["dest"]})
    |> MapSet.new()
  end

  defp terminal_seq(state) do
    state.effects
    |> Enum.filter(&(&1.type == :run_finished))
    |> Enum.map(& &1.seq)
    |> Enum.max(fn -> nil end)
  end

  defp charge_for(run_id) do
    case Mitos.Facilitator.settlement(run_id) do
      %{charge: charge} -> charge
      _ -> nil
    end
  end

  # Ingress config round-trips through config.json, so keys may be atoms (live
  # process) or strings (loaded from disk). Read either.
  defp reply_ref_of(nil), do: nil

  defp reply_ref_of(ingress) do
    intake = ingress[:intake] || ingress["intake"] || %{}
    intake[:reply_ref] || intake["reply_ref"]
  end

  defp run_alive?(run_id) do
    case Registry.lookup(Mitos.Run.Registry, run_id) do
      [] -> false
      _ -> true
    end
  end

  # Owned-writer appends (run process gone). Threaded via the process dictionary
  # so deliver_for stays linear; scoped to this one delivery pass.
  defp open_owned_writer(run_id) do
    {:ok, writer, _} = Mitos.Log.open_writer(run_id)
    Process.put(:mitos_io_writer, writer)
  end

  defp close_owned_writer do
    case Process.get(:mitos_io_writer) do
      nil -> :ok
      writer ->
        Mitos.Log.close(writer)
        Process.delete(:mitos_io_writer)
    end
  end

  defp append_delivery(run_id, fields) do
    case Process.get(:mitos_io_writer) do
      nil ->
        # run is live → route to its single writer
        Mitos.Run.append_effect(run_id, fields)

      writer ->
        {_e, w2} = Mitos.Log.append(writer, Mitos.Event.Effect.new(fields))
        Process.put(:mitos_io_writer, w2)
        :ok
    end
  end

  defp ok!({:ok, v}), do: v
  defp ok!({:error, reason}), do: raise("bind failed: #{inspect(reason)}")

  defp tenant_org(%{"org" => org}), do: org
  defp tenant_org(%{org: org}), do: org
  defp tenant_org(org) when is_binary(org), do: org
  defp tenant_org(_), do: "local"
end
