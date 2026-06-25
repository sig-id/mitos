// Minimal hash router as a Svelte 5 rune-based store.

export function createRoute() {
  let segments = $state<string[]>(parse());

  function parse(): string[] {
    return location.hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  }

  window.addEventListener("hashchange", () => {
    segments = parse();
  });

  return {
    get value() {
      return segments;
    }
  };
}

export function go(path: string): void {
  const clean = path.replace(/^#?\/?/, "");
  location.hash = "/" + clean;
}
