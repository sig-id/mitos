defmodule Mitos.MixProject do
  use Mix.Project

  def project do
    [
      app: :mitos,
      version: "0.1.0",
      elixir: "~> 1.17",
      elixirc_paths: elixirc_paths(Mix.env()),
      deps: deps(),
      start_permanent: Mix.env() == :prod
    ]
  end

  def application do
    [
      extra_applications: [:logger],
      mod: {Mitos.Application, []}
    ]
  end

  defp elixirc_paths(:test), do: ["lib", "test/support"]
  defp elixirc_paths(_), do: ["lib"]

  defp deps do
    [
      # Pure-Erlang MessagePack codec; matches the sidecar's on-the-wire format.
      {:msgpax, "~> 2.4"},
      # HTTP + WebSocket via cowboy. The `/ws/:session_id` raw PTY socket is
      # dispatched straight to a cowboy_websocket handler (see Endpoint);
      # everything else reaches the Phoenix Endpoint + LiveView console.
      {:plug_cowboy, "~> 2.8"},
      {:jason, "~> 1.4"},

      # Phoenix LiveView drives the operations console (`/console`). LiveViews
      # call the same backend ops (`Mitos.*`) the CLI/HTTP API do — no HTTP loop.
      {:phoenix, "~> 1.8"},
      {:phoenix_live_view, "~> 1.2"},
      {:phoenix_html, "~> 4.1"},
      {:phoenix_pubsub, "~> 2.1"},
      {:phoenix_template, "~> 1.0"},
      {:floki, "~> 0.36", only: :test},
      {:lazy_html, ">= 0.1.0", only: :test}
    ]
  end
end
