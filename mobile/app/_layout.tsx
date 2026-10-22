import { Stack, useRouter, useSegments } from "expo-router";
import { StatusBar } from "expo-status-bar";
import * as React from "react";
import { ActivityIndicator, View } from "react-native";
import { SafeAreaProvider } from "react-native-safe-area-context";

import { getSession, loadSession } from "@/api/session";
import { ThemeProvider, useTheme } from "@/theme/provider";

function Gate({ children }: { children: React.ReactNode }) {
  const router = useRouter();
  const segments = useSegments();
  const { palette, mode } = useTheme();
  const [ready, setReady] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    void loadSession().finally(() => {
      if (alive) setReady(true);
    });
    return () => {
      alive = false;
    };
  }, []);

  React.useEffect(() => {
    if (!ready) return;
    const onLogin = segments[0] === "login";
    // 扫码绑定页也是「还没有会话」时才能到的页面：没 token 的人必须能进得去，
    // 否则登录页那个「扫码绑定」按钮点了会被这里立刻弹回来。
    const onPair = segments[0] === "pair";
    const hasToken = Boolean(getSession().token) && Boolean(getSession().baseUrl);
    if (!hasToken && !onLogin && !onPair) router.replace("/login");
    if (hasToken && onLogin) router.replace("/(tabs)");
  }, [ready, router, segments]);

  if (!ready) {
    return (
      <View style={{ flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: palette.bg }}>
        <ActivityIndicator color={palette.accent} />
      </View>
    );
  }
  return (
    <>
      <StatusBar style={mode === "dark" ? "light" : "dark"} />
      {children}
    </>
  );
}

export default function RootLayout() {
  return (
    <SafeAreaProvider>
      <ThemeProvider>
        <Gate>
          <Stack screenOptions={{ headerShown: false }} />
        </Gate>
      </ThemeProvider>
    </SafeAreaProvider>
  );
}
