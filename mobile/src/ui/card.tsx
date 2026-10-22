import * as React from "react";
import { StyleSheet, Text, View, type ViewStyle } from "react-native";

import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";

export function Card({
  children,
  title,
  action,
  style,
}: {
  children: React.ReactNode;
  title?: string;
  action?: React.ReactNode;
  style?: ViewStyle;
}) {
  const { palette } = useTheme();
  return (
    <View
      style={[
        styles.card,
        { backgroundColor: palette.card, borderColor: palette.border },
        style,
      ]}
    >
      {title || action ? (
        <View style={styles.header}>
          {title ? <Text style={[styles.title, { color: palette.textMuted }]}>{title}</Text> : null}
          {action}
        </View>
      ) : null}
      {children}
    </View>
  );
}

const styles = StyleSheet.create({
  card: { borderWidth: StyleSheet.hairlineWidth, borderRadius: radius.lg, padding: space.lg, gap: space.sm },
  header: { flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  title: { fontSize: font.tiny, letterSpacing: 1, textTransform: "uppercase" },
});
