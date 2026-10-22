/**
 * 双色系统：明暗两套 token，一套语义命名。
 * 界面上所有颜色都必须从这里取，不允许在组件里写十六进制字面量——
 * 否则切主题时必然漏改。
 */
export type ThemeMode = "light" | "dark";

export type Palette = {
  bg: string;
  card: string;
  cardAlt: string;
  border: string;
  text: string;
  textMuted: string;
  textFaint: string;
  accent: string;
  accentSoft: string;
  ok: string;
  warn: string;
  danger: string;
  idle: string;
  overlay: string;
};

const light: Palette = {
  bg: "#F5F7FB",
  card: "#FFFFFF",
  cardAlt: "#EEF2F9",
  border: "#DFE5EF",
  text: "#0F172A",
  textMuted: "#5B6B82",
  textFaint: "#93A1B5",
  accent: "#0284C7",
  accentSoft: "#E0F2FE",
  ok: "#15803D",
  warn: "#B45309",
  danger: "#B91C1C",
  idle: "#94A3B8",
  overlay: "rgba(15,23,42,0.04)",
};

const dark: Palette = {
  bg: "#0B0E14",
  card: "#141924",
  cardAlt: "#1B2130",
  border: "#232B3A",
  text: "#E6EDF7",
  textMuted: "#93A1B5",
  textFaint: "#64748B",
  accent: "#38BDF8",
  accentSoft: "#0F2A3D",
  ok: "#22C55E",
  warn: "#F59E0B",
  danger: "#EF4444",
  idle: "#64748B",
  overlay: "rgba(255,255,255,0.04)",
};

export const palettes: Record<ThemeMode, Palette> = { light, dark };

export const space = { xs: 4, sm: 8, md: 12, lg: 16, xl: 24, xxl: 32 };
export const radius = { sm: 8, md: 12, lg: 16, xl: 22, pill: 999 };
export const font = {
  hero: 44,
  title: 22,
  body: 15,
  small: 13,
  tiny: 11,
};
