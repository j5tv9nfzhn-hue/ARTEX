import * as React from "react";
import { Pressable, StyleSheet, Text } from "react-native";

import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";

export function Chip({
  label,
  active,
  onPress,
  badge,
}: {
  label: string;
  active?: boolean;
  onPress?: () => void;
  badge?: number;
}) {
  const { palette } = useTheme();
  return (
    <Pressable
      onPress={onPress}
      style={[
        styles.chip,
        {
          backgroundColor: active ? palette.accentSoft : palette.cardAlt,
          borderColor: active ? palette.accent : palette.border,
        },
      ]}
    >
      <Text style={{ color: active ? palette.accent : palette.textMuted, fontSize: font.small }}>
        {label}
        {badge ? ` ${badge}` : ""}
      </Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  chip: {
    paddingHorizontal: space.md,
    paddingVertical: space.xs + 2,
    borderRadius: radius.pill,
    borderWidth: StyleSheet.hairlineWidth,
  },
});
