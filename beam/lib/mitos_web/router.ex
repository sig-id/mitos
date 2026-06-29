defmodule MitosWeb.Router do
  @moduledoc """
  Browser routes for the operations console.

  The LiveView console lives under `/console/*`; the root `/` redirects there.
  Every other path — the JSON control plane (`/apps`, `/runs`, `/agents`,
  `/hooks/*`, `/broker/*`, …) — is forwarded to the existing `Mitos.Web.Router`
  unchanged.
  """
  use MitosWeb, :router

  pipeline :browser do
    plug :accepts, ["html"]
    plug :fetch_session
    plug :fetch_live_flash
    plug :put_root_layout, html: {MitosWeb.Layouts, :root}
    plug :protect_from_forgery
  end

  scope "/", MitosWeb do
    pipe_through :browser

    get "/", ConsoleRedirect, :show
  end

  scope "/console", MitosWeb.ConsoleLive do
    pipe_through :browser

    live_session :console, root_layout: {MitosWeb.Layouts, :root} do
      live "/", Dashboard, :index
      live "/applications", Applications, :index
      live "/applications/:id", AppDetail, :index
      live "/connectors", Connectors, :index
      live "/runs", Runs, :index
      live "/runs/:id", RunDetail, :index
    end
  end

  # Everything unmatched (the JSON control plane) reaches the existing API
  # router. Declared last so LiveView routes win when paths collide.
  forward "/", Mitos.Web.Router
end

defmodule MitosWeb.ConsoleRedirect do
  @moduledoc false
  use Phoenix.Controller, formats: [html: MitosWeb.ErrorHTML]
  def show(conn, _params) do
    redirect(conn, to: "/console")
  end
end
