import { useRouter } from "expo-router";
import * as React from "react";
import { Alert, Pressable, StyleSheet, Text, View } from "react-native";

import { mobileApi } from "@/api/mobile";
import type { MobileTask } from "@/api/types";
import { usePoll } from "@/hooks/use-poll";
import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";
import { Card } from "@/ui/card";
import { Empty } from "@/ui/empty";
import { ProgressRing } from "@/ui/progress-ring";
import { Screen } from "@/ui/screen";
import { StatusDot } from "@/ui/status-dot";

function stateOf(t: MobileTask): { label: string; tone: "ok" | "warn" | "idle" | "danger" } {
  if (t.completed) return { label: "已完成", tone: "idle" };
  if (t.queued) return { label: "排队", tone: "warn" };
  if (t.paused) return { label: "已暂停", tone: "danger" };
  return { label: "运行中", tone: "ok" };
}

export default function TasksScreen() {
  const { palette } = useTheme();
  const router = useRouter();
  const { data, error, loading, stale, refresh } = usePoll(() => mobileApi.tasks(), 8000, "tasks");
  const [busy, setBusy] = React.useState<string | null>(null);

  const control = React.useCallback(
    async (task: MobileTask, action: "pause" | "resume") => {
      setBusy(task.id);
      try {
        await mobileApi.controlTasks(action, [task.id]);
        await refresh();
      } catch (err) {
        Alert.alert("操作失败", err instanceof Error ? err.message : "未知错误");
      } finally {
        setBusy(null);
      }
    },
    [refresh],
  );

  const items = data ?? [];

  return (
    <Screen
      refreshing={loading}
      onRefresh={() => void refresh()}
      error={error}
      footer={stale ? "当前显示本地缓存，恢复连接后会自动刷新" : undefined}
    >
      <Text style={[styles.h1, { color: palette.text }]}>任务</Text>
      {items.length === 0 ? (
        <Empty title="暂无任务" hint="在 PC 上创建任务后，这里会实时出现。" />
      ) : (
        items.map((t) => {
          const state = stateOf(t);
          const ratio = t.progress >= 0 ? t.progress : null;
          return (
            <Card key={t.id}>
              <View style={styles.row}>
                <ProgressRing ratio={ratio} size={48} stroke={4} label={ratio === null ? "—" : `${Math.round(ratio * 100)}`} />
                <Pressable onPress={() => router.push(`/task/${t.id}`)} style={{ flex: 1, gap: 2 }}>
                  <View style={styles.row}>
                    <Text style={{ color: palette.text, fontSize: font.body, flex: 1 }} numberOfLines={1}>
                      {t.name || t.id}
                    </Text>
                    <StatusDot tone={state.tone} label={state.label} />
                  </View>
                  <Text style={{ color: palette.textFaint, fontSize: font.small }} numberOfLines={2}>
                    {t.goal || t.description || "（无描述）"}
                  </Text>
                  <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>
                    目标 {t.goals_met}/{t.goals_total || "—"}
                  </Text>
                </Pressable>
              </View>
              <View style={styles.actions}>
                <Pressable
                  disabled={busy === t.id}
                  onPress={() => void control(t, t.paused ? "resume" : "pause")}
                  style={[styles.btn, { borderColor: t.paused ? palette.ok : palette.warn }]}
                >
                  <Text style={{ color: t.paused ? palette.ok : palette.warn, fontSize: font.small }}>
                    {busy === t.id ? "处理中…" : t.paused ? "继续执行" : "停止执行"}
                  </Text>
                </Pressable>
              </View>
            </Card>
          );
        })
      )}
      <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>
        ARTEX 用「暂停」实现停止：暂停后引擎不再推进该任务；细化操作（工具审批、diff、日志）仍在 PC。
      </Text>
    </Screen>
  );
}

const styles = StyleSheet.create({
  h1: { fontSize: font.title, fontWeight: "700" },
  row: { flexDirection: "row", alignItems: "center", gap: space.md },
  actions: { marginTop: space.sm },
  btn: {
    alignSelf: "flex-start",
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.pill,
    paddingHorizontal: space.lg,
    paddingVertical: space.sm,
  },
});
