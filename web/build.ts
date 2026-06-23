import { compile, compileModule } from "../../sigui/node_modules/svelte/compiler/index.js";
import { mkdirSync, readFileSync, rmSync } from "node:fs";
import * as path from "node:path";

const webDir = import.meta.dir;
const rootDir = path.resolve(webDir, "..");
const distDir = path.join(webDir, "dist");
const srcDir = path.join(webDir, "src");
const assetsDir = path.join(distDir, "assets");
const siguiRoot = path.resolve(rootDir, "../sigui");
const siguiPackagesDir = path.join(siguiRoot, "packages");
const svelteDir = path.join(siguiRoot, "node_modules/svelte");
const tsTranspiler = new Bun.Transpiler({ loader: "ts" });

type ExportMap = Record<string, string | Record<string, string>>;
type ThemeMode = "light" | "dark";
type ThemeContract = {
  name?: string;
  color?: Record<string, string>;
  typography?: {
    fontDisplay?: string;
    fontBody?: string;
    fontMono?: string;
    scale?: Record<string, string>;
    lineHeight?: Record<string, string>;
    tracking?: Record<string, string>;
  };
  spacing?: {
    scale?: Record<string, string>;
    sectionY?: Record<string, string>;
  };
  shape?: {
    radius?: Record<string, string>;
  };
  depth?: Record<string, string>;
  focus?: Record<string, string>;
  motion?: Record<string, string>;
  layout?: {
    containerMax?: string;
    gutter?: Record<string, string>;
  };
};

const packageExportCache = new Map<string, ExportMap>();
const svelteExports = JSON.parse(
  readFileSync(path.join(svelteDir, "package.json"), "utf8")
).exports as ExportMap;
const githubThemePreset = JSON.parse(
  readFileSync(path.join(siguiPackagesDir, "theme/design-systems/github/theme.json"), "utf8")
) as ThemeContract;
const githubDarkThemePreset: ThemeContract = {
  ...githubThemePreset,
  name: "github-dark",
  color: {
    canvas: "#0d1117",
    surface: "#161b22",
    surfaceAlt: "#21262d",
    text: "#f0f6fc",
    textSecondary: "#c9d1d9",
    textMuted: "#8b949e",
    textMeta: "#7d8590",
    border: "#30363d",
    borderSoft: "#21262d",
    accent: "#2f81f7",
    accentText: "#ffffff",
    accentHover: "#58a6ff",
    accentActive: "color-mix(in oklab, var(--sg-theme-accent), white 12%)",
    success: "#3fb950",
    warning: "#d29922",
    danger: "#f85149",
    info: "#58a6ff"
  },
  depth: {
    ...githubThemePreset.depth,
    raised: "0 16px 32px rgba(1, 4, 9, 0.45)"
  },
  focus: {
    ...githubThemePreset.focus,
    ring: "0 0 0 3px rgba(47, 129, 247, 0.35)"
  }
};

const typeSteps = ["2xs", "xs", "sm", "base", "lg", "xl", "2xl", "3xl", "4xl", "5xl", "6xl", "7xl", "8xl", "9xl"] as const;
const fallbackTypeScale = {
  "2xs": "10px",
  xs: "11.25px",
  sm: "12.5px",
  base: "14px",
  lg: "15.75px",
  xl: "17.5px",
  "2xl": "19.75px",
  "3xl": "22px",
  "4xl": "24.75px",
  "5xl": "27.75px",
  "6xl": "31px",
  "7xl": "34.75px",
  "8xl": "38.75px",
  "9xl": "43.5px"
} satisfies Record<(typeof typeSteps)[number], string>;
const fallbackLineHeights = {
  "2xs": "1.5",
  xs: "1.5",
  sm: "1.5",
  base: "1.5",
  lg: "1.475",
  xl: "1.45",
  "2xl": "1.425",
  "3xl": "1.4",
  "4xl": "1.375",
  "5xl": "1.35",
  "6xl": "1.325",
  "7xl": "1.3",
  "8xl": "1.275",
  "9xl": "1.25"
} satisfies Record<(typeof typeSteps)[number], string>;

function readPackageExports(packageName: string): ExportMap {
  if (packageExportCache.has(packageName)) {
    return packageExportCache.get(packageName)!;
  }

  const packageJson = JSON.parse(
    readFileSync(path.join(siguiPackagesDir, packageName, "package.json"), "utf8")
  );
  const exports = (packageJson.exports ?? {}) as ExportMap;
  packageExportCache.set(packageName, exports);
  return exports;
}

function resolveExportEntry(
  exports: ExportMap,
  exportKey: string,
  conditions: string[]
): string | null {
  const directEntry = exports[exportKey];
  const entry = directEntry ?? findWildcardExportEntry(exports, exportKey);
  if (!entry) return null;
  const replacement = Array.isArray(entry) ? entry[1] : null;
  const exportValue = Array.isArray(entry) ? entry[0] : entry;
  if (typeof exportValue === "string") return replacement ? exportValue.replace("*", replacement) : exportValue;

  for (const condition of conditions) {
    const resolved = exportValue[condition];
    if (resolved) return replacement ? resolved.replace("*", replacement) : resolved;
  }

  return null;
}

function findWildcardExportEntry(
  exports: ExportMap,
  exportKey: string
): [string | Record<string, string>, string] | null {
  for (const [pattern, entry] of Object.entries(exports)) {
    if (!pattern.includes("*")) continue;

    const [prefix, suffix] = pattern.split("*");
    if (!prefix || suffix == null) continue;
    if (!exportKey.startsWith(prefix) || !exportKey.endsWith(suffix)) continue;

    return [entry, exportKey.slice(prefix.length, exportKey.length - suffix.length)];
  }

  return null;
}

function resolveSiguiImport(specifier: string): string | null {
  const match = specifier.match(/^@sig-ui\/([^/]+)(\/.*)?$/);
  if (!match) return null;

  const packageName = match[1]!;
  const subpath = match[2];
  const exportKey = subpath ? `.${subpath}` : ".";
  const resolved = resolveExportEntry(readPackageExports(packageName), exportKey, [
    "bun",
    "browser",
    "import",
    "default"
  ]);

  return resolved ? path.join(siguiPackagesDir, packageName, resolved) : null;
}

function resolveSvelteImport(specifier: string): string | null {
  const exportKey = specifier === "svelte" ? "." : `.${specifier.slice("svelte".length)}`;
  const resolved = resolveExportEntry(svelteExports, exportKey, ["browser", "import", "default"]);
  return resolved ? path.join(svelteDir, resolved) : null;
}

function themeForMode(mode: ThemeMode): ThemeContract {
  return mode === "dark" ? githubDarkThemePreset : githubThemePreset;
}

function token(value: string | undefined, fallback: string): string {
  return value && value.trim().length > 0 ? value : fallback;
}

function trackingToken(value: string | undefined): string {
  const next = token(value, "0");
  return next.startsWith("-") ? "0" : next;
}

function cssVariables(mode: ThemeMode) {
  const theme = themeForMode(mode);
  const color = theme.color ?? {};
  const typography = theme.typography ?? {};
  const spacing = theme.spacing ?? {};
  const shape = theme.shape ?? {};
  const depth = theme.depth ?? {};
  const focus = theme.focus ?? {};
  const motion = theme.motion ?? {};
  const layout = theme.layout ?? {};
  const radius = shape.radius ?? {};
  const gutter = layout.gutter ?? {};
  const sectionY = spacing.sectionY ?? {};
  const spacingScale = spacing.scale ?? {};
  const accent = token(color.accent, mode === "light" ? "#0969da" : "#2f81f7");
  const accentHover = token(color.accentHover, mode === "light" ? "#0550ae" : "#58a6ff");
  const success = token(color.success, mode === "light" ? "#1a7f37" : "#3fb950");
  const warning = token(color.warning, mode === "light" ? "#9a6700" : "#d29922");
  const danger = token(color.danger, mode === "light" ? "#cf222e" : "#f85149");
  const info = token(color.info, mode === "light" ? accent : "#58a6ff");
  const interactiveMix = mode === "light" ? "black" : "white";
  const typeLines = typeSteps.map((step) => {
    return `  --sg-theme-text-${step}: ${token(typography.scale?.[step], fallbackTypeScale[step])};`;
  }).join("\n");
  const leadingLines = typeSteps.map((step) => {
    return `  --sg-theme-leading-${step}: ${token(typography.lineHeight?.[step], fallbackLineHeights[step])};`;
  }).join("\n");
  const trackingLines = typeSteps.map((step) => {
    return `  --sg-theme-tracking-${step}: ${trackingToken(typography.tracking?.[step])};`;
  }).join("\n");

  return `
  color-scheme: ${mode};
  --sg-theme-canvas: ${token(color.canvas, mode === "light" ? "#ffffff" : "#0d1117")};
  --sg-theme-surface: ${token(color.surface, mode === "light" ? "#f6f8fa" : "#161b22")};
  --sg-theme-surface-alt: ${token(color.surfaceAlt, mode === "light" ? "var(--sg-theme-surface)" : "#21262d")};
  --sg-theme-text: ${token(color.text, mode === "light" ? "#1f2328" : "#f0f6fc")};
  --sg-theme-text-secondary: ${token(color.textSecondary, mode === "light" ? "#1f2328" : "#c9d1d9")};
  --sg-theme-text-muted: ${token(color.textMuted, mode === "light" ? "#656d76" : "#8b949e")};
  --sg-theme-text-meta: ${token(color.textMeta, mode === "light" ? "#656d76" : "#7d8590")};
  --sg-theme-border: ${token(color.border, mode === "light" ? "#d0d7de" : "#30363d")};
  --sg-theme-border-soft: ${token(color.borderSoft, mode === "light" ? "#d8dee4" : "#21262d")};
  --sg-theme-accent: ${accent};
  --sg-theme-accent-text: ${token(color.accentText, "#ffffff")};
  --sg-theme-accent-hover: ${accentHover};
  --sg-theme-accent-active: ${token(color.accentActive, `color-mix(in oklab, ${accent}, ${interactiveMix} 14%)`)};
  --sg-theme-success: ${success};
  --sg-theme-warning: ${warning};
  --sg-theme-danger: ${danger};
  --sg-theme-info: ${info};
  --sg-theme-font-body: ${token(typography.fontBody, "-apple-system, BlinkMacSystemFont, \"Segoe UI\", \"Noto Sans\", Helvetica, Arial, sans-serif")};
  --sg-theme-font-display: ${token(typography.fontDisplay, "var(--sg-theme-font-body)")};
  --sg-theme-font-mono: ${token(typography.fontMono, "ui-monospace, SFMono-Regular, \"SF Mono\", Menlo, Consolas, \"Liberation Mono\", monospace")};
${typeLines}
${leadingLines}
${trackingLines}
  --sg-theme-tracking-display: ${trackingToken(typography.tracking?.display)};
  --sg-theme-leading-body: ${token(typography.lineHeight?.body, "1.5")};
  --sg-theme-leading-tight: ${token(typography.lineHeight?.tight, "1.25")};
  --sg-theme-space-1: ${token(spacingScale["1"], "4px")};
  --sg-theme-space-2: ${token(spacingScale["2"], "8px")};
  --sg-theme-space-3: ${token(spacingScale["3"], "12px")};
  --sg-theme-space-4: ${token(spacingScale["4"], "16px")};
  --sg-theme-space-5: ${token(spacingScale["5"], "20px")};
  --sg-theme-space-6: ${token(spacingScale["6"], "24px")};
  --sg-theme-space-8: ${token(spacingScale["8"], "32px")};
  --sg-theme-space-12: ${token(spacingScale["12"], "48px")};
  --sg-theme-section-y-desktop: ${token(sectionY.desktop, "64px")};
  --sg-theme-section-y-tablet: ${token(sectionY.tablet, "48px")};
  --sg-theme-section-y-phone: ${token(sectionY.phone, "32px")};
  --sg-theme-radius-sm: ${token(radius.sm, "6px")};
  --sg-theme-radius-md: ${token(radius.md, "6px")};
  --sg-theme-radius-lg: ${token(radius.lg, "12px")};
  --sg-theme-radius-xl: ${token(radius.xl, token(radius.lg, "12px"))};
  --sg-theme-radius-2xl: ${token(radius["2xl"], token(radius.lg, "12px"))};
  --sg-theme-radius-pill: ${token(radius.pill, "9999px")};
  --sg-theme-elev-flat: ${token(depth.flat, "none")};
  --sg-theme-elev-ring: ${token(depth.ring, "0 0 0 1px var(--sg-theme-border)")};
  --sg-theme-elev-raised: ${token(depth.raised, mode === "light" ? "0 1px 3px rgba(31, 35, 40, 0.12), 0 8px 24px rgba(66, 74, 83, 0.12)" : "0 16px 32px rgba(1, 4, 9, 0.45)")};
  --sg-theme-focus-ring: ${token(focus.ring, "0 0 0 3px rgba(9, 105, 218, 0.3)")};
  --sg-theme-focus-color: var(--sg-theme-accent);
  --sg-theme-motion-fast: ${token(motion.fast, "80ms")};
  --sg-theme-motion-base: ${token(motion.base, "200ms")};
  --sg-theme-ease-standard: ${token(motion.easing, "ease-out")};
  --sg-theme-container-max: ${token(layout.containerMax, "1280px")};
  --sg-theme-gutter-desktop: ${token(gutter.desktop, "24px")};
  --sg-theme-gutter-tablet: ${token(gutter.tablet, "16px")};
  --sg-theme-gutter-phone: ${token(gutter.phone, "16px")};
  --sg-bg: var(--sg-theme-canvas);
  --surface-bg-lowest: var(--sg-theme-canvas);
  --surface-bg: var(--sg-theme-canvas);
  --surface-bg-low: var(--sg-theme-surface);
  --surface-bg-secondary: var(--sg-theme-surface);
  --surface-bg-tertiary: var(--sg-theme-surface-alt);
  --surface-bg-highest: var(--sg-theme-surface-alt);
  --sg-color-surface: var(--sg-theme-surface);
  --sg-color-surface-alt: var(--sg-theme-surface-alt);
  --sg-surface-container-low: var(--sg-theme-surface);
  --sg-surface-container: var(--sg-theme-surface);
  --sg-surface-container-high: var(--sg-theme-surface-alt);
  --sg-surface-container-highest: var(--sg-theme-surface-alt);
  --sg-color-code-bg: var(--sg-theme-surface-alt);
  --sg-color-text: var(--sg-theme-text);
  --sg-color-title: var(--sg-theme-text);
  --sg-color-code-text: var(--sg-theme-text);
  --sg-color-text-secondary: var(--sg-theme-text-secondary);
  --sg-color-subtitle: var(--sg-theme-text-secondary);
  --sg-color-text-muted: var(--sg-theme-text-muted);
  --surface-border-default: var(--sg-theme-border);
  --surface-border-strong: var(--sg-theme-border-soft);
  --sg-color-border: var(--sg-theme-border);
  --sg-color-border-light: var(--sg-theme-border-soft);
  --sg-color-primary: var(--sg-theme-accent);
  --sg-color-secondary: var(--sg-theme-text-muted);
  --sg-color-secondary-hover: color-mix(in oklab, var(--sg-theme-text-muted), ${interactiveMix} 8%);
  --sg-color-secondary-active: color-mix(in oklab, var(--sg-theme-text-muted), ${interactiveMix} 14%);
  --sg-color-secondary-subtle: color-mix(in oklab, var(--sg-theme-text-muted), transparent 88%);
  --sg-color-tertiary: var(--sg-theme-info);
  --sg-color-tertiary-hover: color-mix(in oklab, var(--sg-theme-info), ${interactiveMix} 8%);
  --sg-color-tertiary-active: color-mix(in oklab, var(--sg-theme-info), ${interactiveMix} 14%);
  --sg-color-tertiary-subtle: color-mix(in oklab, var(--sg-theme-info), transparent 88%);
  --sg-color-accent: var(--sg-theme-accent);
  --sg-color-link: var(--sg-theme-accent);
  --sg-color-emphasis: var(--sg-theme-accent);
  --sg-color-border-focus: var(--sg-theme-accent);
  --sg-color-primary-subtle: color-mix(in oklab, var(--sg-theme-accent), transparent 88%);
  --sg-color-accent-subtle: color-mix(in oklab, var(--sg-theme-accent), transparent 88%);
  --sg-color-text-inverse: var(--sg-theme-accent-text);
  --sg-color-primary-hover: var(--sg-theme-accent-hover);
  --sg-color-accent-hover: var(--sg-theme-accent-hover);
  --sg-color-primary-active: var(--sg-theme-accent-active);
  --sg-color-accent-active: var(--sg-theme-accent-active);
  --sg-color-success: var(--sg-theme-success);
  --sg-color-success-subtle: color-mix(in oklab, var(--sg-theme-success), transparent 86%);
  --sg-color-warning: var(--sg-theme-warning);
  --sg-color-warning-subtle: color-mix(in oklab, var(--sg-theme-warning), transparent 86%);
  --sg-color-danger: var(--sg-theme-danger);
  --sg-color-danger-subtle: color-mix(in oklab, var(--sg-theme-danger), transparent 86%);
  --sg-color-info: var(--sg-theme-info);
  --sg-color-info-subtle: color-mix(in oklab, var(--sg-theme-info), transparent 86%);
  --sg-font-family: var(--sg-theme-font-body);
  --sg-font-display: var(--sg-theme-font-display);
  --sg-font-mono: var(--sg-theme-font-mono);
  --sg-text-2xs: var(--sg-theme-text-2xs);
  --sg-text-xs: var(--sg-theme-text-xs);
  --sg-text-sm: var(--sg-theme-text-sm);
  --sg-text-base: var(--sg-theme-text-base);
  --sg-text-lg: var(--sg-theme-text-lg);
  --sg-text-xl: var(--sg-theme-text-xl);
  --sg-text-2xl: var(--sg-theme-text-2xl);
  --sg-text-3xl: var(--sg-theme-text-3xl);
  --sg-leading-normal: var(--sg-theme-leading-body);
  --sg-leading-tight: var(--sg-theme-leading-tight);
  --sg-base-unit: var(--sg-theme-space-1);
  --sg-space-1: var(--sg-theme-space-1);
  --sg-space-2: var(--sg-theme-space-2);
  --sg-space-3: var(--sg-theme-space-3);
  --sg-space-4: var(--sg-theme-space-4);
  --sg-space-5: var(--sg-theme-space-5);
  --sg-space-6: var(--sg-theme-space-6);
  --sg-space-8: var(--sg-theme-space-8);
  --sg-space-12: var(--sg-theme-space-12);
  --sg-radius-sm: var(--sg-theme-radius-sm);
  --sg-radius-md: var(--sg-theme-radius-md);
  --sg-radius-lg: var(--sg-theme-radius-lg);
  --sg-radius-xl: var(--sg-theme-radius-xl);
  --sg-radius-2xl: var(--sg-theme-radius-2xl);
  --sg-radius-full: var(--sg-theme-radius-pill);
  --sg-shadow-xs: var(--sg-theme-elev-flat);
  --sg-shadow-sm: var(--sg-theme-elev-ring);
  --sg-shadow-md: var(--sg-theme-elev-raised);
  --sg-shadow-card: var(--sg-theme-elev-raised);
  --sg-focus-ring: var(--sg-theme-focus-ring);
  --sg-focus-color: var(--sg-theme-focus-color);
  --sg-duration-fast: var(--sg-theme-motion-fast);
  --sg-duration-normal: var(--sg-theme-motion-base);
  --sg-ease-default: var(--sg-theme-ease-standard);
  --sg-transition-default: var(--sg-theme-motion-base) var(--sg-theme-ease-standard);
  --sg-grid-max-width: var(--sg-theme-container-max);
  --sg-grid-gutter: var(--sg-theme-gutter-desktop);
  --sg-pad-page: var(--sg-theme-gutter-desktop);
  --sg-button-bg: var(--sg-theme-accent);
  --sg-button-text: var(--sg-theme-accent-text);
  --sg-button-radius: var(--sg-theme-radius-sm);
  --sg-button-padding-y: 7px;
  --sg-button-padding-x: 12px;
  --sg-input-bg: var(--sg-theme-surface);
  --sg-input-border: var(--sg-theme-border);
  --sg-input-border-focus: var(--sg-theme-accent);
  --sg-input-border-invalid: var(--sg-theme-danger);
  --sg-input-padding: 8px 10px;
  --sg-input-radius: var(--sg-theme-radius-sm);
  --sg-card-bg: var(--sg-theme-surface);
  --sg-card-border: 1px solid var(--sg-theme-border);
  --sg-card-radius: var(--sg-theme-radius-lg);
  --sg-card-padding: 16px;
  --sg-card-shadow: none;
`;
}

function themeCss() {
  return `:root, :root[data-theme="light"] {${cssVariables("light")}}
:root[data-theme="dark"] {${cssVariables("dark")}}
`;
}

function expandCssImports(filePath: string, seen = new Set<string>()): string {
  const normalized = path.normalize(filePath);
  if (seen.has(normalized)) return "";
  seen.add(normalized);

  const source = readFileSync(normalized, "utf8");

  return source.replace(/@import\s+["']([^"']+)["'];/g, (_match, specifier) => {
    const nextPath = path.resolve(path.dirname(normalized), specifier);
    return `\n/* ${path.relative(rootDir, nextPath)} */\n${expandCssImports(nextPath, seen)}\n`;
  });
}

async function buildOnce() {
  rmSync(distDir, { recursive: true, force: true });
  mkdirSync(assetsDir, { recursive: true });

  const jsResult = await Bun.build({
    entrypoints: [path.join(srcDir, "main.ts")],
    outdir: distDir,
    target: "browser",
    splitting: false,
    minify: true,
    naming: {
      entry: "assets/app.js",
      chunk: "assets/[name]-[hash].js",
      asset: "assets/[name]-[hash].[ext]"
    },
    plugins: [
      {
        name: "mitos-svelte-sigui",
        setup(build) {
          build.onResolve({ filter: /^@sig-ui\// }, (args) => {
            const resolved = resolveSiguiImport(args.path);
            return resolved ? { path: resolved } : undefined;
          });

          build.onResolve({ filter: /^svelte(\/.*)?$/ }, (args) => {
            const resolved = resolveSvelteImport(args.path);
            return resolved ? { path: resolved } : undefined;
          });

          build.onResolve({ filter: /^esm-env$/ }, () => ({
            path: "esm-env",
            namespace: "mitos-virtual"
          }));

          build.onLoad({ filter: /^esm-env$/, namespace: "mitos-virtual" }, () => ({
            contents: "export const BROWSER = true; export const DEV = false;",
            loader: "js"
          }));

          build.onLoad({ filter: /\.svelte$/ }, async (args) => {
            const source = await Bun.file(args.path).text();
            const compiled = compile(source, {
              filename: args.path,
              generate: "client",
              css: "injected",
              dev: false
            });

            return {
              contents: compiled.js.code,
              loader: "js"
            };
          });

          build.onLoad({ filter: /\.svelte\.[jt]s$/ }, async (args) => {
            const source = await Bun.file(args.path).text();
            const jsSource = args.path.endsWith(".ts")
              ? tsTranspiler.transformSync(source)
              : source;
            const compiled = compileModule(jsSource, {
              filename: args.path,
              generate: "client",
              dev: false
            });

            return {
              contents: compiled.js.code,
              loader: "js"
            };
          });
        }
      }
    ]
  });

  if (!jsResult.success) {
    for (const log of jsResult.logs) {
      console.error(log);
    }
    throw new Error("Svelte build failed");
  }

  await Bun.write(
    path.join(assetsDir, "sigui.css"),
    expandCssImports(path.join(siguiPackagesDir, "components/src/styles/sigui.css"))
  );
  const appCss = themeCss() + "\n" + (await Bun.file(path.join(srcDir, "styles.css")).text());
  await Bun.write(path.join(assetsDir, "app.css"), appCss);
  await Bun.write(
    path.join(distDir, "index.html"),
    `<!doctype html>
<html lang="en" data-theme="light">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta name="color-scheme" content="light dark" />
    <title>Mitos Operations Console</title>
    <link rel="stylesheet" href="/assets/sigui.css" />
    <link rel="stylesheet" href="/assets/app.css" />
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/assets/app.js"></script>
  </body>
</html>
`
  );
}

await buildOnce();
console.log(`Built Svelte app in ${path.relative(rootDir, distDir)}`);

if (process.argv.includes("--watch")) {
  console.log("Watching web/src for changes...");
  const watcher = require("node:fs").watch(srcDir, { recursive: true }, async () => {
    try {
      await buildOnce();
      console.log(`Rebuilt at ${new Date().toLocaleTimeString()}`);
    } catch (error) {
      console.error(error);
    }
  });

  process.on("SIGINT", () => {
    watcher.close();
    process.exit(0);
  });
}
