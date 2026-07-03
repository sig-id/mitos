defmodule Mitos.Hermetic do
  @moduledoc """
  Hermetic subprocess environments (05 §6): a sandboxed process carries ONLY
  what mitos injected. `env/1` clears the entire inherited OS environment
  (ambient credentials, host git/gpg config hooks, cloud SDK vars) and sets
  exactly the given overrides — the local-provider realization of "ambient
  credentials: not present".

  Git gets its own base: identity fixed, host config surfaces
  (`~/.gitconfig`, system config, `GIT_*` env) neutralized, signing off — a
  sandbox commit must never consult host keys.
  """

  @doc """
  Build a `System.cmd` env list: every inherited variable unset (nil), then
  the overrides. Later entries win, so overrides survive the clear.
  """
  def env(%{} = overrides) do
    cleared =
      for {k, _} <- System.get_env(), not Map.has_key?(overrides, k), do: {k, nil}

    cleared ++ Map.to_list(overrides)
  end

  @doc "Non-secret git base env for in-sandbox git operations."
  def git_env(home) do
    %{
      "HOME" => home,
      "PATH" => path(),
      "GIT_AUTHOR_NAME" => "mitos",
      "GIT_AUTHOR_EMAIL" => "run@mitos.local",
      "GIT_COMMITTER_NAME" => "mitos",
      "GIT_COMMITTER_EMAIL" => "run@mitos.local",
      "GIT_CONFIG_GLOBAL" => "/dev/null",
      "GIT_CONFIG_SYSTEM" => "/dev/null",
      "GIT_CONFIG_NOSYSTEM" => "1"
    }
  end

  @doc "PATH is non-secret config (05 §5.1) — inherited so injected tools resolve."
  def path, do: System.get_env("PATH", "/usr/local/bin:/usr/bin:/bin")
end
