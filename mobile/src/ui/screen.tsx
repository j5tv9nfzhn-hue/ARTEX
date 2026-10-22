import * as React from "react";
import { RefreshControl, ScrollView, StyleSheet, Text, View } from "react-native";

import { useTheme } from "@/theme/provider";
import { font, space } from "@/theme/tokens";

type ScreenProps = {
  children: React.ReactNode;
  refreshing?: boolean;
  onRefresh?: () => void;
  /** 出错时显示在顶部，而不是把整屏清空——手机上看到旧数据比看到白屏有用。 */
  error?: string | null;
  footer?: string;
};

export function Screen({ children, refreshing, onRefresh, error, footer }: ScreenProps) {
  const { palette } = useTheme();
  return (
    <ScrollView
      style={{ backgroundColor: palette.bg }}
      contentContainerStyle={styles.content}
      refreshControl={
        onRefresh ? (
          <RefreshControl
            refreshing={Boolean(refreshing)}
            onRefresh={onRefresh}
            tintColor={palette.accent}
            colors={[palette.accent]}
          />
        ) : undefined
      }
    >
      {error ? (
        <View style={[styles.banner, { backgroundColor: palette.accentSoft, borderColor: palette.danger }]}>
          <Text style={{ color: palette.danger, fontSize: font.small }}>{error}</Text>
        </View>
      ) : null}
      {children}
      {footer ? (
        <Text style={[styles.footer, { color: palette.textFaint }]}>{footer}</Text>
      ) : null}
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  content: { padding: space.lg, paddingBottom: space.xxl, gap: space.md },
  banner: { borderWidth: 1, borderRadius: 12, padding: space.md },
  footer: { fontSize: font.tiny, textAlign: "center", marginTop: space.md },
});
