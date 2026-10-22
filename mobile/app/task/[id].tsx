import { useLocalSearchParams, useRouter } from "expo-router";
import * as React from "react";
import { Alert, Pressable, StyleSheet, Text, View } from "react-native";

import { mobileApi } from "@/api/mobile";
import { usePoll } from "@/hooks/use-poll";
import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";
import { Card } from "@/ui/card";
import { Screen } from "@/ui/screen";
import { StatusDot } from "@/ui/status-dot";

export default function TaskDetailScreen() {
  const { palette } = useTheme();
  const router = useRouter();
  const params = useLocalSearchParams<{ id: string }>();
  const id = String(params.id ?? "");
  const { data, error, loading, refresh } = usePoll(() => mobileApi.task(id), 6000);

  const control = React.useCallback(
    async (action: "pause" | "resume") => {
      try {
        await mobileApi.controlTasks(action, [id]);
        await refresh();
      } catch (err) {
        Alert.alert("操作失败", err instanceof Error ? err.message : "未知错误");
      }
    },
    [id, refresh],
  );

  return (
    <Screen refreshing={loading} onRefresh={() => void refresh()} error={error}>
      <View style={styles.head}>
        <Pressable onPress={() => router.back()}>
          <Text style={{ color: palette.accent, fontSize: font.small }}>‹ 返回</Text>
        </Pressable>
      </View>

      <Text style={[styles.h1, { color: palette.text }]}>{data?.name || id}</Text>
      <StatusDot
        tone={data?.queued ? "warn" : data?.paused ? "danger" : data?.completed_at ? "idle" : "ok"}
        label={data?.queued ? "排队中" : data?.paused ? "已暂停" : data?.completed_at ? "已完成" : "运行中"}
      />

      <Card title="目标">
        <Text style={{ color: palette.text, fontSize: font.body }}>{data?.goal || "（无目标描述）"}</Text>
      </Card>

      <Card title="描述">
        <Text style={{ color: palette.textMuted, fontSize: font.small }}>
          {data?.description || "（无描述）"}
        </Text>
      </Card>

      <Card title="控制">
        <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>
          手机端只提供启停；细化参数、工具审批与日志请回 PC。ARTEX 的「停止」即暂停。
        </Text>
        <Pressable
          onPress={() => void control(data?.paused ? "resume" : "pause")}
          style={[styles.btn, { borderColor: data?.paused ? palette.ok : palette.warn }]}
        >
          <Text style={{ color: data?.paused ? palette.ok : palette.warn, fontSize: font.body }}>
            {data?.paused ? "继续执行" : "停止执行"}
          </Text>
        </Pressable>
      </Card>
    </Screen>
  );
}

const styles = StyleSheet.create({
  head: { flexDirection: "row" },
  h1: { fontSize: font.title, fontWeight: "700" },
  btn: {
    marginTop: space.sm,
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingVertical: space.md,
    alignItems: "center",
  },
});
