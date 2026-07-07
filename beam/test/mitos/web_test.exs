defmodule Mitos.WebTest do
  @moduledoc """
  11 §1/§3/§4/§5: the surfaces are readers of the log — the mock role selects
  navigation + redaction cut (operator = dev cut, buyer = commercial cut,
  seller = attribution), the one renderer serves both report and proof, and
  the commercial cut never shows rationale, inner trace, or harness identity.
  Buyer proof/watch URLs work without sign-in (11 §9: buy is a URL).
  """
  use ExUnit.Case, async: false
  import Plug.Test
  import Plug.Conn

  alias Mitos.{Fixtures, Run}
  alias Mitos.Web.Router

  @moduletag timeout: 120_000
  @opts Router.init([])

  defp get_page(path, role \\ nil) do
    conn = conn(:get, path)
    conn = if role, do: put_req_header(conn, "cookie", "mitos_role=#{role}"), else: conn
    conn = Router.call(conn, @opts)
    {conn.status, conn.resp_body, conn}
  end

  defp finished_run do
    cap = Fixtures.capability("webcap")

    {:ok, run_id} =
      Run.start_run(
        tenant: %{"org" => "webtest", "project" => "ui"},
        pipeline: Fixtures.pipeline(["webcap"]),
        capabilities: %{"webcap" => cap}
      )

    state = Run.await(run_id)
    assert state.status == :finished
    run_id
  end

  # --- mock identity: role cookie selects landing + cut ---

  test "no role: /web redirects to the role picker; picking a role sets the cookie" do
    {303, _, conn} = get_page("/web")
    assert get_resp_header(conn, "location") == ["/web/login"]

    {200, body, _} = get_page("/web/login")
    assert body =~ "Operator"
    assert body =~ "Seller"
    assert body =~ "Buyer"
    assert body =~ "mock identity"

    {303, _, conn} = get_page("/web/login/operator")
    assert get_resp_header(conn, "location") == ["/web"]
    assert conn.resp_cookies["mitos_role"].value == "operator"
  end

  test "roles land on their own surface; dev pages are operator-only" do
    {303, _, conn} = get_page("/web", "buyer")
    assert get_resp_header(conn, "location") == ["/web/capabilities"]

    {303, _, conn} = get_page("/web", "seller")
    assert get_resp_header(conn, "location") == ["/web/attribution"]

    # seller/buyer cannot reach the operator's app surface
    {303, _, conn} = get_page("/web/apps", "buyer")
    assert get_resp_header(conn, "location") == ["/web/capabilities"]

    # unauthenticated dev-cut page → sign in
    {303, _, conn} = get_page("/web/apps")
    assert get_resp_header(conn, "location") == ["/web/login"]
  end

  test "a buyer asking for a run is routed to the commercial cut, never the dev cut" do
    run_id = finished_run()

    {303, _, conn} = get_page("/web/runs/#{run_id}", "buyer")
    assert get_resp_header(conn, "location") == ["/web/runs/#{run_id}/watch"]

    {303, _, conn} = get_page("/web/runs/#{run_id}/report", "buyer")
    assert get_resp_header(conn, "location") == ["/web/runs/#{run_id}/proof"]

    # the inner trace is dev-cut only
    {303, _, conn} = get_page("/web/runs/#{run_id}/trace/webcap", "buyer")
    assert get_resp_header(conn, "location") == ["/web/login"]
  end

  # --- the surfaces themselves ---

  test "dashboard lists runs from the log directory (operator)" do
    run_id = finished_run()
    {200, body, _} = get_page("/web", "operator")

    assert body =~ run_id
    assert body =~ "finished"
    # stat tiles present
    assert body =~ "model spend"
  end

  test "watch page (dev cut): outer timeline with decisions + inner trace link" do
    run_id = finished_run()
    {200, body, _} = get_page("/web/runs/#{run_id}", "operator")

    assert body =~ "advance"
    assert body =~ "capability_finished"
    # dev cut shows rationale and links the inner zoom (11 §4)
    assert body =~ "linear order"
    assert body =~ "/web/runs/#{run_id}/trace/webcap"
    # terminal run: no auto-refresh
    refute body =~ "http-equiv"
  end

  test "inner trace page renders the persisted harness stream (11 §4)" do
    run_id = finished_run()
    {200, body, _} = get_page("/web/runs/#{run_id}/trace/webcap", "operator")

    assert body =~ "assistant"
    assert body =~ "agent: working in"
  end

  test "inner trace page groups events into collapsible turns with highlighted tools" do
    # A synthetic pi-shaped trace written straight to the run dir exercises the
    # new transcript UI: turn cards, tool blocks with the parsed input, and the
    # hljs include. Uses raw frames persisted as :stderr blobs so the legacy
    # re-hydration path is the one producing the rendered events.
    run_id = "webtest/#{Mitos.id()}"
    stage = "webcap"
    path = Path.join([Mitos.run_dir(run_id), "trace", stage <> ".jsonl"])
    File.mkdir_p!(Path.dirname(path))

    # Build lines programmatically so the JSON-in-JSON (a raw pi frame nested
    # as a :stderr `line`) is encoded correctly, not hand-escaped.
    frame = fn m -> Mitos.JSON.encode!(m) end

    line = fn m -> %{"iteration" => 1, "type" => "stderr", "line" => frame.(m)} |> Mitos.JSON.encode!() end

    events = [
      %{"iteration" => 1, "type" => "status", "status" => "session"} |> Mitos.JSON.encode!(),
      %{"iteration" => 1, "type" => "status", "status" => "turn_start"} |> Mitos.JSON.encode!(),
      line.(%{"type" => "tool_execution_start", "toolCallId" => "c1", "toolName" => "bash", "args" => %{"command" => "cat a"}}),
      line.(%{"type" => "tool_execution_end", "toolCallId" => "c1", "toolName" => "bash",
              "result" => %{"content" => [%{"type" => "text", "text" => "hello"}]}, "isError" => false}),
      line.(%{"type" => "turn_end",
              "message" => %{"role" => "assistant", "stopReason" => "stop", "usage" => %{"totalTokens" => 10},
                             "content" => [%{"type" => "text", "text" => "done"}]}})
    ]

    File.write!(path, Enum.join(events, "\n") <> "\n")

    {200, body, _} = get_page("/web/runs/#{run_id}/trace/#{stage}", "operator")

    # one turn card, marked as a transcript turn
    assert body =~ ~s(class="turn-idx">turn 1)
    # the bash tool call recovered from the raw frame, with its parsed command
    assert body =~ "language-bash"
    assert body =~ "cat a"
    assert body =~ ~s(class="tname">bash)
    # the recovered result text
    assert body =~ "hello"
    # hljs is pulled in for this surface
    assert body =~ "highlight.min.js"
  end

  test "one renderer, two cuts: report shows rationale, proof never does (11 §5)" do
    run_id = finished_run()

    {200, report, _} = get_page("/web/runs/#{run_id}/report", "operator")
    # proof is public by URL — no role cookie at all (11 §9)
    {200, proof, _} = get_page("/web/runs/#{run_id}/proof")

    # assert against rendered content, not the shared stylesheet
    proof = proof |> String.split("</style>") |> List.last()
    report = report |> String.split("</style>") |> List.last()

    # both cuts carry the deliverable + totals
    for body <- [report, proof] do
      assert body =~ "note.txt"
      assert body =~ "log hash"
      assert body =~ "work complete"
    end

    # dev cut: rationale + inner trace
    assert report =~ "linear order"
    assert report =~ "trace/webcap"

    # commercial cut: no rationale, no decisions, no inner trace, no harness
    refute proof =~ "linear order"
    refute proof =~ "rationale"
    refute proof =~ "trace/webcap"
    refute proof =~ "harness"
  end

  test "buyer watch link is the commercial cut, public by URL" do
    run_id = finished_run()
    {200, body, _} = get_page("/web/runs/#{run_id}/watch")

    assert body =~ "capability_finished"
    refute body =~ "linear order"
    refute body =~ "advance"
  end

  test "order form → run → redirect to watch (12 §4)" do
    dir = Path.join([Mitos.data_dir(), "fixtures", Mitos.id(), "webapp"])
    File.mkdir_p!(Path.join(dir, "pipelines"))
    cap = Fixtures.capability("former")
    File.cp_r!(cap, Path.join(dir, "former"))
    File.write!(Path.join(dir, "pipelines/main.md"), Fixtures.pipeline(["former"]))

    File.write!(Path.join(dir, "app.yaml"), """
    name: web-app
    version: 0.1.0
    capabilities: [{ ref: "former", as: "former" }]
    pipelines: [{ id: "main", file: "pipelines/main.md" }]
    entrypoints:
      - id: "go"
        pipeline: "main"
        title: "Do the thing"
        workspace: "none"
        parameters: [{ name: "tone", type: "string", default: "warm" }]
    """)

    app = Mitos.App.Manifest.load!(dir)
    Mitos.Catalog.put_app(app)

    {200, form, _} = get_page("/web/apps/#{app.app_id}/entrypoints/go", "operator")
    assert form =~ "Do the thing"
    assert form =~ ~s(name="param_tone")

    conn =
      conn(
        :post,
        "/web/apps/#{app.app_id}/entrypoints/go/run",
        "inputs=%7B%22subject%22%3A%22x%22%7D&param_tone=warm"
      )
      |> put_req_header("content-type", "application/x-www-form-urlencoded")
      |> put_req_header("cookie", "mitos_role=operator")
      |> Router.call(@opts)

    assert conn.status == 303
    [location] = get_resp_header(conn, "location")
    assert location =~ ~r{\A/web/runs/local/}

    run_id = String.replace_prefix(location, "/web/runs/", "")
    state = Run.await(run_id)
    assert state.status == :finished
    assert state.stages["former"].status == :finished
  end

  test "attribution page renders the seller's flat revenue list (11 §7)" do
    cap = Fixtures.capability("attrcap")

    {:ok, capability_id} =
      Mitos.Facilitator.publish(cap, %{price_usd: 2.0, payout_addr: "0xA", seller: "attr_seller"})

    {:ok, q} = Mitos.Facilitator.quote(capability_id)

    payment = Mitos.JSON.encode!(%{quote_id: q.quote_id, amount_usdc: 2.0, payer: "0xB"})
    {:ok, %{run_id: run_id}} = Mitos.Facilitator.purchase(capability_id, payment, %{})
    Run.await(run_id)

    {200, body, _} = get_page("/web/attribution", "seller")
    assert body =~ "attrcap"
    assert body =~ run_id
    assert body =~ "own-key"
    assert body =~ "net revenue"
  end
end
