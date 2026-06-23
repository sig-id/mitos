import { resolveTheme } from "@sig-ui/theme";
import githubTheme from "@sig-ui/theme/design-systems/github/theme.json";

export type ThemeMode = "light" | "dark";

type ThemePreset = typeof githubTheme & {
  color: Record<string, string>;
};

const githubDarkTheme: ThemePreset = {
  ...githubTheme,
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
    ...githubTheme.depth,
    raised: "0 16px 32px rgba(1, 4, 9, 0.45)"
  },
  focus: {
    ...githubTheme.focus,
    ring: "0 0 0 3px rgba(47, 129, 247, 0.35)"
  }
};

export const themePresets: Record<ThemeMode, ThemePreset> = {
  light: githubTheme as ThemePreset,
  dark: githubDarkTheme
};

export const themeContract = resolveTheme({
  theme: githubTheme,
  appearance: "auto",
  density: "compact",
  material: "solid"
});

export function themeSwatches(mode: ThemeMode) {
  const colors = themePresets[mode].color;

  return [
    { name: "Canvas", value: colors.canvas },
    { name: "Surface", value: colors.surface },
    { name: "Border", value: colors.border },
    { name: "Accent", value: colors.accent },
    { name: "Success", value: colors.success },
    { name: "Warning", value: colors.warning },
    { name: "Danger", value: colors.danger }
  ];
}
