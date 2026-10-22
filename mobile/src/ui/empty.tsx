import * as React from "react";
import { StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/provider";
import { font, space } from "@/theme/tokens";

export function Empty({ title, hint }: { title: string; hint?: string }) {
  const { palette } = useTheme();
  return (
    <View style={styles.wrap}>
      <Text style={[styles.title, { color: palette.text }]}>{title}</Text>
      {hint ? <Text style={[styles.hint, { color: palette.textFaint }]}>{hint}</Text> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  wrap: { alignItems: "center", paddingVertical: space.xxl, gap: space.xs },
  title: { fontSize: font.body, fontWeight: "600" },
  hint: { fontSize: font.small, textAlign: "center" },
});
