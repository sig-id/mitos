defmodule Mitos.CLI do
  @moduledoc """
  The mitos CLI (escript). CLI-first dev, web-first operate (11 §7); every
  command is the same backend operation the web router calls (01 §10.11).

      mitos run <app_dir> [--entrypoint ID] [--workspace DIR] [-i k=v]...
                          [--budget-usd X] [--org ORG] [--dev] [--no-sandbox]
                          # --no-sandbox works on the workspace IN PLACE: no copy,
                          # no clone ceiling, and the agent's edits land in your
                          # real tree. For a repo you own at T1; refused at T2.
                          [--http-port N] [--broker-port N]
                          # --dev lets pi read ~/.pi so runs boot locally;
                          # ports let a CLI run coexist with a live control plane.
      mitos run-pipeline <pipeline.md> --cap name=dir [--workspace DIR] [-i k=v]...
                                 [--dev] [--http-port N] [--broker-port N]
      mitos log <run_id>
      mitos proof <run_id>
      mitos status <run_id>
      mitos publish <cap_dir> --price X --payout ADDR --seller ORG
      mitos deliver <run_id>                       # idempotent egress re-drive (10 §5.3)
      mitos decisions [<run_id>]                   # cards waiting on you (15, 11 §4a)
      mitos resolve <decision_id> <choice> [--note TEXT] [--value V]
      mitos autonomy [<run_id>...]                 # autonomy ratio / horizon / precision (16 §6)
      mitos contract [--live] [--quarantine]       # adapter observation contracts (14 §5)
      mitos interrupts                             # the interrupt distribution (02 §6.1)
      mitos eval [--strict]                        # replay the corpus against today's policy (16 §6.1)
      mitos gc [--all] [--dry-run] [--older-than N]  # reclaim sandboxes + per-stage copies
                          # Evidence is never touched: events.jsonl, artifacts,
                          # trace and spool stay. --all ignores age; a LIVE run's
                          # sandbox is never removed.
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
          note: :string,
          value: :string,
          live: :boolean,
          quarantine: :boolean,
          all: :boolean,
          dry_run: :boolean,
          older_than: :integer,
          http_port: :integer,
          broker_port: :integer,
          dev: :boolean,
          no_sandbox: :boolean
        ],
        aliases: [i: :input, e: :entrypoint, w: :workspace]
      )

    case args do
      ["run", app_dir] -> boot_run(opts) && run_app(app_dir, opts)
      ["run-pipeline", file] -> boot_run(opts) && run_pipeline(file, opts)
      ["log", run_id] -> boot() && print_log(run_id)
      ["proof", run_id] -> boot() && print_json(Mitos.Proof.render(run_id, settled(run_id)))
      ["status", run_id] -> boot() && print_status(run_id)
      ["publish", cap_dir] -> boot() && publish(cap_dir, opts)
      ["deliver", run_id] -> boot() && deliver(run_id)
      ["decisions"] -> boot() && print_decisions(nil)
      ["decisions", run_id] -> boot() && print_decisions(run_id)
      ["resolve", id, choice] -> boot() && resolve_decision(id, choice, opts)
      ["autonomy"] -> boot() && print_autonomy(Mitos.Runs.list() |> Enum.map(& &1.run_id))
      ["autonomy" | run_ids] -> boot() && print_autonomy(run_ids)
      ["contract"] -> boot() && print_contract(opts)
      ["interrupts"] -> boot() && print_interrupts()
      ["eval"] -> boot() && print_eval(opts)
      ["gc"] -> boot() && print_gc(opts)
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
      trust_tier: opts[:trust_tier],
      workspace_mode: workspace_mode(opts)
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
      trust_tier: opts[:trust_tier],
      workspace_mode: workspace_mode(opts)
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

    IO.puts(
      "spent:  #{state.spent.tokens} tokens · $#{state.spent.usd} · #{state.spent.wall_ms}ms"
    )

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
    out =
      opts[:out] ||
        Path.join(Path.dirname(Path.expand(agent_dir)), "#{Path.basename(agent_dir)}-app")

    case Mitos.Import.Eve.scaffold(agent_dir, out) do
      {:ok, out_dir} ->
        app = Mitos.App.Manifest.load!(out_dir)

        IO.puts("imported: #{app.name} → #{out_dir}")
        IO.puts("  app_id:  #{String.slice(app.app_id, 0, 12)}…")
        IO.puts("  run it:  mitos run #{out_dir} --entrypoint run -i message=\"hello\"")

        IO.puts(
          "  NOTE:    entrypoint 'run' is a DRAFT — type the order form before publish (12 §10)"
        )

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
    boot(http_port: opts[:http_port], broker_port: opts[:broker_port], dev: opts[:dev])
    Enum.each(cap_dirs, &publish(&1, opts))

    for dir <- Keyword.get_values(opts, :app) do
      app = dir |> Path.expand() |> Mitos.App.Manifest.load!()
      # Adding an app activates its shipped I/O defaults for the local tenant
      # (12 §5: ship defaults, bind at install).
      Mitos.App.add(app, %{"org" => "local"})
      IO.puts("app: #{app.name} (#{String.slice(app.app_id, 0, 12)}…) → /web/apps")
    end

    port = Application.fetch_env!(:mitos, :http_port)

    IO.puts(
      "mitos control plane on http://localhost:#{port} (broker on 127.0.0.1:#{Application.fetch_env!(:mitos, :broker_port)})"
    )

    Process.sleep(:infinity)
  end

  # --- helpers ---

  # `run`/`run-pipeline` forward the same --http-port/--broker-port/--dev
  # overrides `serve` uses, so a CLI run can boot alongside a live control
  # plane (no :eaddrinuse) and, with --dev, let pi read the operator's ~/.pi
  # auth the same way `serve --dev` does. A `run` only executes one pipeline —
  # it is not the hosted control plane — so it does NOT stand up the model/MITM
  # edge proxy (those are serve concerns); disabling them also avoids colliding
  # on the escript-baked model_proxy port (4648) that a live serve holds.
  defp boot_run(opts),
    do:
      boot(
        http_port: opts[:http_port],
        broker_port: opts[:broker_port],
        model_proxy_port: 0,
        mitm_proxy_port: 0,
        dev: opts[:dev]
      )

  defp boot(overrides \\ []) do
    # Load first so the compiled config is baked into env, THEN apply overrides
    # — ensure_all_started would otherwise re-load and clobber put_env.
    Application.load(:mitos)

    for {k, v} <- overrides, not is_nil(v) do
      Application.put_env(:mitos, k, v)
    end

    # `--dev`: local T1 conveniences — let pi read the operator's real ~/.pi so
    # it boots (its startup auth check). Production routes model calls through
    # the broker/MITM edge (05 R1) and leaves this off.
    if Keyword.get(overrides, :dev) do
      Application.put_env(:mitos, :pi_dev_auth, true)
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

  # nil means "use the configured default" — only an explicit --no-sandbox turns
  # the boundary off, and the run records the choice on its own log.
  defp workspace_mode(opts), do: if(opts[:no_sandbox], do: :in_place, else: nil)

  defp workspace_from(%{workspace: %{kind: "repo", ref: ref}}) when is_binary(ref), do: ref
  defp workspace_from(_), do: nil

  defp settled(run_id) do
    case Mitos.Facilitator.settlement(run_id) do
      %{charge: charge} -> charge
      _ -> nil
    end
  end

  # 11 §4a rendered for a terminal: the same card the phone gets, at list
  # density. The console and the push are one renderer, two densities.
  defp print_decisions(run_id) do
    case Mitos.Decision.open(run_id) do
      [] ->
        IO.puts("nothing waiting on you.")

      cards ->
        Enum.each(cards, fn c ->
          waiting = if is_nil(c.default), do: "WAITING FOR YOU", else: "defaults to [#{c.default}] in #{div(c.ttl_ms, 60_000)}m"

          IO.puts("""
          #{c.decision_id}  (#{c.reversibility}, #{c.kind})  #{waiting}
            run:      #{c.run_id}#{if c.stage_id, do: " / " <> to_string(c.stage_id), else: ""}
            #{c.question}
            tried:    #{c.context.attempted || "-"}
            blocked:  #{c.context.blocked_by || "-"}
            options:  #{Enum.map_join(c.options, "  ", &("[" <> &1.id <> "] " <> &1.label))}
            resolve:  mitos resolve #{c.decision_id} <choice>
          """)
        end)
    end
  end

  defp resolve_decision(id, choice, opts) do
    resolution = %{
      resolver: System.get_env("USER") || "operator",
      choice: choice,
      note: opts[:note],
      value: opts[:value],
      via: "cli"
    }

    case Mitos.Decision.resolve(id, resolution) do
      :ok -> IO.puts("resolved #{id} as #{choice}")
      {:error, reason} -> halt("could not resolve #{id}: #{inspect(reason)}")
    end
  end

  defp print_autonomy([]), do: IO.puts("no runs yet.")

  defp print_autonomy(run_ids) do
    m = Mitos.Autonomy.fleet(run_ids)

    IO.puts("""
    runs                  #{m.runs}
    autonomy ratio        #{fmt_ratio(m.autonomy_ratio)}  (productive wall-clock per human decision)
    unattended horizon    #{fmt_ms(m.unattended_horizon_p50_ms)} p50  (to the first decision with no default)
    decision precision    #{fmt_pct(m.decision_precision)}  (cards NOT resolved as their default)
    rewind depth          #{m.rewind_depth}
    green rate            #{fmt_pct(m.green_rate)}
    """)
  end

  defp print_contract(opts) do
    results = Mitos.Observation.Contract.check_all(live: opts[:live], quarantine: opts[:quarantine])
    IO.puts(Mitos.Observation.Contract.report(results))
    if Enum.all?(results, & &1.ok), do: :ok, else: System.halt(1)
  end

  # Build-order item 0 (02 §6.1): measure before building. This distribution,
  # not an opinion, decides whether the next thing to build is a gate, a broker,
  # or a supervisor.
  defp print_interrupts do
    d = Mitos.Eval.interrupt_distribution()

    if d.total == 0 do
      IO.puts("no human decisions on the record yet — nothing to measure.")
    else
      IO.puts("#{d.total} interrupts (#{d.defaulted} answered by silence)\n")

      Enum.each(d.shares, fn {bucket, share} ->
        IO.puts("  #{String.pad_trailing(to_string(bucket), 22)} #{fmt_pct(share)}")
      end)

      IO.puts("\nimplication: #{d.implication}")
    end
  end

  defp print_gc(opts) do
    result =
      Mitos.GC.sweep(all: opts[:all], dry_run: opts[:dry_run], older_than: opts[:older_than])

    IO.puts(Mitos.GC.report(result))
  end

  defp print_eval(opts) do
    result = Mitos.Eval.replay_all(nil, strict: opts[:strict])
    IO.puts(Mitos.Eval.report(result))
    if result.ok, do: :ok, else: System.halt(1)
  end

  defp fmt_ratio(:infinity), do: "no human decisions"
  defp fmt_ratio(r) when is_number(r), do: fmt_ms(round(r))
  defp fmt_ratio(_), do: "-"

  defp fmt_ms(nil), do: "-"
  defp fmt_ms(ms) when ms < 1000, do: "#{ms}ms"
  defp fmt_ms(ms) when ms < 60_000, do: "#{Float.round(ms / 1000, 1)}s"
  defp fmt_ms(ms), do: "#{Float.round(ms / 60_000, 1)}m"

  defp fmt_pct(nil), do: "-"
  defp fmt_pct(v), do: "#{round(v * 100)}%"

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
