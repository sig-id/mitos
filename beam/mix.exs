defmodule Mitos.MixProject do
  use Mix.Project

  def project do
    [
      app: :mitos,
      version: "0.1.0",
      elixir: "~> 1.16",
      elixirc_paths: elixirc_paths(Mix.env()),
      start_permanent: Mix.env() == :prod,
      deps: deps(),
      escript: [main_module: Mitos.CLI, name: "mitos", app: nil]
    ]
  end

  def application do
    [
      extra_applications: [:logger, :inets, :ssl, :crypto],
      mod: {Mitos.Application, []}
    ]
  end

  defp elixirc_paths(:test), do: ["lib", "test/support"]
  defp elixirc_paths(_), do: ["lib"]

  defp deps do
    [
      {:yaml_elixir, "~> 2.11"},
      {:plug, "~> 1.16"},
      {:bandit, "~> 1.5"}
    ]
  end
end
