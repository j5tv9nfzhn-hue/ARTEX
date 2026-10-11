"use client";

import * as React from "react";

import { setClientCookie } from "@/lib/cookie.client";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

import { isLocale, LOCALE_COOKIE, LOCALE_STORAGE_KEY, type Locale, normalizeLocale, resolveLocale } from "./config";

// UI 语言是浏览器本地偏好，存于 cookie、localStorage，不与账户同步。
// 更换浏览器或清除站点数据后使用默认语言；模型输出语言另存服务器设置。

function parseLocale(raw: string | null): Locale {
  return normalizeLocale(raw) ?? resolveLocale();
}

// 同标签页订阅者集合。storage 事件仅在其他标签页触发，
// 本页切换后需 emit 通知订阅者立即重绘。
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

function emit() {
  for (const listener of listeners) listener();
}

function getSnapshot(): Locale {
  return parseLocale(getLocalStorageValue(LOCALE_STORAGE_KEY));
}

// 服务器没有 localStorage，先渲染构建默认 locale，
// 水合后 getSnapshot 恢复用户选择。
function getServerSnapshot(): Locale {
  return resolveLocale();
}

// 订阅当前显示语言，变化时组件重新渲染。
export function useActiveLocale(): Locale {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

// 写入 cookie、localStorage，并通知同标签页订阅者；
// cookie 保留一年，下次访问恢复选择。
export function setActiveLocale(locale: Locale) {
  if (!isLocale(locale)) return;
  setLocalStorageValue(LOCALE_STORAGE_KEY, locale);
  setClientCookie(LOCALE_COOKIE, locale, 365);
  emit();
}
