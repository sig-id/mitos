defmodule Mitos.Web.Endpoint do
  @moduledoc """
  HTTP + WebSocket entry point. Uses Plug.Cowboy with a custom dispatch list
  so the `/ws/:session_id` route reaches `Mitos.Web.TerminalSocket` (a
  cowboy_websocket handler) directly, while every other request is served by
  `Mitos.Web.Router`.

  The split lets the WS handler own state with minimal copying: it pipes raw
  PTY bytes straight from `Mitos.Session` (received via `Mitos.PubSub`) to
  the browser without any plug pipeline in between.
  """

  def child_spec(_opts) do
    # MITOS_HTTP_PORT wins over compile-time config so the dev/prod port can
    # be set without recompiling. Default 7682 avoids colliding with the
    # legacy `mitos webpty` Zig server on 7681 while both stacks coexist.
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
         {:_, Plug.Cowboy.Handler, {Mitos.Web.Router, []}}
       ]}
    ]

    Plug.Cowboy.child_spec(
      scheme: :http,
      plug: Mitos.Web.Router,
      options: [
        ip: addr,
        port: port,
        dispatch: dispatch
      ]
    )
  end
end
