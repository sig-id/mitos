defmodule MitosWeb.ConsoleLiveTest do
  use MitosWeb.ConnCase, async: true

  import Phoenix.LiveViewTest

  test "the console shell renders and root redirects to /console", %{conn: conn} do
    conn = get(conn, "/")
    assert redirected_to(conn, 302) =~ "/console"
  end

  test "/console renders the dashboard with sidebar + nav", %{conn: conn} do
    {:ok, view, html} = live(conn, "/console")

    assert html =~ "Mitos"
    assert html =~ "Control plane"
    assert html =~ "Run agent"
    assert html =~ "Dashboard"
    assert html =~ "Applications"
    assert has_element?(view, "aside.side")
    # sidebar nav is wired
    assert has_element?(view, "button[phx-click='nav-applications']")
    assert has_element?(view, "button[phx-click='nav-runs']")
    assert has_element?(view, "button[phx-click='open-launch']")
  end

  test "/console/applications lists installed apps and shows the add form", %{conn: conn} do
    {:ok, view, html} = live(conn, "/console/applications")

    assert html =~ "Applications"
    assert has_element?(view, "input[name='source']")
    # at least one bundled app is visible
    assert html =~ "auto-code-review"
    assert html =~ "kanban-task-worker"
  end

  test "the existing JSON API is forwarded unchanged behind the LiveView", %{conn: conn} do
    conn = get(conn, "/apps")
    body = json_response(conn, 200)
    assert is_list(body["apps"])

    conn = get(conn, "/io/connectors")
    assert is_list(json_response(conn, 200)["connectors"])
  end

  test "an app detail page renders its sections", %{conn: conn} do
    id = hd(Mitos.Applications.list())["id"]
    {:ok, _view, html} = live(conn, "/console/applications/#{id}")

    assert html =~ "Overview"
    assert html =~ "I/O bindings"
    assert html =~ "Harnesses"
    assert html =~ "Entrypoints"
  end
end
