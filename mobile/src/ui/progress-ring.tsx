import * as React from "react";
import { StyleSheet, Text, View } from "react-native";
import Svg, { Circle } from "react-native-svg";

import { useTheme } from "@/theme/provider";
import { font } from "@/theme/tokens";

/** 环形进度：任务阶段与预算使用率共用。空值显示为灰环，不假装 0%。 */
export function ProgressRing({
  ratio,
  size = 56,
  stroke = 5,
  label,
  tone,
}: {
  ratio: number | null;
  size?: number;
  stroke?: number;
  label?: string;
  tone?: "ok" | "warn" | "danger" | "accent";
}) {
  const { palette } = useTheme();
  const radius = (size - stroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const clamped = ratio === null ? 0 : Math.max(0, Math.min(1, ratio));
  const color =
    ratio === null
      ? palette.idle
      : tone === "ok"
        ? palette.ok
        : tone === "warn"
          ? palette.warn
          : tone === "danger"
            ? palette.danger
            : palette.accent;

  return (
    <View style={{ width: size, height: size }}>
      <Svg width={size} height={size}>
        <Circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          stroke={palette.border}
          strokeWidth={stroke}
          fill="none"
        />
        <Circle
          cx={size / 2}
          cy={size / 2}
          r={radius}
          stroke={color}
          strokeWidth={stroke}
          fill="none"
          strokeLinecap="round"
          strokeDasharray={`${circumference}`}
          strokeDashoffset={circumference * (1 - clamped)}
          transform={`rotate(-90 ${size / 2} ${size / 2})`}
        />
      </Svg>
      {label ? (
        <View style={styles.center}>
          <Text style={[styles.label, { color: palette.text }]}>{label}</Text>
        </View>
      ) : null}
    </View>
  );
}

const styles = StyleSheet.create({
  center: { ...StyleSheet.absoluteFillObject, alignItems: "center", justifyContent: "center" },
  label: { fontSize: font.tiny, fontVariant: ["tabular-nums"] },
});
