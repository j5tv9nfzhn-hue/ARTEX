"use client";

import type { ReactNode } from "react";
import * as React from "react";

import { NextIntlClientProvider } from "next-intl";

import zhMessages from "../../messages/zh.json";
import type { Locale } from "./config";
import { useActiveLocale } from "./locale-store";

// 本仓库只提供简体中文一种界面语言。静态打包消息，设置页切换时无需重建、
// 刷新或异步加载，组件立即重绘，并兼容 output: export。
const MESSAGES: Record<Locale, Record<string, unknown>> = {
  zh: zhMessages as Record<string, unknown>,
};

// 浏览器或预渲染服务器的运行时区。
const TIME_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone;

// IntlRuntimeProvider 按用户 cookie、localStorage 中的有效 locale 提供 messages。
export function IntlRuntimeProvider({ children }: Readonly<{ children: ReactNode }>) {
  const locale = useActiveLocale();

  // 语言切换同步更新 html lang，覆盖服务器预渲染的初始值，
  // 使屏幕阅读器与浏览器翻译识别正确语言。
  React.useEffect(() => {
    document.documentElement.lang = locale;
  }, [locale]);

  // 不继承服务器请求配置，需显式提供 timeZone，
  // 否则 next-intl 预渲染报告 ENVIRONMENT_FALLBACK。
  return (
    <NextIntlClientProvider locale={locale} messages={MESSAGES[locale]} timeZone={TIME_ZONE}>
      {children}
    </NextIntlClientProvider>
  );
}
