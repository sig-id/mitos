defmodule Mitos.WebappTest do
  use ExUnit.Case, async: false

  import Plug.Test

  setup do
    tmp = Path.join(System.tmp_dir!(), "mitos-webapp-#{System.unique_integer([:positive])}")
    File.mkdir_p!(Path.join(tmp, "assets"))
    File.write!(Path.join(tmp, "index.html"), "<html>console</html>")
    File.write!(Path.join([tmp, "assets", "app.js"]), "// js")
    File.write!(Path.join(tmp, "secret.txt"), "not an asset")
    previous = Application.get_env(:mitos, :webapp_dir)
    Application.put_env(:mitos, :webapp_dir, tmp)

    on_exit(fn ->
      if previous do
        Application.put_env(:mitos, :webapp_dir, previous)
      else
        Application.delete_env(:mitos, :webapp_dir)
      end

      File.rm_rf!(tmp)
    end)

    {:ok, webapp_dir: tmp}
  end

  test "GET / serves the operations console index with no-cache" do
    conn = conn(:get, "/") |> Mitos.Web.Router.call([])

    assert conn.status == 200
    assert conn.resp_body == "<html>console</html>"
    assert Plug.Conn.get_resp_header(conn, "cache-control") == ["no-cache"]
  end

  test "GET /assets/:file serves asset files with their MIME type" do
    conn = conn(:get, "/assets/app.js") |> Mitos.Web.Router.call([])

    assert conn.status == 200
    assert conn.resp_body == "// js"
    assert [content_type] = Plug.Conn.get_resp_header(conn, "content-type")
    assert content_type =~ "javascript"
  end

  test "missing and traversing asset paths 404" do
    assert (conn(:get, "/assets/nope.js") |> Mitos.Web.Router.call([])).status == 404
    assert (conn(:get, "/assets/..%2Fsecret.txt") |> Mitos.Web.Router.call([])).status == 404
  end

  test "symlinked asset files 404", %{webapp_dir: webapp_dir} do
    outside = unique_tmp_dir("webapp-symlink-file")
    File.mkdir_p!(outside)
    File.write!(Path.join(outside, "secret.js"), "secret asset")
    File.rm!(Path.join([webapp_dir, "assets", "app.js"]))
    File.ln_s!(Path.join(outside, "secret.js"), Path.join([webapp_dir, "assets", "app.js"]))

    on_exit(fn -> File.rm_rf!(outside) end)

    conn = conn(:get, "/assets/app.js") |> Mitos.Web.Router.call([])

    assert conn.status == 404
    refute conn.resp_body =~ "secret asset"
  end

  test "symlinked asset directories 404", %{webapp_dir: webapp_dir} do
    outside = unique_tmp_dir("webapp-symlink-dir")
    File.mkdir_p!(Path.join(outside, "assets"))
    File.write!(Path.join([outside, "assets", "app.js"]), "secret asset")
    File.rm_rf!(Path.join(webapp_dir, "assets"))
    File.ln_s!(Path.join(outside, "assets"), Path.join(webapp_dir, "assets"))

    on_exit(fn -> File.rm_rf!(outside) end)

    conn = conn(:get, "/assets/app.js") |> Mitos.Web.Router.call([])

    assert conn.status == 404
    refute conn.resp_body =~ "secret asset"
  end

  test "symlinked webapp roots 404" do
    outside = unique_tmp_dir("webapp-symlink-root-target")
    link = unique_tmp_dir("webapp-symlink-root")
    File.mkdir_p!(outside)
    File.write!(Path.join(outside, "index.html"), "secret index")
    File.ln_s!(outside, link)
    Application.put_env(:mitos, :webapp_dir, link)

    on_exit(fn ->
      File.rm(link)
      File.rm_rf!(outside)
    end)

    conn = conn(:get, "/") |> Mitos.Web.Router.call([])

    assert conn.status == 404
    refute conn.resp_body =~ "secret index"
  end

  defp unique_tmp_dir(prefix) do
    Path.join(System.tmp_dir!(), "mitos-#{prefix}-#{System.unique_integer([:positive])}")
  end
end
