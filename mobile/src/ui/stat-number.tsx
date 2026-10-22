import * as React from "react";
import { StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/provider";
import { font, space } from "@/theme/tokens";

/** 大数字：等宽数字（tabular-nums）避免轮询刷新时整行抖动。 */
export function StatNumber({
  label,
  value,
  unit,
  tone = "neutral",
  size = "md",
}: {
  label: string;
  value: string | number;
  unit?: string;
  tone?: "neutral" | "ok" | "warn" | "danger" | "accent";
  size?: "hero" | "md";
}) {
  const { palette } = useTheme();
  const color =
    tone === "ok"
      ? palette.ok
      : tone === "warn"
        ? palette.warn
        : tone === "danger"
          ? palette.danger
          : tone === "accent"
            ? palette.accent
            : palette.text;

  return (
    <View style={styles.wrap}>
      <Text style={[styles.label, { color: palette.textMuted }]}>{label}</Text>
      <View style={styles.valueRow}>
        <Text
          style={[
            size === "hero" ? styles.hero : styles.value,
            { color, fontVariant: ["tabular-nums"] },
          ]}
        >
          {value}
        </Text>
        {unit ? <Text style={[styles.unit, { color: palette.textFaint }]}>{unit}</Text> : null}
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { gap: 2 },
  label: { fontSize: font.tiny },
  valueRow: { flexDirection: "row", alignItems: "baseline", gap: space.xs },
  hero: { fontSize: font.hero, fontWeight: "700" },
  value: { fontSize: font.title, fontWeight: "600" },
  unit: { fontSize: font.small },
});
