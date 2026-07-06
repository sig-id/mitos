defmodule Mitos.IOTest do
  @moduledoc """
  The I/O plane (10): connectors bring the world in; sinks send results back out;
  the web app is just one connector + one sink. Ingress normalizes → dedups →
  selects → Task → run (signal_received + task_enqueued on the log). Egress folds
  the log, fires matched sinks, and records delivery_* Effects — at-least-once
  with idempotency keyed on (run_id, sink, dest).

  Invariants checked: I/O adds no parallel ledger (ingress/egress are Effects on
  the run log), redaction is at the data layer (a commercial sink render is an
  Effect-only fold — no rationale/harness), and the fold readers (Run.State,
  Proof) consume the delivery Effects (01 §4).
  """
  use ExUnit.Case, async: false

  alias Mitos.{Fixtures, IO, Run}
  alias Mitos.IO.Binding
  alias Mitos.Event.Effect

  @moduletag timeout: 120_000

  defp unique_org, do: "io_" <> Mitos.id()

  defp cap_dir, do: Fixtures.capability("worker")

  defp pipeline_md do
    """
    # Pipeline: io-flow

    Goal: fulfill one ingested job.

    ## Stages
    1. **worker** — run the capability against the ingested inputs.
    """
  end

  defp webhook_binding(org, cap) do
    %Binding{
      tenant: %{"org" => org},
      name: "webhook",
      direction: :input,
      kind: :webhook,
      module: Mitos.Connector.Webhook,
      config: %{},
      select: %{pipeline: %{name: "io-flow", body: pipeline_md()}, capabilities: %{"worker" => cap}}
    }
  end

  defp notify_binding(org, to \\ "ops-chat") do
    %Binding{
      tenant: %{"org" => org},
      name: "notify",
      direction: :output,
      kind: :notify,
      module: Mitos.Sink.Notify,
      config: %{to: to, subject: "io job done"},
      on: :capability_finished,
      cut: :commercial
    }
  end

  defp web_binding(org) do
    %Binding{
      tenant: %{"org" => org},
      name: "web",
      direction: :output,
      kind: :web,
      module: Mitos.Sink.Web,
      config: %{},
      on: :run_finished,
      cut: :commercial
    }
  end

  defp reply_binding(org) do
    %Binding{
      tenant: %{"org" => org},
      name: "reply",
      direction: :output,
      kind: :reply,
      module: Mitos.Sink.Reply,
      config: %{},
      on: :capability_finished,
      cut: :commercial
    }
  end

  defp ingest_webhook(org, body, id) do
    IO.ingest(%{"org" => org}, "webhook", %{body: body, headers: %{}, id: id})
  end

  defp effects(run_id), do: Mitos.Log.read(run_id) |> Enum.filter(&match?(%Effect{}, &1))

  # Ingest + await a finished run with only an input binding, so the finalize-time
  # delivery check finds no output binding (no async driver). Output bindings are
  # registered in the test *after* the run finishes, making deliver_for the sole,
  # deterministic delivery.
  defp ingest_and_finish(org, body, id) do
    cap = cap_dir()
    IO.register(webhook_binding(org, cap))
    {:ok, run_id} = ingest_webhook(org, body, id)
    state = Run.await(run_id)
    {state, run_id}
  end

  setup do
    on_exit(fn ->
      IO.clear()
      Mitos.Sink.Notify.Destination.clear()
      Mitos.Sink.Reply.Destination.clear()
    end)
  end

  # --- ingress (10 §3) ---

  test "a webhook connector normalizes an inbound POST into a run; signal_received + task_enqueued land on the log" do
    org = unique_org()
    IO.register(webhook_binding(org, cap_dir()))

    assert {:ok, run_id} =
             ingest_webhook(org, %{"intent" => "ship a widget", "inputs" => %{"sku" => "X1"}}, "deliv-1")

    state = Run.await(run_id)
    assert state.status == :finished

    types = Enum.map(effects(run_id), & &1.type)
    assert :signal_received in types
    assert :task_enqueued in types

    signal = Enum.find(effects(run_id), &(&1.type == :signal_received))
    assert signal.ref["connector"] == "webhook"
    assert signal.payload["idempotency_key"] == "deliv-1"
    assert signal.payload["intent"] == "ship a widget"

    task = Enum.find(effects(run_id), &(&1.type == :task_enqueued))
    assert task.ref["source"] == "connector:webhook"
    assert task.ref["task"] != nil
  end

  test "ingress is idempotent: re-delivery of the same signal does NOT create a duplicate run" do
    org = unique_org()
    IO.register(webhook_binding(org, cap_dir()))

    assert {:ok, run_a} = ingest_webhook(org, %{"inputs" => %{}}, "dup-key")
    # A genuinely new delivery id ⇒ a new run.
    assert {:ok, run_b} = ingest_webhook(org, %{"inputs" => %{}}, "fresh-key")
    refute run_a == run_b

    # Re-delivering the first signal ⇒ :duplicate, no new run.
    assert {:ok, :duplicate} = ingest_webhook(org, %{"inputs" => %{}}, "dup-key")

    # Exactly two runs for this org; exactly one signal_received on run_a.
    runs = Enum.filter(Mitos.Runs.list(), &String.starts_with?(&1.run_id, org <> "/"))
    assert length(runs) == 2
    assert Enum.count(effects(run_a), &(&1.type == :signal_received)) == 1
  end

  # --- egress (10 §5) ---

  test "a notify sink delivers after the run and records delivery_sent on the log" do
    org = unique_org()
    {state, run_id} = ingest_and_finish(org, %{"inputs" => %{}}, "n-1")
    assert state.status == :finished

    IO.register(notify_binding(org, "ops-chat"))

    assert {:ok, [{:sent, :notify, "ops-chat", _}]} = IO.deliver_for(run_id)

    types = Enum.map(effects(run_id), & &1.type)
    assert :delivery_attempted in types
    assert :delivery_sent in types

    sent = Enum.find(effects(run_id), &(&1.type == :delivery_sent))
    assert sent.ref["sink"] == "notify"
    assert sent.ref["dest"] == "ops-chat"

    # The notify transport received the message.
    [msg] = Mitos.Sink.Notify.Destination.collect("ops-chat")
    assert msg.body =~ "io job done"
  end

  test "egress is idempotent: re-driving deliver_for does not re-deliver to (sink, dest)" do
    org = unique_org()
    ingest_and_finish(org, %{"inputs" => %{}}, "i-1")
    run_id = hd(for r <- Mitos.Runs.list(), String.starts_with?(r.run_id, org <> "/"), do: r.run_id)

    IO.register(notify_binding(org, "notify-default"))

    assert {:ok, [{:sent, :notify, _, _}]} = IO.deliver_for(run_id)
    # Second drive: nothing left to deliver.
    assert {:ok, []} = IO.deliver_for(run_id)

    assert Enum.count(effects(run_id), &(&1.type == :delivery_sent)) == 1
    assert length(Mitos.Sink.Notify.Destination.collect("notify-default")) == 1
  end

  test "a commercial sink render is an Effect-only fold — no recipe, no harness, no rationale" do
    org = unique_org()
    ingest_and_finish(org, %{"inputs" => %{}}, "r-1")
    run_id = hd(for r <- Mitos.Runs.list(), String.starts_with?(r.run_id, org <> "/"), do: r.run_id)

    IO.register(notify_binding(org, "buyer-inbox"))
    IO.deliver_for(run_id)

    [msg] = Mitos.Sink.Notify.Destination.collect("buyer-inbox")
    rendered = Mitos.JSON.encode!(msg)

    # Redaction is enforced at the data layer (01 §4): the commercial cut is a
    # fold over a strict Effect subset — rationale/harness/skill never reach it.
    refute rendered =~ "rationale"
    refute rendered =~ "harness"
    refute rendered =~ "skill"
  end

  test "the web sink records the web surface delivery (the web app is one sink, 10 §6)" do
    org = unique_org()
    ingest_and_finish(org, %{"inputs" => %{}}, "w-1")
    run_id = hd(for r <- Mitos.Runs.list(), String.starts_with?(r.run_id, org <> "/"), do: r.run_id)

    IO.register(web_binding(org))

    assert {:ok, [{:sent, :web, "web", receipt}]} = IO.deliver_for(run_id)
    assert "/runs/#{run_id}/deliverable" in receipt.urls

    sent = Enum.find(effects(run_id), &(&1.type == :delivery_sent))
    assert sent.ref["sink"] == "web"
  end

  test "the reply sink round-trips back to the connector's reply_ref (10 §5.4)" do
    org = unique_org()
    thread = "slack/T123/C456"

    IO.register(webhook_binding(org, cap_dir()))

    {:ok, run_id} =
      IO.ingest(
        %{"org" => org},
        "webhook",
        %{body: %{"inputs" => %{}, "reply_ref" => thread}, headers: %{}, id: "rt-1"}
      )

    Run.await(run_id)

    IO.register(reply_binding(org))

    assert {:ok, [{:sent, :reply, ^thread, _}]} = IO.deliver_for(run_id)
    [reply] = Mitos.Sink.Reply.Destination.collect(thread)
    assert reply.reply_ref == thread
    assert reply.status == :finished
  end

  # --- the folds consume the delivery Effects (01 §4) ---

  test "Run.State folds deliveries, and the deliveries/1 read model returns them" do
    org = unique_org()
    ingest_and_finish(org, %{"inputs" => %{}}, "f-1")
    run_id = hd(for r <- Mitos.Runs.list(), String.starts_with?(r.run_id, org <> "/"), do: r.run_id)

    IO.register(notify_binding(org, "fold-chat"))
    IO.deliver_for(run_id)

    # Run.State (the orchestrator's read model) consumes the delivery Effect.
    state = Run.status(run_id)
    assert [%{sink: "notify", dest: "fold-chat"}] = state.deliveries.sent

    # deliveries/1 — the I/O read model, a pure fold over delivery_* Effects.
    dv = IO.deliveries(run_id)
    assert [%{"sink" => "notify", "dest" => "fold-chat"}] = dv.sent
    assert dv.failed == []
  end

  test "the proof page surfaces delivery_sent on its timeline" do
    org = unique_org()
    ingest_and_finish(org, %{"inputs" => %{}}, "p-1")
    run_id = hd(for r <- Mitos.Runs.list(), String.starts_with?(r.run_id, org <> "/"), do: r.run_id)

    IO.register(notify_binding(org, "proof-chat"))
    IO.deliver_for(run_id)

    proof = Mitos.Proof.render(run_id)
    types = Enum.map(proof["timeline"], & &1["type"])
    assert "delivery_sent" in types
    delivery = Enum.find(proof["timeline"], &(&1["type"] == "delivery_sent"))
    assert delivery["sink"] == "notify"
  end

  test "deliver_for on a run with no output bindings is a clean no-op" do
    org = unique_org()
    ingest_and_finish(org, %{"inputs" => %{}}, "no-1")
    run_id = hd(for r <- Mitos.Runs.list(), String.starts_with?(r.run_id, org <> "/"), do: r.run_id)

    assert {:ok, []} = IO.deliver_for(run_id)
    refute Enum.any?(effects(run_id), &(&1.type == :delivery_sent))
  end

  # --- app I/O binding activation (12 §5: ship defaults, bind at install) ---

  test "adding an app activates its shipped io defaults as live bindings" do
    org = unique_org()
    dir = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "bound-app"])
    File.mkdir_p!(Path.join(dir, "pipelines"))
    File.cp_r!(cap_dir(), Path.join(dir, "worker"))

    File.write!(Path.join(dir, "pipelines/main.md"), pipeline_md())

    File.write!(Path.join(dir, "app.yaml"), """
    name: bound-app
    version: 0.1.0
    capabilities:
      - { ref: "worker", as: "worker" }
    pipelines:
      - { id: "main", file: "pipelines/main.md" }
    entrypoints:
      - { id: "run", pipeline: "main", workspace: "none", inputs: { schema: { message: "string" } } }
    io:
      inputs:
        - { connector: webhook, entrypoint: run, select: { mode: explicit } }
      outputs:
        - { sink: notify, on: capability_finished, entrypoint: run }
    """)

    app = Mitos.App.Manifest.load!(dir)
    Mitos.App.add(app, %{"org" => org})

    # the webhook input binding is live: a webhook ingest runs the entrypoint.
    assert {:ok, run_id} =
             IO.ingest(%{"org" => org}, "bound-app.run.webhook", %{
               body: %{"inputs" => %{"message" => "hi"}},
               headers: %{},
               id: "app-1"
             })

    assert Mitos.Run.await(run_id).status == :finished

    # the notify output binding is live: a delivery fires it.
    {:ok, [{:sent, :notify, dest, _}]} = IO.deliver_for(run_id)
    assert dest != nil
    assert Enum.any?(effects(run_id), &(&1.type == :delivery_sent))
  end

  test "a webhook binding with a secret rejects a bad signature; one with none admits (10 §8 Q3)" do
    mod = Mitos.Connector.Webhook
    {:ok, handle} = mod.mount(%{secret: "s3cret"})

    bad = %{body: %{"inputs" => %{}}, headers: %{"x-mitos-signature" => "deadbeef"}, id: "x"}
    assert {:error, :bad_signature} = mod.receive(handle, bad)

    # A correct HMAC over the canonical body admits.
    good_sig =
      :crypto.mac(:hmac, :sha256, "s3cret", Mitos.canonical_json(%{"inputs" => %{}}))
      |> Base.encode16(case: :lower)

    good = %{body: %{"inputs" => %{}}, headers: %{"x-mitos-signature" => good_sig}, id: "x"}
    assert {:ok, [_]} = mod.receive(handle, good)

    # No secret ⇒ unauthenticated (dev only) — always admits.
    {:ok, plain} = mod.mount(%{})
    assert {:ok, [_]} = mod.receive(plain, %{body: %{"inputs" => %{}}, headers: %{}, id: "y"})
  end

  # --- HTTP surface (10 §3.1 / §5.3) ---

  test "the /ingest webhook route turns an inbound POST into a run, and /runs/:id/deliver re-drives" do
    import Plug.Conn

    org = "io_http_" <> Mitos.id()
    cap = cap_dir()
    IO.register(webhook_binding(org, cap))

    body = Mitos.JSON.encode!(%{"intent" => "http job", "inputs" => %{"k" => "v"}})

    conn =
      Plug.Test.conn(:post, "/ingest/#{org}/webhook", body)
      |> put_req_header("content-type", "application/json")
      |> put_req_header("x-mitos-delivery-id", "http-1")
      |> Mitos.Web.Router.call(Mitos.Web.Router.init([]))

    assert conn.status == 202
    %{"run_id" => run_id} = Mitos.JSON.decode!(conn.resp_body)
    Run.await(run_id)

    IO.register(notify_binding(org, "http-out"))

    conn =
      Plug.Test.conn(:post, "/runs/#{run_id}/deliver", "")
      |> Mitos.Web.Router.call(Mitos.Web.Router.init([]))

    assert conn.status == 200
    %{"deliveries" => [%{"sink" => "notify"} | _]} =
      Mitos.JSON.decode!(conn.resp_body)
  end
end
