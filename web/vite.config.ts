import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// sig-ui lives in a sibling checkout (`../../sigui`) and ships source JS (no
// build step). npm installs it via `file:` deps as symlinks into that
// checkout, so Vite resolves `@sig-ui/*` (including subpath `exports`) through
// node_modules normally. `svelte` and `@sveltejs/vite-plugin-svelte` come from
// npm (matching sig-ui's version).
export default defineConfig({
  plugins: [svelte()],
  build: {
    outDir: "dist",
    emptyOutDir: true
  },
  server: {
    proxy: {
      // The BEAM control plane serves the API + this static bundle from one
      // origin in production. In dev, proxy API calls to the running server.
      "/apps": "http://127.0.0.1:7682",
      "/runs": "http://127.0.0.1:7682",
      "/work-pool": "http://127.0.0.1:7682",
      "/io": "http://127.0.0.1:7682",
      "/hooks": "http://127.0.0.1:7682",
      "/sessions": "http://127.0.0.1:7682",
      "/healthz": "http://127.0.0.1:7682"
    }
  }
});
