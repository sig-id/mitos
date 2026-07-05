defmodule Mitos.CLI do
  @moduledoc """
  The mitos CLI (escript). CLI-first dev, web-first operate (11 §7); every
  command is the same backend operation the web router calls (01 §10.11).

      mitos run <app_dir> [--entrypoint ID] [--workspace DIR] [-i k=v]...
                          [--budget-usd X] [--org ORG]
      mitos run-pipeline <pipeline.md> --cap name=dir [--workspace DIR] [-i k=v]...
      mitos log <run_id>
      mitos proof <run_id>
      mitos status <run_id>
      mitos publish <cap_dir> --price X --payout ADDR --seller ORG
      mitos deliver <run_id>                       # idempotent egress re-drive (10 §5.3)
      mitos import eve <agent_dir> [--out <dir>]   # Eve agent/ → app scaffold (12 §10)
      mitos serve [<cap_dir>...] [--price X --payout ADDR --seller ORG]
                  [--app <app_dir>]...                       # register apps for the web UI
                  [--http-port N] [--broker-port N]          # per-instance port override
  """

  def main(argv) do
    {opts, args, _} =
      OptionParser.parse(argv,
        strict: [
          entrypoint: :string,
          workspace: :string,
          budget_usd: :float,
          org: :string,
          input: :keep,
          cap: :keep,
          price: :float,
          payout: :string,
          seller: :string,
          trust_tier: :string,
          app: :keep,
          out: :string,
          http_port: :integer,
          broker_port: :integer
        ],
        aliases: [i: :input, e: :entrypoint, w: :workspace]
      )

    case args do
      ["run", app_dir] -> boot() && run_app(app_dir, opts)
      ["run-pipeline", file] -> boot() && run_pipeline(file, opts)
      ["log", run_id] -> boot() && print_log(run_id)
      ["proof", run_id] -> boot() && print_json(Mitos.Proof.render(run_id, settled(run_id)))
      ["status", run_id] -> boot() && print_status(run_id)
      ["publish", cap_dir] -> boot() && publish(cap_dir, opts)
      ["deliver", run_id] -> boot() && deliver(run_id)
      ["import", "eve", agent_dir] -> import_eve(agent_dir, opts)
      ["serve" | cap_dirs] -> serve(cap_dirs, opts)
      _ -> usage()
    end
  end

  # --- commands ---

  # Run an application entrypoint (12 §4): a user runs a pipeline named by an
  # entrypoint, never a bare capability (01 §8.8).
  defp run_app(app_dir, opts) do
    app = Mitos.App.Manifest.load!(app_dir)

    ep =
      case opts[:entrypoint] do
        nil -> List.first(app.entrypoints) || halt("app declares no entrypoints")
        id -> Mitos.App.Manifest.entrypoint(app, id) || halt("no entrypoint #{id}")
      end

    pipeline =
      Mitos.App.Manifest.pipeline(app, ep.pipeline) ||
        halt("entrypoint #{ep.id} names unknown pipeline #{ep.pipeline}")

    capabilities =
      Map.new(app.capabilities, fn c -> {c.as, Path.expand(c.ref, app_dir)} end)

    start_and_await(
      pipeline: pipeline.body,
      capabilities: capabilities,
      workspace: opts[:workspace] || workspace_from(ep),
      inputs: Map.merge(ep.defaults, inputs(opts)),
      budget: budget(opts),
      tenant: %{"org" => opts[:org] || "local", "project" => app.name},
      trust_tier: opts[:trust_tier]
    )
  end

  defp run_pipeline(file, opts) do
    body = File.read!(file)

    capabilities =
      opts
      |> Keyword.get_values(:cap)
      |> Map.new(fn kv ->
        [name, dir] = String.split(kv, "=", parts: 2)
        {name, Path.expand(dir)}
      end)

    start_and_await(
      pipeline: body,
      capabilities: capabilities,
      workspace: opts[:workspace],
      inputs: inputs(opts),
      budget: budget(opts),
      tenant: %{"org" => opts[:org] || "local", "project" => "cli"},
      trust_tier: opts[:trust_tier]
    )
  end

  defp start_and_await(fields) do
    {:ok, run_id} = Mitos.Run.start_run(fields)
    IO.puts("run:    #{run_id}")

    state = Mitos.Run.await(run_id, :infinity)
    IO.puts("status: #{state.status}")

    Enum.each(state.stage_order, fn id ->
      s = state.stages[id]
      signal = (s.result && s.result["signal"]) || "-"
      IO.puts("  stage #{id}: #{s.status} (signal: #{signal})")
    end)

    IO.puts("spent:  #{state.spent.tokens} tokens · $#{state.spent.usd} · #{state.spent.wall_ms}ms")
    IO.puts("log:    #{Mitos.Log.path(run_id)}")
    if state.status != :finished, do: System.halt(1)
  end

  defp print_log(run_id) do
    run_id
    |> Mitos.Log.read()
    |> Enum.each(fn e -> IO.write(Mitos.Event.to_line(e)) end)
  end

  defp print_status(run_id) do
    state = Mitos.Run.status(run_id)

    print_json(%{
      run_id: run_id,
      status: state.status,
      spent: state.spent,
      stages: Enum.map(state.stage_order, &%{id: &1, status: state.stages[&1].status})
    })
  end

  # I/O-plane egress re-drive (10 §5.3): fold the log, fire matched sinks,
  # record delivery_* Effects. Idempotent — safe to re-run.
  defp deliver(run_id) do
    case Mitos.IO.deliver_for(run_id) do
      {:ok, results} ->
        IO.puts("deliveries for #{run_id}:")

        Enum.each(results, fn
          {:sent, sink, dest, receipt} ->
            IO.puts("  + #{sink} -> #{dest}  (#{inspect(receipt)})")

          {:failed, sink, dest, reason} ->
            IO.puts("  x #{sink} -> #{dest}  (#{inspect(reason)})")
        end)

      {:error, reason} ->
        halt("deliver failed: #{inspect(reason)}")
    end
  end

  # Eve agent/ → mitos app scaffold (12 §10, 03 §10.1). The output is a draft
  # app runnable from disk; publish is refused until the order form is typed.
  defp import_eve(agent_dir, opts) do
    out = opts[:out] || Path.join(Path.dirname(Path.expand(agent_dir)), "#{Path.basename(agent_dir)}-app")

    case Mitos.Import.Eve.scaffold(agent_dir, out) do
      {:ok, out_dir} ->
        app = Mitos.App.Manifest.load!(out_dir)

        IO.puts("imported: #{app.name} → #{out_dir}")
        IO.puts("  app_id:  #{String.slice(app.app_id, 0, 12)}…")
        IO.puts("  run it:  mitos run #{out_dir} --entrypoint run -i message=\"hello\"")
        IO.puts("  NOTE:    entrypoint 'run' is a DRAFT — type the order form before publish (12 §10)")

      {:error, reason} ->
        halt("import failed: #{inspect(reason)}")
    end
  end

  defp publish(cap_dir, opts) do
    offer = %{
      price_usd: opts[:price] || halt("--price required"),
      payout_addr: opts[:payout] || halt("--payout required"),
      seller: opts[:seller] || "seller"
    }

    case Mitos.Facilitator.publish(Path.expand(cap_dir), offer) do
      {:ok, id} ->
        IO.puts("capability_id: #{id}")
        IO.puts("endpoint:      POST /capabilities/#{id}/runs  (x402)")

      {:error, reason} ->
        halt("publish failed: #{inspect(reason)}")
    end
  end

  # The catalog is in-memory (MVP), so publishing for the hosted surface
  # happens in the serving VM: `mitos serve <cap_dir> --price X --payout A`.
  # `--app <dir>` registers an application so its entrypoints get web order
  # forms (11 §4).
  defp serve(cap_dirs, opts) do
    boot(http_port: opts[:http_port], broker_port: opts[:broker_port])
    Enum.each(cap_dirs, &publish(&1, opts))

    for dir <- Keyword.get_values(opts, :app) do
      app = dir |> Path.expand() |> Mitos.App.Manifest.load!()
      # Adding an app activates its shipped I/O defaults for the local tenant
      # (12 §5: ship defaults, bind at install).
      Mitos.App.add(app, %{"org" => "local"})
      IO.puts("app: #{app.name} (#{String.slice(app.app_id, 0, 12)}…) → /web/apps")
    end

    port = Application.fetch_env!(:mitos, :http_port)
    IO.puts("mitos control plane on http://localhost:#{port} (broker on 127.0.0.1:#{Application.fetch_env!(:mitos, :broker_port)})")
    Process.sleep(:infinity)
  end

  # --- helpers ---

  defp boot(overrides \\ []) do
    # Load first so the compiled config is baked into env, THEN apply overrides
    # — ensure_all_started would otherwise re-load and clobber put_env.
    Application.load(:mitos)

    for {k, v} <- overrides, not is_nil(v) do
      Application.put_env(:mitos, k, v)
    end

    {:ok, _} = Application.ensure_all_started(:mitos)
    true
  end

  defp inputs(opts) do
    opts
    |> Keyword.get_values(:input)
    |> Map.new(fn kv ->
      case String.split(kv, "=", parts: 2) do
        [k, v] -> {k, v}
        [k] -> {k, true}
      end
    end)
  end

  defp budget(opts) do
    case opts[:budget_usd] do
      nil -> %{}
      usd -> %{"usd" => usd}
    end
  end

  defp workspace_from(%{workspace: %{kind: "repo", ref: ref}}) when is_binary(ref), do: ref
  defp workspace_from(_), do: nil

  defp settled(run_id) do
    case Mitos.Facilitator.settlement(run_id) do
      %{charge: charge} -> charge
      _ -> nil
    end
  end

  defp print_json(term), do: IO.puts(Mitos.JSON.encode!(term))

  defp usage do
    IO.puts(@moduledoc)
    System.halt(1)
  end

  defp halt(msg) do
    IO.puts(:stderr, "error: " <> msg)
    System.halt(1)
  end
end
