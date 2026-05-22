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
      # HTTP + WebSocket via cowboy. We're not pulling Phoenix yet — the design
      # calls for Channels/LiveView, but the surface area we need (one HTTP
      # route + one WS handler bridged to a Session GenServer) is small enough
      # that Plug.Cowboy keeps the BEAM-side concept count low. Migration to
      # Phoenix is mechanical when the orchestration surface grows.
      {:plug_cowboy, "~> 2.8"},
      {:jason, "~> 1.4"}
    ]
  end
end
