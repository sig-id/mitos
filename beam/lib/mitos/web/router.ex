defmodule Mitos.Web.Router do
  @moduledoc """
  The HTTP surface. UI, CLI, API, and automation MUST call the same backend
  operations (01 §10.11) — every handler here is a thin adapter over
  `Mitos.Facilitator`, `Mitos.Run`, and the log's read models (08 §2); nothing
  is computed here that the CLI can't reach by the same function calls.

  The buyer surface is the Gentix-compliant endpoint (08 §8):
  `POST /capabilities/{id}/runs` with x402 admission — no payment ⇒ 402 with a
  quote; verified payment ⇒ the run. Delivery is `GET .../deliverable` +
  `GET .../proof` (08 §5).
  """
  use Plug.Router

  alias Mitos.{Catalog, Facilitator, Proof, Run}
  alias Mitos.Web.Pages

  plug(:match)
  plug(Plug.Parsers, parsers: [:json, :urlencoded], json_decoder: Mitos.JSON, pass: ["*/*"])
  plug(:dispatch)

  get "/health" do
    json(conn, 200, %{status: "ok"})
  end

  # --- the web surfaces (11): server-rendered readers of the log ---
  #
  # Identity is a role cookie (operator | seller | buyer) standing in for an
  # IdP. Locally there is no sign-in wall: with no cookie the console defaults
  # to the operator view (11 §13.2, :web_default_role); the role picker
  # survives as a "view as" cut preview. The role selects navigation and the
  # redaction cut (08 §2); buyer proof/watch URLs stay reachable without
  # sign-in — "buy is a URL, no account" (11 §9), redacted by construction.

  # The customer-facing site (02/08 told to the counterparties) — same BEAM,
  # same brand as the console.
  get "/" do
    html(conn, 200, Mitos.Web.Landing.page())
  end

  get "/web/login" do
    html(conn, 200, Pages.login(role(conn)))
  end

  get "/web/login/:role_name" when role_name in ~w(operator seller buyer) do
    conn
    |> put_resp_cookie("mitos_role", role_name, max_age: 30 * 24 * 3600, http_only: true)
    |> put_resp_header("location", landing(role_name))
    |> send_resp(303, "")
  end

  get "/web/logout" do
    conn
    |> delete_resp_cookie("mitos_role")
    |> put_resp_header("location", if(default_role(), do: "/web", else: "/web/login"))
    |> send_resp(303, "")
  end

  get "/web" do
    case role(conn) do
      "operator" -> html(conn, 200, Pages.home("operator"))
      nil -> redirect(conn, "/web/login")
      other -> redirect(conn, landing(other))
    end
  end

  get "/web/runs" do
    require_role(conn, ["operator"], fn r -> html(conn, 200, Pages.runs(r)) end)
  end

  get "/web/apps" do
    require_role(conn, ["operator"], fn r -> html(conn, 200, Pages.apps(r)) end)
  end

  get "/web/apps/:app_id" do
    require_role(conn, ["operator"], fn r ->
      case Pages.app_detail(app_id, r) do
        {:ok, page} -> html(conn, 200, page)
        :not_found -> not_found_page(conn, r, "no such application")
      end
    end)
  end

  get "/web/apps/:app_id/entrypoints/:ep_id" do
    require_role(conn, ["operator"], fn r ->
      case Pages.order_form(app_id, ep_id, r) do
        {:ok, page} -> html(conn, 200, page)
        :not_found -> not_found_page(conn, r, "no such entrypoint")
      end
    end)
  end

  # The order form maps onto the same backend op the CLI uses (01 §10.11):
  # entrypoint → run (12 §4). Inputs are validated against the entrypoint's
  # schema before any model spend (12 §4: validate before run).
  post "/web/apps/:app_id/entrypoints/:ep_id/run" do
    require_role(conn, ["operator"], fn r ->
      app = Catalog.find_app(app_id)

      cond do
        is_nil(app) or is_nil(Mitos.App.Manifest.entrypoint(app, ep_id)) ->
          not_found_page(conn, r, "no such entrypoint")

        Mitos.App.Manifest.draft?(app, ep_id) ->
          html(conn, 422, Mitos.Web.HTML.layout("draft entrypoint", "<p>This entrypoint is still a draft — give it a typed order form (an inputs schema in <code>app.yaml</code>) and it becomes runnable.</p>", role: r))

        true ->
          p = conn.body_params

          # base inputs from the JSON textarea (advanced), overlaid with the
          # per-field `input_<name>` form fields (12 §4).
          base_inputs =
            case Mitos.JSON.decode(p["inputs"] || "{}") do
              {:ok, %{} = m} -> m
              _ -> %{}
            end

          inputs =
            for {"input_" <> name, v} <- p, v not in [nil, ""], into: base_inputs do
              {name, v}
            end

          parameters = for {"param_" <> name, v} <- p, into: %{}, do: {name, v}
          workspace = if p["workspace"] in [nil, ""], do: nil, else: p["workspace"]

          # optional spend ceiling (a declared budget, enforced by the meter)
          budget =
            case Float.parse(p["budget_usd"] || "") do
              {usd, _} when usd > 0 -> %{"usd" => usd}
              _ -> %{}
            end

          case Mitos.App.run_entrypoint(app, ep_id, %{"org" => "local", "project" => app.name},
                 %{inputs: inputs, workspace: workspace, parameters: parameters, budget: budget}
               ) do
            {:ok, run_id} ->
              redirect(conn, "/web/runs/#{run_id}")

            {:error, {:missing_required, fields}} ->
              html(conn, 422, Mitos.Web.HTML.layout("missing inputs", "<p>These required inputs are missing: <b>#{Mitos.Web.HTML.esc(Enum.join(fields, ", "))}</b>. Go back and fill them in.</p>", role: r))

            {:error, {:bad_type, type, _v}} ->
              html(conn, 422, Mitos.Web.HTML.layout("bad input", "<p>An input doesn't match its declared type (<code>#{Mitos.Web.HTML.esc(type)}</code>). Go back and correct it.</p>", role: r))

            {:error, _} ->
              not_found_page(conn, r, "no such entrypoint")
          end
      end
    end)
  end

  # The storefront is public — buy is a URL (11 §9).
  get "/web/capabilities" do
    html(conn, 200, Pages.capabilities(role(conn)))
  end

  get "/web/capabilities/:id" do
    case Pages.buy(id, role(conn)) do
      {:ok, page} -> html(conn, 200, page)
      :not_found -> not_found_page(conn, role(conn), "not published")
    end
  end

  get "/web/attribution" do
    require_role(conn, ["seller", "operator"], fn r ->
      html(conn, 200, Pages.attribution(r))
    end)
  end

  # The iterate step (11 §13.3): re-start a run from its durable config. The
  # config.json holds everything needed to run again (04 §7); a fresh run_id
  # is minted. Deliberately NOT copied: offer_key (a paid run's re-run must
  # not re-settle), task_id and ingress (this is a fresh dev iteration, not
  # the original signal).
  post "/web/runs/*path" do
    case {Enum.reverse(path), role(conn)} do
      {["rerun" | rid], "operator"} ->
        run_id = rejoin(rid)

        with {:ok, config} <- Mitos.Run.Config.load(run_id),
             {:ok, new_id} <-
               Run.start_run(
                 tenant: config.tenant,
                 pipeline: config.pipeline,
                 capabilities: config.capabilities,
                 workspace: config.workspace,
                 inputs: config.inputs,
                 parameters: config.parameters,
                 budget: config.budget,
                 scope: config.scope,
                 trust_tier: config.trust_tier,
                 policy: config.policy,
                 max_iterations: config.max_iterations
               ) do
          redirect(conn, "/web/runs/#{new_id}")
        else
          {:error, :no_config} ->
            not_found_page(conn, "operator", "no such run")

          {:error, reason} ->
            html(
              conn,
              422,
              Mitos.Web.HTML.layout(
                "run again",
                "<p>Could not start the run: <code>#{Mitos.Web.HTML.esc(inspect(reason))}</code></p>",
                role: "operator"
              )
            )
        end

      {["rerun" | _], _} ->
        redirect(conn, "/web/login")

      _ ->
        json(conn, 405, %{error: "method_not_allowed"})
    end
  end

  get "/web/runs/*path" do
    r = role(conn)

    result =
      case Enum.reverse(path) do
        # dev-cut pages: operator only (the recipe side of the cut)
        [stage, "trace" | rid] ->
          if r == "operator", do: Pages.trace(rejoin(rid), stage, r), else: {:redirect, "/web/login"}

        ["report" | rid] ->
          if r == "operator",
            do: Pages.report(rejoin(rid), :dev, r),
            else: {:redirect, "/web/runs/#{rejoin(rid)}/proof"}

        # commercial-cut pages: public by URL — redacted by construction (08 §5)
        ["proof" | rid] ->
          Pages.report(rejoin(rid), :commercial, r)

        ["watch" | rid] ->
          Pages.watch(rejoin(rid), :commercial, r)

        _rid ->
          run_id = Enum.join(path, "/")

          if r == "operator",
            do: Pages.watch(run_id, :dev, r),
            else: {:redirect, "/web/runs/#{run_id}/watch"}
      end

    case result do
      {:ok, page} -> html(conn, 200, page)
      {:redirect, to} -> redirect(conn, to)
      :not_found -> not_found_page(conn, r, "no such run")
    end
  end

  # --- catalog (public view — never the recipe, 08 §1) ---

  get "/capabilities" do
    caps =
      for {id, m} <- Catalog.capabilities() do
        offer = Catalog.get_offer(id)

        %{
          capability_id: id,
          name: m.name,
          version: m.version,
          trust_tier: m.trust_tier,
          price_usd: offer && offer.price_usd
        }
      end

    json(conn, 200, %{capabilities: caps})
  end

  # --- the paid endpoint (08 §7/§8) ---

  post "/capabilities/:id/runs" do
    case List.first(get_req_header(conn, "x-payment")) do
      nil ->
        case Facilitator.quote(id) do
          {:ok, q} ->
            # HTTP 402 Payment Required + the x402 quote (08 §7).
            json(conn, 402, %{
              error: "payment_required",
              quote_id: q.quote_id,
              amount_usdc: q.amount_usdc,
              pay_to: q.pay_to
            })

          {:error, :not_published} ->
            json(conn, 404, %{error: "not_published"})
        end

      payment ->
        inputs = conn.body_params["inputs"] || conn.body_params["params"] || %{}

        case Facilitator.purchase(id, payment, inputs) do
          {:ok, admitted} ->
            json(conn, 202, %{
              run_id: admitted.run_id,
              task_id: admitted.task_id,
              quote_id: admitted.quote_id,
              status_url: "/runs/#{admitted.run_id}",
              proof_url: "/runs/#{admitted.run_id}/proof"
            })

          {:error, reason} ->
            # Refused admission emits nothing billable (08 §7).
            json(conn, 402, %{error: "payment_rejected", reason: inspect(reason)})
        end
    end
  end

  # --- local/operator run surface (same op the CLI uses) ---

  post "/runs" do
    p = conn.body_params

    case Run.start_run(
           tenant: p["tenant"] || %{"org" => "local", "project" => "default"},
           pipeline: p["pipeline"] || "",
           capabilities: p["capabilities"] || %{},
           workspace: p["workspace"],
           inputs: p["inputs"] || %{},
           budget: p["budget"] || %{},
           scope: p["scope"],
           trust_tier: p["trust_tier"]
         ) do
      {:ok, run_id} -> json(conn, 202, %{run_id: run_id, status_url: "/runs/#{run_id}"})
      {:error, reason} -> json(conn, 422, %{error: inspect(reason)})
    end
  end

  # --- I/O plane (10): connectors in / sinks out ---
  #
  # Ingress: a webhook binding turns an inbound POST into a Task + run (10 §3.1).
  # The web app's order form is the `manual`/`web` connector realization (10 §6);
  # both reach the same backend op the CLI uses (01 §10.11).
  post "/ingest/:org/:name" do
    headers = for {k, v} <- conn.req_headers, into: %{}, do: {k, v}

    delivery = %{
      body: conn.body_params,
      headers: headers,
      id: List.first(get_req_header(conn, "x-mitos-delivery-id"))
    }

    case Mitos.IO.ingest(%{"org" => org}, name, delivery) do
      {:ok, run_id} when is_binary(run_id) ->
        json(conn, 202, %{run_id: run_id, status_url: "/runs/#{run_id}"})

      {:ok, :duplicate} ->
        json(conn, 200, %{status: "duplicate"})

      {:error, reason} ->
        json(conn, 422, %{error: inspect(reason)})
    end
  end

  # Egress re-drive: idempotent delivery for a finished run (10 §5.3). Safe to
  # call repeatedly; a delivery_sent keyed (sink, dest) suppresses re-delivery.
  post "/runs/*path" do
    case Enum.reverse(path) do
      ["deliver" | rid] ->
        run_id = rejoin(rid)

        case Mitos.IO.deliver_for(run_id) do
          {:ok, results} ->
            view =
              Enum.map(results, fn
                {:sent, sink, dest, receipt} ->
                  %{status: "sent", sink: sink, dest: dest, receipt: receipt}

                {:failed, sink, dest, reason} ->
                  %{status: "failed", sink: sink, dest: dest, reason: inspect(reason)}
              end)

            json(conn, 200, %{run_id: run_id, deliveries: view})

          {:error, reason} ->
            json(conn, 404, %{error: inspect(reason)})
        end

      _rid ->
        json(conn, 405, %{error: "method_not_allowed"})
    end
  end

  # run_id is tenant-namespaced ("<org>/<id>", 09 §4) — glob and re-join.
  get "/runs/*path" do
    case Enum.reverse(path) do
      ["proof" | rid] ->
        run_id = rejoin(rid)
        charge = settled_charge(run_id)
        json(conn, 200, Proof.render(run_id, charge))

      ["deliverable" | rid] ->
        json(conn, 200, Proof.deliverable(rejoin(rid)))

      ["events" | rid] ->
        # Operator/audit read — full fidelity, incl. Decisions (08 §2 audit row).
        lines = rejoin(rid) |> Mitos.Log.read() |> Enum.map(&Mitos.Event.to_line/1)

        conn
        |> put_resp_content_type("application/jsonl")
        |> send_resp(200, Enum.join(lines))

      [sha, "artifacts" | rid] ->
        case Proof.artifact(rejoin(rid), sha) do
          {:ok, bytes} ->
            conn
            |> put_resp_content_type("application/octet-stream")
            |> send_resp(200, bytes)

          {:error, _} ->
            json(conn, 404, %{error: "no_such_artifact"})
        end

      _rid ->
        run_id = Enum.join(path, "/")
        state = Run.status(run_id)

        if state.events == [] do
          json(conn, 404, %{error: "no_such_run"})
        else
          json(conn, 200, %{
            run_id: run_id,
            status: state.status,
            spent: state.spent,
            stages:
              Enum.map(state.stage_order, fn id ->
                s = state.stages[id]
                %{id: id, status: s.status, signal: s.result && s.result["signal"]}
              end),
            settlement: settlement_view(run_id)
          })
        end
    end
  end

  match _ do
    json(conn, 404, %{error: "not_found"})
  end

  # --- helpers ---

  defp rejoin(reversed_segments), do: reversed_segments |> Enum.reverse() |> Enum.join("/")

  defp settled_charge(run_id) do
    case Facilitator.settlement(run_id) do
      %{charge: charge} -> charge
      _ -> nil
    end
  end

  defp settlement_view(run_id) do
    case Facilitator.settlement(run_id) do
      %{charge: charge, transfers: txs} ->
        %{
          delivered: charge.delivered?,
          buyer_charge_usd: charge.buyer_charge,
          refund_usd: charge.buyer_refund,
          transfers: Enum.map(txs, &Map.take(&1, [:kind, :amount, :tx]))
        }

      _ ->
        nil
    end
  end

  defp json(conn, status, body) do
    conn
    |> put_resp_content_type("application/json")
    |> send_resp(status, Mitos.JSON.encode!(body))
  end

  defp html(conn, status, body) do
    conn
    |> put_resp_content_type("text/html")
    |> send_resp(status, body)
  end

  # --- identity (role cookie; an IdP replaces this seam) ---
  #
  # Locally the default is the operator view (11 §13.2): no cookie means the
  # machine's owner, and the picker at /web/login is a "view as" preview.
  # Hosted deployments set :web_default_role to nil, restoring sign-in.

  defp default_role, do: Application.get_env(:mitos, :web_default_role)

  defp role(conn) do
    conn = fetch_cookies(conn)

    case conn.cookies["mitos_role"] do
      r when r in ["operator", "seller", "buyer"] -> r
      _ -> default_role()
    end
  end

  defp require_role(conn, allowed, fun) do
    case role(conn) do
      r when r != nil ->
        if r in allowed, do: fun.(r), else: redirect(conn, landing(r))

      nil ->
        redirect(conn, "/web/login")
    end
  end

  defp landing("operator"), do: "/web"
  defp landing("seller"), do: "/web/attribution"
  defp landing("buyer"), do: "/web/capabilities"
  defp landing(_), do: "/web/login"

  defp redirect(conn, to) do
    conn |> put_resp_header("location", to) |> send_resp(303, "")
  end

  defp not_found_page(conn, r, message) do
    html(
      conn,
      404,
      Mitos.Web.HTML.layout("not found", "<h1>#{Mitos.Web.HTML.esc(message)}</h1>", role: r)
    )
  end
end
