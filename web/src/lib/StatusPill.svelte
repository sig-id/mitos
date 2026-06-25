<script lang="ts">
  let { status = "idle" }: { status?: string } = $props();
  let label = $derived(
    status === "running"
      ? "Live"
      : status === "idle"
        ? "Idle"
        : status === "done" || status === "completed"
          ? "Done"
          : status === "error" || status === "failed"
            ? "Error"
            : status === "parked"
              ? "Parked"
              : (status || "idle")
  );
</script>

<span class="s s-{status || 'idle'}">
  <span class="pulse"></span>
  <span class="t">{label}</span>
</span>

<style>
  .s {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    font-weight: 500;
    padding: 3px 8px;
    border-radius: 999px;
    border: 1px solid transparent;
    white-space: nowrap;
  }
  .pulse {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    flex: none;
  }
  .s-running {
    color: var(--mt-green);
    background: var(--mt-green-soft);
  }
  .s-running .pulse {
    background: var(--mt-green);
    animation: mt-pulse 1.8s infinite;
  }
  .s-completed,
  .s-done {
    color: var(--mt-blue);
    background: var(--mt-blue-soft);
  }
  .s-completed .pulse,
  .s-done .pulse {
    background: var(--mt-blue);
  }
  .s-idle {
    color: var(--mt-text-muted);
    background: rgba(255, 255, 255, 0.03);
  }
  .s-idle .pulse {
    background: var(--mt-text-muted);
  }
  .s-parked {
    color: var(--mt-yellow);
    background: var(--mt-yellow-soft);
  }
  .s-parked .pulse {
    background: var(--mt-yellow);
  }
  .s-error,
  .s-failed {
    color: var(--mt-red);
    background: var(--mt-red-soft);
  }
  .s-error .pulse,
  .s-failed .pulse {
    background: var(--mt-red);
  }
  @keyframes mt-pulse {
    0% {
      box-shadow: 0 0 0 0 color-mix(in srgb, var(--mt-green) 60%, transparent);
    }
    70% {
      box-shadow: 0 0 0 6px transparent;
    }
    100% {
      box-shadow: 0 0 0 0 transparent;
    }
  }
</style>
