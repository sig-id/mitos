<script lang="ts">
  // Connector glyph chip. The platform registers a small set of connectors
  // today (webhook/manual/web); chat/tracker connectors (slack, github, …) are
  // shown with their canonical brand glyphs from a local map so the UI reads
  // correctly even before those connector classes ship.

  let {
    id,
    size = 16,
    label = ""
  }: { id?: string; size?: number; label?: string } = $props();

  const colors: Record<string, string> = {
    github: "#e6edf3",
    slack: "#e01e5a",
    discord: "#5865f2",
    email: "#fbbf24",
    linear: "#5e6ad2",
    gdrive: "#1fa463",
    webhook: "#6b7280",
    jira: "#2684ff",
    manual: "#8b949e",
    web: "#8b949e",
    chat: "#e01e5a",
    tracker: "#e6edf3"
  };

  const glyphs: Record<string, string> = {
    github: "◈",
    slack: "✦",
    discord: "✲",
    email: "✉",
    linear: "▸",
    gdrive: "▽",
    webhook: "⇄",
    jira: "◼",
    manual: "✎",
    web: "🌐",
    chat: "✦",
    tracker: "◈"
  };

  let c = $derived(id ? colors[id] || "#9aa" : "#9aa");
  let glyph = $derived(id ? glyphs[id] || "•" : "•");
</script>

<span
  class="connector"
  style="--c:{c}; --s:{size}px"
  title={label || id || ""}
  aria-label={label || id || ""}
>
  <span class="g">{glyph}</span>
</span>

<style>
  .connector {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: var(--s);
    height: var(--s);
    border-radius: 5px;
    background: color-mix(in srgb, var(--c) 16%, transparent);
    color: var(--c);
    font-size: calc(var(--s) * 0.6);
    line-height: 1;
    flex: none;
  }
  .g {
    transform: translateY(-0.5px);
  }
</style>
