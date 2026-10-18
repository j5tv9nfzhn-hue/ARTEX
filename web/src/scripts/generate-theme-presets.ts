/**
 * Script: generate-theme-presets.ts
 *
 * Scans `/src/styles/presets/*.css` to extract theme metadata and `--primary` colors,
 * then updates the generated block inside `/src/lib/preferences/theme.ts`.
 */

import fs from "node:fs";
import path from "node:path";

interface ThemePresetMeta {
  label: string;
  value: string;
  primary: {
    light: string;
    dark: string;
  };
}

const PRESETS_DIR = path.resolve(process.cwd(), "src/styles/presets");
const THEME_TS_PATH = path.resolve(process.cwd(), "src/lib/preferences/theme.ts");

function extractMetadata(content: string, fileName: string) {
  const commentMatch = content.match(/\/\*\*?([\s\S]*?)\*\//);

  let label: string | undefined;
  let value: string | undefined;

  if (commentMatch) {
    const commentContent = commentMatch[1];
    const labelMatch = commentContent.match(/label:\s*(.+)/i);
    const valueMatch = commentContent.match(/value:\s*(.+)/i);

    label = labelMatch?.[1]?.trim();
    value = valueMatch?.[1]?.trim();
  }

  const derivedValue = fileName.replace(/\.css$/, "");
  const finalValue = value ?? derivedValue;

  const finalLabel =
    label ??
    finalValue
      .split("-")
      .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
      .join(" ");

  if (!label || !value) {
    console.warn(`⚠️ [${fileName}] Missing label or value in comment block, using fallback:`, {
      label: finalLabel,
      value: finalValue,
    });
  }

  return { label: finalLabel, value: finalValue };
}

function extractPrimaryColors(content: string, fileName: string) {
  const lightMatch = content.match(/:root\[data-theme-preset="[^"]+"\]\s*{[^}]*--primary:\s*([^;]+);/);
  const darkMatch = content.match(/\.dark:root\[data-theme-preset="[^"]+"\]\s*{[^}]*--primary:\s*([^;]+);/);

  const light = lightMatch?.[1]?.trim() ?? "";
  const dark = darkMatch?.[1]?.trim() ?? "";

  if (!light || !dark) {
    console.warn(`⚠️ [${fileName}] Missinglight/dark --primary value (light: "${light}", dark: "${dark}")`);
  }

  return { light, dark };
}

function parsePresetFiles(): ThemePresetMeta[] {
  const files = fs.readdirSync(PRESETS_DIR).filter((f) => f.endsWith(".css"));

  const presets: ThemePresetMeta[] = [
    {
      label: "默认",
      value: "default",
      primary: {
        light: "oklch(0.52 0.175 248)",
        dark: "oklch(0.58 0.185 248)",
      },
    },
  ];

  for (const file of files) {
    const filePath = path.join(PRESETS_DIR, file);
    const content = fs.readFileSync(filePath, "utf8");

    const { label, value } = extractMetadata(content, file);
    const primary = extractPrimaryColors(content, file);

    presets.push({ label, value, primary });
  }

  return presets;
}

const presets = parsePresetFiles();

const generatedBlock = `// --- generated:themePresets:start ---

export const THEME_PRESET_OPTIONS = ${JSON.stringify(presets, null, 2)} as const;

export const THEME_PRESET_VALUES = THEME_PRESET_OPTIONS.map((p) => p.value);

export type ThemePreset = (typeof THEME_PRESET_OPTIONS)[number]["value"];

// --- generated:themePresets:end ---`;

const themeFile = fs.readFileSync(THEME_TS_PATH, "utf8");
const updated = themeFile.replace(
  /\/\/ --- generated:themePresets:start ---[\s\S]*?\/\/ --- generated:themePresets:end ---/,
  generatedBlock,
);

if (updated === themeFile) {
  console.warn("ℹ️ No changes detected in theme.ts");
} else {
  fs.writeFileSync(THEME_TS_PATH, updated, "utf8");
  console.log("✅ Successfully updated THEME_PRESET_OPTIONS in theme.ts");
}
