"use client";

import * as React from "react";

import { useTranslations } from "next-intl";

import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";

// 聊天发送、换行按键是浏览器本地偏好，只存 localStorage，不与账户同步。
// 更换浏览器需重新设置。issue #39：0.3.2 从 Ctrl+Enter 改为 Enter，
// 在此提供旧按键组合选项，默认操作习惯保持不变。
export type ChatSendMode = "enter" | "ctrl-enter";

export const CHAT_SEND_MODE_KEY = "artex_chat_send_mode";
export const DEFAULT_CHAT_SEND_MODE: ChatSendMode = "enter";

// 发送模式下拉选项由 Hook 提供本地化标签，不在纯模块中硬编码。
// value 是稳定标识，用于 localStorage 与行为判断，不能翻译。
export function useChatSendModeOptions(): { value: ChatSendMode; label: string }[] {
  const t = useTranslations("chatSendMode.option");
  return [
    { value: "enter", label: t("enter") },
    { value: "ctrl-enter", label: t("ctrlEnter") },
  ];
}

function parseMode(raw: string | null): ChatSendMode {
  return raw === "ctrl-enter" || raw === "enter" ? raw : DEFAULT_CHAT_SEND_MODE;
}

// storage 事件只通知其他标签页；本页修改后需 emit 通知同页输入框，
// 避免必须刷新才能生效。
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  window.addEventListener("storage", listener);
  return () => {
    listeners.delete(listener);
    window.removeEventListener("storage", listener);
  };
}

// 返回字符串字面量，使 Object.is 按值比较，避免 useSyncExternalStore 无限循环。
function getSnapshot(): ChatSendMode {
  return parseMode(getLocalStorageValue(CHAT_SEND_MODE_KEY));
}

// 服务器无 localStorage，先使用默认值，水合后由 getSnapshot 校正。
function getServerSnapshot(): ChatSendMode {
  return DEFAULT_CHAT_SEND_MODE;
}

export function useChatSendMode(): ChatSendMode {
  return React.useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

export function setChatSendMode(mode: ChatSendMode) {
  setLocalStorageValue(CHAT_SEND_MODE_KEY, mode);
  for (const listener of listeners) listener();
}

// shouldSubmitOnKey 判断按键是否应发送。isComposing 或 keyCode=229 表示 IME 正在组字，
// 中文等输入法必须跳过发送，否则确认字符的 Enter 会误发送。
// enter 模式仅排除 Shift，保持 0.3.2 行为；ctrl-enter 同时支持 Ctrl 与 macOS Cmd。
export function shouldSubmitOnKey(e: React.KeyboardEvent, mode: ChatSendMode): boolean {
  if (e.key !== "Enter") return false;
  if (e.nativeEvent.isComposing || e.nativeEvent.keyCode === 229) return false;
  if (mode === "ctrl-enter") return e.ctrlKey || e.metaKey;
  return !e.shiftKey;
}
