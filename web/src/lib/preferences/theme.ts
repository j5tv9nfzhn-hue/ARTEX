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
      light: "oklch(0.205 0 0)",
      dark: "oklch(0.922 0 0)",
    },
  },
  {
    label: "琥珀终端",
    value: "amber-crt",
    primary: {
      light: "oklch(0.58 0.14 58)",
      dark: "oklch(0.79 0.17 74)",
    },
  },
  {
    label: "粗野主义",
    value: "brutalist",
    primary: {
      light: "oklch(0.6489 0.237 26.9728)",
      dark: "oklch(0.7044 0.1872 23.1858)",
    },
  },
  {
    label: "深空指挥室",
    value: "command",
    primary: {
      light: "oklch(0.52 0.105 223.13)",
      dark: "oklch(0.79 0.15 211.5)",
    },
  },
  {
    label: "矩阵绿",
    value: "matrix",
    primary: {
      light: "oklch(0.52 0.15 152)",
      dark: "oklch(0.84 0.25 148)",
    },
  },
  {
    label: "柔和流行",
    value: "soft-pop",
    primary: {
      light: "oklch(0.5106 0.2301 276.9656)",
      dark: "oklch(0.6801 0.1583 276.9349)",
    },
  },
  {
    label: "柑橘",
    value: "tangerine",
    primary: {
      light: "oklch(0.64 0.17 36.44)",
      dark: "oklch(0.64 0.17 36.44)",
    },
  },
] as const;

export const THEME_PRESET_VALUES = THEME_PRESET_OPTIONS.map((p) => p.value);

export type ThemePreset = (typeof THEME_PRESET_OPTIONS)[number]["value"];

// --- generated:themePresets:end ---
