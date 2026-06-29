defmodule MitosWeb do
  @moduledoc """
  The entry point for web modules — pulls in the controller/live_view/router
  plumbing the Phoenix console needs.
  """
  def html do
    quote do
      use Phoenix.Component
      alias MitosWeb.Router.Helpers, as: Routes
      alias Phoenix.LiveView.JS
      unquote(verified_routes())
    end
  end

  def controller do
    quote do
      use Phoenix.Controller

      import Plug.Conn
      alias MitosWeb.Router.Helpers, as: Routes
      unquote(verified_routes())
    end
  end

  def live_view do
    quote do
      use Phoenix.LiveView,
        layout: {MitosWeb.Layouts, :app}

      unquote(html_helpers())
    end
  end

  def live_component do
    quote do
      use Phoenix.LiveComponent

      unquote(html_helpers())
    end
  end

  def router do
    quote do
      use Phoenix.Router, helpers: true

      import Phoenix.Controller
      import Phoenix.LiveView.Router
    end
  end

  defp html_helpers do
    quote do
      use Phoenix.Component

      import Phoenix.Component,
        except: [link: 1, link: 2]

      alias MitosWeb.Router.Helpers, as: Routes
      alias Phoenix.LiveView.JS
      unquote(verified_routes())
    end
  end

  defp verified_routes do
    quote do
      use Phoenix.VerifiedRoutes,
        endpoint: MitosWeb.Endpoint,
        router: MitosWeb.Router,
        statics: ~w(assets)
    end
  end

  @doc """
  When used, dispatch to the appropriate controller/live_view/etc. macro.
  """
  defmacro __using__(which) when is_atom(which) do
    apply(__MODULE__, which, [])
  end
end
