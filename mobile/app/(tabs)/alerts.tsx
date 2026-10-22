import * as React from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { mobileApi } from "@/api/mobile";
import type { MobileAlert } from "@/api/types";
import { usePoll } from "@/hooks/use-poll";
import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";
import { Card } from "@/ui/card";
import { Chip } from "@/ui/chip";
import { Empty } from "@/ui/empty";
import { Screen } from "@/ui/screen";

type Level = "all" | "error" | "warn" | "info";

const LEVEL_LABEL: Record<string, string> = { error: "危急", warn: "注意", success: "完成", info: "提示" };

function toneOf(level: string): "danger" | "warn" | "ok" | "idle" {
  if (level === "error") return "danger";
  if (level === "warn") return "warn";
  if (level === "success") return "ok";
  return "idle";
}

function timeOf(ts: number): string {
  const d = new Date(ts);
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export default function AlertsScreen() {
  const { palette } = useTheme();
  const [level, setLevel] = React.useState<Level>("all");
  const { data, error, loading, refresh } = usePoll(() => mobileApi.alerts({ limit: 80 }), 10000);

  const items = (data?.items ?? []).filter((a: MobileAlert) =>
    level === "all" ? true : a.level === level,
  );

  const markAll = React.useCallback(async () => {
    await mobileApi.readAllAlerts().catch(() => {});
    await refresh();
  }, [refresh]);

  const markOne = React.useCallback(
    async (id: number) => {
      await mobileApi.readAlerts([id]).catch(() => {});
      await refresh();
    },
    [refresh],
  );

  return (
    <Screen refreshing={loading} onRefresh={() => void refresh()} error={error}>
      <View style={styles.head}>
        <Text style={[styles.h1, { color: palette.text }]}>通知</Text>
        <Pressable onPress={() => void markAll()}>
          <Text style={{ color: palette.accent, fontSize: font.small }}>全部已读</Text>
        </Pressable>
      </View>

      <View style={styles.chips}>
        {(["all", "error", "warn", "info"] as Level[]).map((l) => (
          <Chip
            key={l}
            label={l === "all" ? `全部 ${data?.items.length ?? 0}` : LEVEL_LABEL[l]}
            active={level === l}
            onPress={() => setLevel(l)}
            badge={l === "all" ? data?.unread : data?.by_level?.[l]}
          />
        ))}
      </View>

      {items.length === 0 ? (
        <Empty title="没有通知" hint="任务完成、漏洞命中、审批请求与系统提示都会落在这里。" />
      ) : (
        items.map((a) => {
          const tone = toneOf(a.level);
          const color =
            tone === "danger" ? palette.danger : tone === "warn" ? palette.warn : tone === "ok" ? palette.ok : palette.idle;
          const unread = !a.read_at;
          return (
            <Pressable key={a.id} onPress={() => void markOne(a.id)}>
              <Card style={{ borderLeftWidth: 3, borderLeftColor: color, opacity: unread ? 1 : 0.62 }}>
                <View style={styles.row}>
                  <Text style={{ color: palette.text, fontSize: font.body, flex: 1 }} numberOfLines={2}>
                    {a.title}
                  </Text>
                  <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>{timeOf(a.ts)}</Text>
                </View>
                {a.body ? (
                  <Text style={{ color: palette.textMuted, fontSize: font.small }} numberOfLines={3}>
                    {a.body}
                  </Text>
                ) : null}
                <View style={styles.meta}>
                  <Text style={{ color, fontSize: font.tiny }}>
                    {LEVEL_LABEL[a.level] ?? a.level}
                    {a.severity ? ` · ${a.severity}` : ""}
                  </Text>
                  {a.task_id ? (
                    <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>任务 {a.task_id}</Text>
                  ) : null}
                  {unread ? <Text style={{ color: palette.accent, fontSize: font.tiny }}>未读</Text> : null}
                </View>
              </Card>
            </Pressable>
          );
        })
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  head: { flexDirection: "row", alignItems: "center", justifyContent: "space-between" },
  h1: { fontSize: font.title, fontWeight: "700" },
  chips: { flexDirection: "row", gap: space.sm, flexWrap: "wrap" },
  row: { flexDirection: "row", alignItems: "flex-start", gap: space.sm },
  meta: { flexDirection: "row", gap: space.md, alignItems: "center", marginTop: space.xs },
});
