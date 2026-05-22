defmodule Mitos.Adapter.RegistryTest do
  use ExUnit.Case, async: true

  alias Mitos.Adapter.{Claude, Codex, Fake, Gemini, Generic, Registry}

  describe "select/2" do
    test "follows the requested order and resolves the pty fallback" do
      # Config orders list headless modes first; this runtime only implements
      # :pty, so it's chosen as the fallback — never an error.
      assert {:ok, Claude, :pty} = Registry.select("claude", [:headless_json, :pty])
      assert {:ok, Codex, :pty} = Registry.select("codex", [:exec_json, :pty])
      assert {:ok, Gemini, :pty} = Registry.select("gemini", [:headless_json, :pty])
    end

    test "resolves each registered provider under the default order" do
      assert {:ok, Fake, :pty} = Registry.select("fake")
      assert {:ok, Claude, :pty} = Registry.select("claude")
      assert {:ok, Codex, :pty} = Registry.select("codex")
      assert {:ok, Gemini, :pty} = Registry.select("gemini")
      assert {:ok, Generic, :pty} = Registry.select("generic")
      assert {:ok, Generic, :pty} = Registry.select("generic-pty")
    end

    test "unknown provider is not_registered" do
      assert {:error, :provider_not_registered} = Registry.select("nope", [:pty])
    end

    test "known provider with no matching mode is unavailable" do
      assert {:error, :adapter_unavailable} = Registry.select("claude", [:headless_json])
      assert {:error, :adapter_unavailable} = Registry.select("claude", [])
    end
  end

  test "providers/0 lists every registered provider id" do
    assert Enum.sort(Registry.providers()) ==
             ~w(claude codex fake gemini generic generic-pty)
  end
end
