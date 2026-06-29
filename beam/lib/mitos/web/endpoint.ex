defmodule Mitos.Web.Endpoint do
  @moduledoc """
  HTTP + WebSocket entry point.

  Cowboy's dispatch list routes the raw `/ws/:session_id` PTY socket straight
  to `Mitos.Web.TerminalSocket` (a `cowboy_websocket` handler, outside any plug
  pipeline — it pipes raw PTY bytes from `Mitos.Session` to the browser with
  minimal copying). Every other request reaches the **Phoenix Endpoint**
  (`MitosWeb.Endpoint`), which:

    * serves the LiveView operations console under `/console` (and redirects
      `/` there),
    * exposes the LiveView websocket at `/live/websocket`,
    * `forward`s everything else to `Mitos.Web.Router` — the JSON/HTTP control
      plane (`/apps`, `/runs`, `/agents`, `/hooks/*`, `/broker/*`, …) consumed
      by the CLI, external HTTP callers, and the smoke tests.

  All existing API paths stay byte-identical; only the SPA at `/` is replaced.
  """

  def child_spec(_opts) do
    # MITOS_HTTP_PORT wins over compile-time config so the dev/prod port can
    # be set without recompiling.
    port =
      case System.get_env("MITOS_HTTP_PORT") do
        nil -> Application.get_env(:mitos, :http_port, 7682)
        value -> String.to_integer(value)
      end

    addr = Application.get_env(:mitos, :http_addr, {127, 0, 0, 1})

    dispatch = [
      {:_,
       [
         {"/ws/:session_id", Mitos.Web.TerminalSocket, %{}},
         {:_, Plug.Cowboy.Handler, {MitosWeb.Endpoint, []}}
       ]}
    ]

    Plug.Cowboy.child_spec(
      scheme: :http,
      plug: MitosWeb.Endpoint,
      options: [
        ip: addr,
        port: port,
        dispatch: dispatch
      ]
    )
  end
end
