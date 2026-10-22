import { useRouter } from "expo-router";
import * as React from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { mobileApi } from "@/api/mobile";
import { friendlyNetError } from "@/api/net-error";
import { normalizeBaseUrl, saveSession } from "@/api/session";
import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";

export default function LoginScreen() {
  const { palette } = useTheme();
  const router = useRouter();
  const insets = useSafeAreaInsets();
  const [server, setServer] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const submit = React.useCallback(async () => {
    const baseUrl = normalizeBaseUrl(server);
    if (!baseUrl) {
      setError("请填写服务器地址，例如 192.168.1.10:8787");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await saveSession({ baseUrl });
      const res = await mobileApi.login(password);
      await saveSession({ token: res.token });
      router.replace("/(tabs)");
    } catch (err) {
      setError(friendlyNetError(err, "登录失败"));
    } finally {
      setBusy(false);
    }
  }, [password, router, server]);

  return (
    <View style={[styles.root, { backgroundColor: palette.bg, paddingTop: insets.top + 48 }]}>
      <Text style={[styles.title, { color: palette.text }]}>ARTEX 总控台</Text>
      <Text style={{ color: palette.textMuted, fontSize: font.small }}>
        连接你自己的 ARTEX 服务。手机端只做总控：看结论、收通知、停或批。
      </Text>

      <View style={styles.field}>
        <Text style={[styles.label, { color: palette.textMuted }]}>服务器地址</Text>
        <TextInput
          value={server}
          onChangeText={setServer}
          placeholder="192.168.1.10:8787 或 https://artex.example.com"
          placeholderTextColor={palette.textFaint}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
          style={[styles.input, { color: palette.text, borderColor: palette.border, backgroundColor: palette.card }]}
        />
      </View>

      <View style={styles.field}>
        <Text style={[styles.label, { color: palette.textMuted }]}>管理密码</Text>
        <TextInput
          value={password}
          onChangeText={setPassword}
          placeholder="与 PC 端登录密码相同"
          placeholderTextColor={palette.textFaint}
          secureTextEntry
          style={[styles.input, { color: palette.text, borderColor: palette.border, backgroundColor: palette.card }]}
        />
      </View>

      {error ? <Text style={{ color: palette.danger, fontSize: font.small }}>{error}</Text> : null}

      <Pressable
        disabled={busy}
        onPress={() => void submit()}
        style={[styles.button, { backgroundColor: palette.accent, opacity: busy ? 0.7 : 1 }]}
      >
        {busy ? <ActivityIndicator color="#fff" /> : <Text style={styles.buttonText}>连接</Text>}
      </Pressable>

      <Pressable
        onPress={() => router.push("/pair")}
        style={[styles.secondaryButton, { borderColor: palette.border }]}
      >
        <Text style={[styles.secondaryText, { color: palette.text }]}>扫码绑定（扫电脑上的二维码）</Text>
      </Pressable>

      <Text style={{ color: palette.textFaint, fontSize: font.tiny, marginTop: space.md }}>
        外网访问请先配好 TLS 与反向代理；明文 HTTP 只建议在局域网内使用。
      </Text>
    </View>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, paddingHorizontal: space.xl, gap: space.md },
  title: { fontSize: 30, fontWeight: "700" },
  field: { gap: space.xs, marginTop: space.sm },
  label: { fontSize: font.small },
  input: {
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingHorizontal: space.md,
    paddingVertical: space.md,
    fontSize: font.body,
  },
  button: {
    marginTop: space.md,
    borderRadius: radius.md,
    paddingVertical: space.md + 2,
    alignItems: "center",
  },
  buttonText: { color: "#fff", fontSize: font.body, fontWeight: "600" },
  secondaryButton: {
    marginTop: space.sm,
    borderRadius: radius.md,
    paddingVertical: space.md,
    alignItems: "center",
    borderWidth: StyleSheet.hairlineWidth,
  },
  secondaryText: { fontSize: font.body, fontWeight: "600" },
});
