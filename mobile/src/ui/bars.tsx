import * as React from "react";
import { StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/provider";
import { font, space } from "@/theme/tokens";

export type BarDatum = { label: string; value: number };

/** 7 日柱状：不用图表库，几根 View 足够，还省掉一个大依赖。 */
export function Bars({ data, height = 96 }: { data: BarDatum[]; height?: number }) {
  const { palette } = useTheme();
  const max = data.reduce((acc, d) => Math.max(acc, d.value), 0) || 1;

  return (
    <View style={styles.wrap}>
      {data.map((d) => {
        const h = Math.max(2, Math.round((d.value / max) * height));
        return (
          <View key={d.label} style={styles.col}>
            <View style={styles.barArea}>
              <View style={[styles.bar, { height: h, backgroundColor: palette.accent }]} />
            </View>
            <Text style={[styles.label, { color: palette.textFaint }]} numberOfLines={1}>
              {d.label}
            </Text>
          </View>
        );
      })}
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { flexDirection: "row", alignItems: "flex-end", gap: space.sm, height: 128 },
  col: { flex: 1, alignItems: "center", gap: space.xs },
  barArea: { justifyContent: "flex-end", height: 96, width: "100%" },
  bar: { width: "100%", borderRadius: 6 },
  label: { fontSize: font.tiny },
});
