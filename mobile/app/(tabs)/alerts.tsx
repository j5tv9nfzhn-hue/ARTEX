import * as React from "react";
import { Alert, Pressable, StyleSheet, Text, View } from "react-native";

import { mobileApi } from "@/api/mobile";
import type { Approval, MobileAlert } from "@/api/types";
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

function previewToolInput(input: unknown): string {
  if (input === null || input === undefined) return "";
  try {
    const text = typeof input === "string" ? input : JSON.stringify(input);
    return text.length > 180 ? `${text.slice(0, 180)}…` : text;
  } catch {
    return "";
  }
}

export default function AlertsScreen() {
  const { palette } = useTheme();
  const [level, setLevel] = React.useState<Level>("all");
  const alerts = usePoll(() => mobileApi.alerts({ limit: 80 }), 10000, "alerts");
  const approvals = usePoll(() => mobileApi.approvals(), 8000, "approvals");
  const [busy, setBusy] = React.useState<number | null>(null);

  const items = (alerts.data?.items ?? []).filter((a: MobileAlert) =>
    level === "all" ? true : a.level === level,
  );
  const pending = approvals.data ?? [];

  const markAll = React.useCallback(async () => {
    await mobileApi.readAllAlerts().catch(() => {});
    await alerts.refresh();
  }, [alerts]);

  const markOne = React.useCallback(
    async (id: number) => {
      await mobileApi.readAlerts([id]).catch(() => {});
      await alerts.refresh();
    },
    [alerts],
  );

  const decide = React.useCallback(
    (item: Approval, decision: "allowed" | "denied") => {
      const what = item.tool_name || "该工具调用";
      Alert.alert(
        decision === "allowed" ? "批准执行" : "拒绝执行",
        `${item.agent_name ? `${item.agent_name} · ` : ""}${what}\n\n${
          item.reason ? `原因：${item.reason}\n` : ""
        }${previewToolInput(item.tool_input)}`,
        [
          { text: "取消", style: "cancel" },
          {
            text: decision === "allowed" ? "批准" : "拒绝",
            style: decision === "allowed" ? "default" : "destructive",
            onPress: () => {
              setBusy(item.id);
              void mobileApi
                .decide(item.id, decision)
                .catch((err: unknown) => {
                  Alert.alert("操作失败", err instanceof Error ? err.message : "未知错误");
                })
                .finally(() => {
                  setBusy(null);
                  void approvals.refresh();
                  void alerts.refresh();
                });
            },
          },
        ],
      );
    },
    [alerts, approvals],
  );

  return (
    <Screen
      refreshing={alerts.loading || approvals.loading}
      onRefresh={() => {
        void alerts.refresh();
        void approvals.refresh();
      }}
      error={alerts.error ?? approvals.error}
      footer={alerts.stale ? "当前显示本地缓存，恢复连接后会自动刷新" : undefined}
    >
      <View style={styles.head}>
        <Text style={[styles.h1, { color: palette.text }]}>通知</Text>
        <Pressable onPress={() => void markAll()}>
          <Text style={{ color: palette.accent, fontSize: font.small }}>全部已读</Text>
        </Pressable>
      </View>

      <Card title={`待审批 · ${pending.length}`}>
        {pending.length === 0 ? (
          <Text style={{ color: palette.textFaint, fontSize: font.small }}>没有待审批的调用</Text>
        ) : (
          pending.map((item) => (
            <View key={item.id} style={[styles.approval, { borderColor: palette.warn }]}>
              <Text style={{ color: palette.text, fontSize: font.body }}>
                {item.tool_name || `工具调用 #${item.id}`}
              </Text>
              <Text style={{ color: palette.textMuted, fontSize: font.small }}>
                {item.agent_name ? `${item.agent_name} · ` : ""}
                {item.task_id ? `任务 ${item.task_id}` : "无任务归属"}
              </Text>
              {item.reason ? (
                <Text style={{ color: palette.textFaint, fontSize: font.tiny }} numberOfLines={2}>
                  {item.reason}
                </Text>
              ) : null}
              <Text style={{ color: palette.textFaint, fontSize: font.tiny }} numberOfLines={3}>
                {previewToolInput(item.tool_input)}
              </Text>
              <View style={styles.approvalActions}>
                <Pressable
                  disabled={busy === item.id}
                  onPress={() => decide(item, "allowed")}
                  style={[styles.actBtn, { borderColor: palette.ok }]}
                >
                  <Text style={{ color: palette.ok, fontSize: font.small }}>
                    {busy === item.id ? "处理中…" : "批准"}
                  </Text>
                </Pressable>
                <Pressable
                  disabled={busy === item.id}
                  onPress={() => decide(item, "denied")}
                  style={[styles.actBtn, { borderColor: palette.danger }]}
                >
                  <Text style={{ color: palette.danger, fontSize: font.small }}>拒绝</Text>
                </Pressable>
              </View>
            </View>
          ))
        )}
      </Card>

      <View style={styles.chips}>
        {(["all", "error", "warn", "info"] as Level[]).map((l) => (
          <Chip
            key={l}
            label={l === "all" ? `全部 ${alerts.data?.items.length ?? 0}` : LEVEL_LABEL[l]}
            active={level === l}
            onPress={() => setLevel(l)}
            badge={l === "all" ? alerts.data?.unread : alerts.data?.by_level?.[l]}
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
  approval: {
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    padding: space.md,
    gap: space.xs,
    marginTop: space.sm,
  },
  approvalActions: { flexDirection: "row", gap: space.sm, marginTop: space.xs },
  actBtn: {
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.pill,
    paddingHorizontal: space.lg,
    paddingVertical: space.xs + 2,
  },
});
