defmodule MitosWeb.ConnCase do
  @moduledoc """
  Shared case for testing the Phoenix console: Plug.Conn-level requests against
  `MitosWeb.Endpoint` plus LiveView test helpers.
  """
  use ExUnit.CaseTemplate

  using do
    quote do
      use ExUnit.Case

      import Plug.Conn
      import Phoenix.ConnTest
      import Phoenix.LiveViewTest

      alias MitosWeb.Router.Helpers, as: Routes

      @endpoint MitosWeb.Endpoint
    end
  end

  setup tags do
    # Tests hit the endpoint via Plug.Conn; no live HTTP port is needed. The
    # application supervision tree (started by ExUnit) provides the backend ops.
    {:ok, conn: Phoenix.ConnTest.build_conn()}
  end
end
