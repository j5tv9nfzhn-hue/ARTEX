import * as React from "react";
import { StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/provider";
import { font, space } from "@/theme/tokens";

export type StatusTone = "ok" | "warn" | "danger" | "idle";

export function StatusDot({ tone, label }: { tone: StatusTone; label?: string }) {
  const { palette } = useTheme();
  const color =
    tone === "ok" ? palette.ok : tone === "warn" ? palette.warn : tone === "danger" ? palette.danger : palette.idle;
  return (
    <View style={styles.row}>
      <View style={[styles.dot, { backgroundColor: color }]} />
      {label ? <Text style={[styles.label, { color: palette.textMuted }]}>{label}</Text> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  row: { flexDirection: "row", alignItems: "center", gap: space.sm },
  dot: { width: 8, height: 8, borderRadius: 4 },
  label: { fontSize: font.small },
});
