defmodule MitosWeb.Catalog do
  @moduledoc """
  Static catalog data ported from the Svelte SPA's `lib/api.ts` — the harness
  list and the connector/sink brand maps. v1 mirrors the prototype's set.
  """

  def harnesses do
    [
      %{"id" => "claude", "name" => "Claude Code", "vendor" => "Anthropic", "icon" => "✶", "color" => "#d97757"},
      %{"id" => "codex", "name" => "Codex", "vendor" => "OpenAI", "icon" => "⬡", "color" => "#10a37f"},
      %{"id" => "gemini", "name" => "Gemini CLI", "vendor" => "Google", "icon" => "◆", "color" => "#4285f4"},
      %{"id" => "cursor", "name" => "Cursor Agent", "vendor" => "Cursor", "icon" => "◢", "color" => "#a855f7"},
      %{"id" => "local", "name" => "Local Runtime", "vendor" => "Self-hosted", "icon" => "◇", "color" => "#6b6b78"},
      %{"id" => "opencode", "name" => "OpenCode", "vendor" => "SST", "icon" => "◈", "color" => "#5b6cff"}
    ]
  end

  def harness(id) when is_binary(id) do
    Enum.find(harnesses(), &(&1["id"] == id))
  end

  def harness(_), do: nil

  @connector_colors %{
    "github" => "#e6edf3",
    "slack" => "#e01e5a",
    "discord" => "#5865f2",
    "email" => "#fbbf24",
    "linear" => "#5e6ad2",
    "gdrive" => "#1fa463",
    "webhook" => "#6b7280",
    "jira" => "#2684ff",
    "manual" => "#8b949e",
    "web" => "#8b949e",
    "chat" => "#e01e5a",
    "tracker" => "#e6edf3"
  }

  @connector_glyphs %{
    "github" => "◈",
    "slack" => "✦",
    "discord" => "✲",
    "email" => "✉",
    "linear" => "▸",
    "gdrive" => "▽",
    "webhook" => "⇄",
    "jira" => "◼",
    "manual" => "✎",
    "web" => "🌐",
    "chat" => "✦",
    "tracker" => "◈"
  }

  def connector_color(nil), do: "#9aa"
  def connector_color(id), do: Map.get(@connector_colors, id, "#9aa")

  def connector_glyph(nil), do: "•"
  def connector_glyph(id), do: Map.get(@connector_glyphs, id, "•")

  @connector_desc %{
    "webhook" => "Inbound HTTP POST — generic ingress for any external system.",
    "manual" => "Operator UI / CLI / direct API call.",
    "web" => "The web app surface, as an input.",
    "chat" => "Slack / Discord messages, slash-commands, mentions.",
    "email" => "Inbound messages to an address.",
    "tracker" => "GitHub / Linear / kanban issues and cards."
  }

  def describe_connector(name) do
    Map.get(@connector_desc, name, "Platform ingress connector.")
  end

  @sink_desc %{
    "web" => "The web app — live trace + receipt card. No network call.",
    "deliver" => "POST the run's result and artifacts to a webhook URL.",
    "notify" => "A human-facing 'your job is done' message.",
    "reply" => "Round-trip the result back to the originating connector."
  }

  def describe_sink(name) do
    Map.get(@sink_desc, name, "Platform egress sink.")
  end
end
