import { useRouter } from "expo-router";
import * as React from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { mobileApi } from "@/api/mobile";
import { usePoll } from "@/hooks/use-poll";
import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";
import { Card } from "@/ui/card";
import { ProgressRing } from "@/ui/progress-ring";
import { Screen } from "@/ui/screen";
import { StatNumber } from "@/ui/stat-number";
import { StatusDot } from "@/ui/status-dot";

function compact(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`;
  return String(n);
}

function relative(ms: number): string {
  if (!ms) return "—";
  const diff = Math.max(0, Date.now() - ms);
  if (diff < 60_000) return `${Math.round(diff / 1000)} 秒前`;
  if (diff < 3_600_000) return `${Math.round(diff / 60_000)} 分钟前`;
  return `${Math.round(diff / 3_600_000)} 小时前`;
}

export default function OverviewScreen() {
  const { palette } = useTheme();
  const router = useRouter();
  const { data, error, loading, updatedAt, refresh } = usePoll(() => mobileApi.overview(), 8000);

  const unread = data?.alerts_unread ?? 0;
  const danger = (data?.alerts_by_level?.error ?? 0) + (data?.alerts_by_level?.warn ?? 0);
  const tokens = data?.tokens_today;
  const totalTokens = (tokens?.input ?? 0) + (tokens?.output ?? 0);

  return (
    <Screen
      refreshing={loading}
      onRefresh={() => void refresh()}
      error={error}
      footer={updatedAt ? `更新于 ${relative(updatedAt)}` : undefined}
    >
      <View style={styles.headerRow}>
        <View>
          <Text style={[styles.h1, { color: palette.text }]}>总控台</Text>
          <StatusDot
            tone={data?.engine_mode === "running" ? "ok" : data?.engine_mode === "paused" ? "warn" : "idle"}
            label={
              data
                ? `${data.engine_mode === "running" ? "运行中" : data.engine_mode === "paused" ? "已暂停" : "空闲"} · LLM ${
                    data.llm_ready ? "就绪" : "未配置"
                  }`
                : "连接中…"
            }
          />
        </View>
        <Pressable onPress={() => router.push("/settings")} style={styles.gear}>
          <Text style={{ color: palette.textMuted, fontSize: font.small }}>设置</Text>
        </Pressable>
      </View>

      <Card>
        <StatNumber
          label="需要处理"
          value={unread}
          unit="条未读"
          tone={danger > 0 ? "danger" : unread > 0 ? "warn" : "ok"}
          size="hero"
        />
        {data?.active_task ? (
          <Pressable
            onPress={() => router.push(`/task/${data.active_task?.id ?? ""}`)}
            style={[styles.activeRow, { borderTopColor: palette.border }]}
          >
            <View style={{ flex: 1 }}>
              <Text style={{ color: palette.text, fontSize: font.body }} numberOfLines={1}>
                {data.active_task.name || data.active_task.id}
              </Text>
              <Text style={{ color: palette.textFaint, fontSize: font.small }} numberOfLines={1}>
                {data.active_task.goal || "（无目标描述）"}
              </Text>
            </View>
            <ProgressRing ratio={null} size={40} stroke={4} />
          </Pressable>
        ) : (
          <Text style={{ color: palette.textFaint, fontSize: font.small }}>当前没有活跃任务</Text>
        )}
      </Card>

      <View style={styles.grid}>
        <Card style={styles.cell}>
          <StatNumber label="运行中" value={data?.running ?? 0} tone={(data?.running ?? 0) > 0 ? "ok" : "neutral"} />
        </Card>
        <Card style={styles.cell}>
          <StatNumber label="排队" value={data?.queued ?? 0} />
        </Card>
        <Card style={styles.cell}>
          <StatNumber label="暂停" value={data?.paused ?? 0} tone={(data?.paused ?? 0) > 0 ? "warn" : "neutral"} />
        </Card>
        <Card style={styles.cell}>
          <StatNumber label="今日 token" value={compact(totalTokens)} tone="accent" />
        </Card>
      </View>

      <Card title="情报与流量">
        <View style={styles.kvRow}>
          <StatNumber label="资产" value={compact(data?.assets ?? 0)} />
          <StatNumber label="流量条目" value={compact(data?.traffic ?? 0)} />
        </View>
      </Card>

      <Card title="快捷入口">
        <View style={styles.quickRow}>
          <Pressable onPress={() => router.push("/(tabs)/alerts")} style={[styles.quick, { borderColor: palette.border }]}>
            <Text style={{ color: palette.text }}>通知 {unread > 0 ? `(${unread})` : ""}</Text>
            <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>分级收件箱</Text>
          </Pressable>
          <Pressable onPress={() => router.push("/(tabs)/usage")} style={[styles.quick, { borderColor: palette.border }]}>
            <Text style={{ color: palette.text }}>消耗</Text>
            <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>输入 / 缓存 / 输出</Text>
          </Pressable>
        </View>
      </Card>
    </Screen>
  );
}

const styles = StyleSheet.create({
  headerRow: { flexDirection: "row", alignItems: "flex-start", justifyContent: "space-between" },
  h1: { fontSize: font.title, fontWeight: "700", marginBottom: space.xs },
  gear: { paddingHorizontal: space.md, paddingVertical: space.xs },
  grid: { flexDirection: "row", flexWrap: "wrap", gap: space.md },
  cell: { flexBasis: "47%", flexGrow: 1 },
  activeRow: { flexDirection: "row", alignItems: "center", gap: space.md, borderTopWidth: StyleSheet.hairlineWidth, paddingTop: space.md },
  kvRow: { flexDirection: "row", gap: space.xl },
  quickRow: { flexDirection: "row", gap: space.md },
  quick: { flex: 1, borderWidth: StyleSheet.hairlineWidth, borderRadius: radius.md, padding: space.md, gap: 2 },
});
