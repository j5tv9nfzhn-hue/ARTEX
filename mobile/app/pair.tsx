import { CameraView, useCameraPermissions } from "expo-camera";
import { useRouter } from "expo-router";
import * as React from "react";
import { ActivityIndicator, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { claimPair, parsePairPayload } from "@/api/pair";
import { normalizeBaseUrl, saveSession } from "@/api/session";
import { useTheme } from "@/theme/provider";
import { font, radius, space } from "@/theme/tokens";

// 扫码绑定
//
// 电脑上「系统 → 局域网连接」会出一张配对二维码。这里读到它就同时拿到
// 服务器地址和一次性凭据，扫完直接进主页——不用抄 IP，也不用输密码。
//
// 相机权限在这一页主动申请（不在启动时就弹），拒绝之后仍然可以手输地址 + 配对码，
// 所以「不给权限」不是死路。相册暂未接入：从相册选图需要一条完整的图像解码链路
// （JPEG/PNG → RGBA → 定位二维码），本轮不做半成品，手输一行就够。

type Phase = "idle" | "claiming";

export default function PairScreen() {
  const { palette } = useTheme();
  const router = useRouter();
  const insets = useSafeAreaInsets();
  const [permission, requestPermission] = useCameraPermissions();
  const [phase, setPhase] = React.useState<Phase>("idle");
  const [error, setError] = React.useState<string | null>(null);
  const [host, setHost] = React.useState("");
  const [code, setCode] = React.useState("");
  const [torch, setTorch] = React.useState(false);
  // 一次扫码会连续回调很多帧：只认第一帧，绑定成功或失败后再解锁。
  const locked = React.useRef(false);

  const finish = React.useCallback(
    async (targetHost: string, targetCode: string) => {
      setPhase("claiming");
      setError(null);
      try {
        const claim = await claimPair(targetHost, targetCode);
        await saveSession({ baseUrl: normalizeBaseUrl(claim.base_url || targetHost), token: claim.token });
        router.replace("/(tabs)");
      } catch (e) {
        locked.current = false;
        setPhase("idle");
        setError(e instanceof Error ? e.message : "绑定失败，请重新生成配对码");
      }
    },
    [router],
  );

  const onScanned = React.useCallback(
    (data: string) => {
      if (locked.current) return;
      const target = parsePairPayload(data);
      if (!target) {
        setError("这不是 ARTEX 的配对码，请对准电脑上的那张图");
        return;
      }
      locked.current = true;
      void finish(target.host, target.code);
    },
    [finish],
  );

  const submitManual = React.useCallback(() => {
    const trimmedHost = host.trim();
    const trimmedCode = code.trim().toUpperCase();
    if (!trimmedHost || !trimmedCode) {
      setError("请填写服务器地址和配对码");
      return;
    }
    locked.current = true;
    void finish(trimmedHost, trimmedCode);
  }, [code, finish, host]);

  const busy = phase === "claiming";
  const granted = permission?.granted ?? false;

  return (
    <ScrollView
      style={{ flex: 1, backgroundColor: palette.bg }}
      contentContainerStyle={{ padding: space.xl, paddingTop: insets.top + space.xl, gap: space.md }}
      keyboardShouldPersistTaps="handled"
    >
      <View style={{ gap: space.xs }}>
        <Text style={[styles.title, { color: palette.text }]}>扫码绑定</Text>
        <Text style={{ color: palette.textMuted, fontSize: font.small }}>
          在电脑上打开「系统 → 局域网连接」，用这里扫那张二维码，手机就自动完成地址与登录授权。
        </Text>
      </View>

      <View style={[styles.cameraBox, { borderColor: palette.border, backgroundColor: palette.card }]}>
        {granted ? (
          <CameraView
            style={StyleSheet.absoluteFill}
            facing="back"
            enableTorch={torch}
            barcodeScannerSettings={{ barcodeTypes: ["qr"] }}
            onBarcodeScanned={(result) => {
              if (result?.data) onScanned(result.data);
            }}
          />
        ) : (
          <View style={styles.cameraEmpty}>
            <Text style={{ color: palette.textMuted, fontSize: font.small, textAlign: "center" }}>
              需要相机权限才能扫码。只用来读二维码，不拍照、不上传。
            </Text>
            <Pressable
              onPress={() => void requestPermission()}
              style={[styles.button, { backgroundColor: palette.accent }]}
            >
              <Text style={styles.buttonText}>允许使用相机</Text>
            </Pressable>
          </View>
        )}
        {busy ? (
          <View style={styles.overlay}>
            <ActivityIndicator color="#fff" />
            <Text style={{ color: "#fff", fontSize: font.small, marginTop: space.xs }}>正在授权…</Text>
          </View>
        ) : null}
      </View>

      {granted ? (
        <Pressable onPress={() => setTorch((v) => !v)} style={[styles.button, { backgroundColor: palette.card }]}>
          <Text style={{ color: palette.text, fontSize: font.small }}>{torch ? "关闭手电筒" : "打开手电筒"}</Text>
        </Pressable>
      ) : null}

      {error ? <Text style={{ color: palette.danger, fontSize: font.small }}>{error}</Text> : null}

      <View style={{ gap: space.sm, marginTop: space.sm }}>
        <Text style={{ color: palette.textMuted, fontSize: font.small }}>或者手动输入（二维码下面就写着这两项）</Text>

        <TextInput
          value={host}
          onChangeText={setHost}
          placeholder="192.168.1.61:8787"
          placeholderTextColor={palette.textFaint}
          autoCapitalize="none"
          autoCorrect={false}
          keyboardType="url"
          editable={!busy}
          style={[styles.input, { color: palette.text, borderColor: palette.border, backgroundColor: palette.card }]}
        />
        <TextInput
          value={code}
          onChangeText={setCode}
          placeholder="配对码，例如 K7QM2XPD"
          placeholderTextColor={palette.textFaint}
          autoCapitalize="characters"
          autoCorrect={false}
          editable={!busy}
          style={[
            styles.input,
            styles.codeInput,
            { color: palette.text, borderColor: palette.border, backgroundColor: palette.card },
          ]}
        />
        <Pressable
          disabled={busy}
          onPress={submitManual}
          style={[styles.button, { backgroundColor: palette.accent, opacity: busy ? 0.6 : 1 }]}
        >
          {busy ? <ActivityIndicator color="#fff" /> : <Text style={styles.buttonText}>绑定并登录</Text>}
        </Pressable>
      </View>

      <Pressable onPress={() => router.replace("/login")} style={styles.back}>
        <Text style={{ color: palette.textMuted, fontSize: font.small }}>返回登录</Text>
      </Pressable>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  title: { fontSize: 26, fontWeight: "700" },
  cameraBox: {
    height: 300,
    borderRadius: radius.md,
    borderWidth: StyleSheet.hairlineWidth,
    overflow: "hidden",
  },
  cameraEmpty: { flex: 1, alignItems: "center", justifyContent: "center", gap: space.md, padding: space.lg },
  overlay: {
    ...StyleSheet.absoluteFillObject,
    alignItems: "center",
    justifyContent: "center",
    backgroundColor: "rgba(0,0,0,0.45)",
  },
  input: {
    borderWidth: StyleSheet.hairlineWidth,
    borderRadius: radius.md,
    paddingHorizontal: space.md,
    paddingVertical: space.md,
    fontSize: font.body,
  },
  codeInput: { letterSpacing: 3, textTransform: "uppercase" },
  button: { borderRadius: radius.md, paddingVertical: space.md, alignItems: "center" },
  buttonText: { color: "#fff", fontSize: font.body, fontWeight: "600" },
  back: { alignItems: "center", paddingVertical: space.md, marginTop: space.sm },
});
