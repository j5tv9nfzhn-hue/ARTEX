import { useRouter } from "expo-router";
import * as React from "react";
import { Alert, Pressable, StyleSheet, Text, View } from "react-native";

import { clearSession, getSession } from "@/api/session";
import { useTheme, type ThemePreference } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";
import { Card } from "@/ui/card";
import { Chip } from "@/ui/chip";
import { Screen } from "@/ui/screen";

const OPTIONS: { key: ThemePreference; label: string }[] = [
  { key: "system", label: "跟随系统" },
  { key: "light", label: "浅色" },
  { key: "dark", label: "深色" },
];

export default function SettingsScreen() {
  const { palette, preference, setPreference } = useTheme();
  const router = useRouter();
  const session = getSession();

  return (
    <Screen>
      <Text style={[styles.h1, { color: palette.text }]}>设置</Text>

      <Card title="主题（双色系统）">
        <Text style={{ color: palette.textMuted, fontSize: font.small }}>
          明暗两套 token 完整独立；系统切换时自动跟随，也可以手动锁定。
        </Text>
        <View style={styles.row}>
          {OPTIONS.map((o) => (
            <Chip key={o.key} label={o.label} active={preference === o.key} onPress={() => setPreference(o.key)} />
          ))}
        </View>
      </Card>

      <Card title="连接">
        <Text style={{ color: palette.text, fontSize: font.small }}>{session.baseUrl || "(未配置)"}</Text>
        <Text style={{ color: palette.textFaint, fontSize: font.tiny }}>更换服务器需要重新登录。</Text>
      </Card>

      <Pressable
        onPress={() => {
          Alert.alert("退出登录", "将清除本机保存的令牌，推送注册也需要重新进行。", [
            { text: "取消", style: "cancel" },
            {
              text: "退出",
              style: "destructive",
              onPress: () => {
                void clearSession().finally(() => router.replace("/login"));
              },
            },
          ]);
        }}
        style={[styles.logout, { borderColor: palette.danger }]}
      >
        <Text style={{ color: palette.danger, fontSize: font.body }}>退出登录</Text>
      </Pressable>
    </Screen>
  );
}

const styles = StyleSheet.create({
  h1: { fontSize: font.title, fontWeight: "700" },
  row: { flexDirection: "row", gap: space.sm, flexWrap: "wrap", marginTop: space.sm },
  logout: {
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingVertical: space.md,
    alignItems: "center",
  },
});
