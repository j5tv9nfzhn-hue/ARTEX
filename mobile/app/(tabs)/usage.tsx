import * as React from "react";
import { StyleSheet, Text, View } from "react-native";

import { mobileApi } from "@/api/mobile";
import { usePoll } from "@/hooks/use-poll";
import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";
import { Bars } from "@/ui/bars";
import { Card } from "@/ui/card";
import { Empty } from "@/ui/empty";
import { Screen } from "@/ui/screen";
import { StatNumber } from "@/ui/stat-number";

function compact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

export default function UsageScreen() {
  const { palette } = useTheme();
  const { data, error, loading, refresh } = usePoll(() => mobileApi.usage(7), 30000, "usage");

  const total = data?.total;
  const sum = (total?.input ?? 0) + (total?.output ?? 0) + (total?.cache_read ?? 0) || 1;
  const parts = [
    { key: "输入", value: total?.input ?? 0, color: palette.accent },
    { key: "缓存", value: total?.cache_read ?? 0, color: palette.ok },
    { key: "输出", value: total?.output ?? 0, color: palette.warn },
  ];

  const daily = (data?.daily ?? []).map((d) => ({
    label: d.date.slice(5),
    value: d.input + d.output + d.cache_read,
  }));

  return (
    <Screen refreshing={loading} onRefresh={() => void refresh()} error={error}>
      <Text style={[styles.h1, { color: palette.text }]}>模型消耗</Text>

      <Card title="近 7 日合计">
        <View style={styles.parts}>
          {parts.map((p) => (
            <View key={p.key} style={styles.part}>
              <View style={[styles.dot, { backgroundColor: p.color }]} />
              <Text style={{ color: palette.textMuted, fontSize: font.small }}>{p.key}</Text>
              <Text style={{ color: palette.text, fontSize: font.body, fontVariant: ["tabular-nums"] }}>
                {compact(p.value)}
              </Text>
            </View>
          ))}
        </View>
        <View style={[styles.bar, { backgroundColor: palette.cardAlt }]}>
          {parts.map((p) => (
            <View key={p.key} style={{ flex: p.value / sum, backgroundColor: p.color }} />
          ))}
        </View>
      </Card>

      <Card title="每日总量">
        {daily.length === 0 ? <Empty title="暂无计量数据" hint="配置 LLM 并跑过任务后才有记录。" /> : <Bars data={daily} />}
      </Card>

      <Card title="按配置档">
        {(data?.by_profile ?? []).length === 0 ? (
          <Empty title="暂无分布" />
        ) : (
          (data?.by_profile ?? []).map((p) => (
            <View key={p.profile || "(默认)"} style={styles.profileRow}>
              <Text style={{ color: palette.text, fontSize: font.small, flex: 1 }} numberOfLines={1}>
                {p.profile || "(默认档)"}
              </Text>
              <StatNumber label="输入" value={compact(p.input)} />
              <StatNumber label="输出" value={compact(p.output)} />
            </View>
          ))
        )}
      </Card>

      <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>
        金额口径仍在 PC 端（价格表与折算在 web 用量页），手机端只保证 token 与 PC 同源一致。
      </Text>
    </Screen>
  );
}

const styles = StyleSheet.create({
  h1: { fontSize: font.title, fontWeight: "700" },
  parts: { flexDirection: "row", justifyContent: "space-between", gap: space.md },
  part: { alignItems: "flex-start", gap: 2 },
  dot: { width: 8, height: 8, borderRadius: 4 },
  bar: { flexDirection: "row", height: 10, borderRadius: radius.pill, overflow: "hidden", marginTop: space.sm },
  profileRow: { flexDirection: "row", alignItems: "center", gap: space.lg, paddingVertical: space.xs },
});
