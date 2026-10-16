// 自托管字体(离线构建可用):全部通过 next/font/local 引用本地文件,不依赖构建期联网。
// 用途分层:
// - geist:UI 主字体(沿用 node_modules/geist 依赖内的可变字体文件)。
// - spaceGrotesk:展示/标题位(Google Fonts 官方仓库 OFL 许可,可变字重 300-700)。
// - jetBrainsMono:数据/终端/代码位(JetBrains 官方 OFL 许可,可变字重 100-800)。
import localFont from "next/font/local";

const geist = localFont({
  src: "../../../node_modules/geist/dist/fonts/geist-sans/Geist-Variable.woff2",
  variable: "--font-geist",
});

// wght 轴范围已通过脚本解析 fvar 表确认:300-700。
const spaceGrotesk = localFont({
  src: "./files/space-grotesk-var.ttf",
  variable: "--font-space-grotesk",
  weight: "300 700",
});

// wght 轴范围已通过脚本解析 fvar 表确认:100-800。
const jetBrainsMono = localFont({
  src: "./files/jetbrains-mono-var.ttf",
  variable: "--font-jetbrains-mono",
  weight: "100 800",
});

export const fontRegistry = {
  geist: { label: "Geist", font: geist },
  spaceGrotesk: { label: "Space Grotesk", font: spaceGrotesk },
  jetBrainsMono: { label: "JetBrains Mono", font: jetBrainsMono },
} as const;

export type FontKey = keyof typeof fontRegistry;

export const fontVars = (Object.values(fontRegistry) as Array<(typeof fontRegistry)[FontKey]>)
  .map((f) => f.font.variable)
  .join(" ");

export const fontOptions = (
  Object.entries(fontRegistry) as [FontKey, { label: string; font: { variable: string } }][]
).map(([key, f]) => ({
  key,
  label: f.label,
  variable: f.font.variable,
}));
