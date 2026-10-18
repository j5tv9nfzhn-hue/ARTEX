export const THEME_MODE_OPTIONS = [
  { label: "浅色", value: "light" },
  { label: "深色", value: "dark" },
  { label: "跟随系统", value: "system" },
] as const;

export const THEME_MODE_VALUES = THEME_MODE_OPTIONS.map((o) => o.value);
export type ThemeMode = (typeof THEME_MODE_VALUES)[number];
export type ResolvedThemeMode = "light" | "dark";

// --- generated:themePresets:start ---

export const THEME_PRESET_OPTIONS = [
  {
    label: "默认",
    value: "default",
    primary: {
      light: "oklch(0.52 0.175 248)",
      dark: "oklch(0.58 0.185 248)",
    },
  },
  {
    label: "琥珀终端",
    value: "amber-crt",
    primary: {
      light: "oklch(0.55 0.165 62)",
      dark: "oklch(0.6 0.17 64)",
    },
  },
  {
    label: "粗野主义",
    value: "brutalist",
    primary: {
      light: "oklch(0.58 0.225 26.5)",
      dark: "oklch(0.62 0.215 25)",
    },
  },
  {
    label: "深空指挥室",
    value: "command",
    primary: {
      light: "oklch(0.52 0.175 248)",
      dark: "oklch(0.58 0.185 248)",
    },
  },
  {
    label: "红队利刃",
    value: "crimson-ops",
    primary: {
      light: "oklch(0.54 0.21 18)",
      dark: "oklch(0.58 0.21 18)",
    },
  },
  {
    label: "量子脉冲",
    value: "cyber-violet",
    primary: {
      light: "oklch(0.52 0.21 286)",
      dark: "oklch(0.58 0.21 286)",
    },
  },
  {
    label: "矩阵绿",
    value: "matrix",
    primary: {
      light: "oklch(0.52 0.165 152)",
      dark: "oklch(0.58 0.175 152)",
    },
  },
  {
    label: "柔和流行",
    value: "soft-pop",
    primary: {
      light: "oklch(0.52 0.22 276)",
      dark: "oklch(0.6 0.195 276)",
    },
  },
  {
    label: "柑橘",
    value: "tangerine",
    primary: {
      light: "oklch(0.6 0.185 38)",
      dark: "oklch(0.62 0.185 38)",
    },
  },
] as const;

export const THEME_PRESET_VALUES = THEME_PRESET_OPTIONS.map((p) => p.value);

export type ThemePreset = (typeof THEME_PRESET_OPTIONS)[number]["value"];

// --- generated:themePresets:end ---
