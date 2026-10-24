"use client";

import * as React from "react";

import { api } from "@/lib/api";
import type { ShellCapability } from "@/lib/types";

/**
 * 读取「本机实际用哪个 Shell」（/health.shell）。
 *
 * 三点约定：
 *   - 模块级缓存：整个页面只打一次 /health，几十个工具行共享同一个结果；
 *   - 失败即静默降级：拿不到就返回 null，由 shellDisplayName 退回中性的 Terminal，
 *     绝不因为探测失败把 UI 变红或抛错；
 *   - 水合安全：首帧一律 null（与服务端渲染一致），拿到数据后自然更新，
 *     不会触发 hydration mismatch。
 */

let cache: ShellCapability | null | undefined; // undefined = 还没请求过
let inflight: Promise<ShellCapability | null> | null = null;

function load(): Promise<ShellCapability | null> {
  if (cache !== undefined) return Promise.resolve(cache);
  if (!inflight) {
    inflight = api
      .health()
      .then((r) => {
        cache = r?.shell ?? null;
        return cache;
      })
      .catch(() => {
        cache = null;
        return null;
      })
      .finally(() => {
        inflight = null;
      });
  }
  return inflight;
}

export function useShellCapability(): ShellCapability | null {
  const [shell, setShell] = React.useState<ShellCapability | null>(cache ?? null);

  React.useEffect(() => {
    if (cache !== undefined) {
      setShell(cache ?? null);
      return;
    }
    let alive = true;
    void load().then((s) => {
      if (alive) setShell(s);
    });
    return () => {
      alive = false;
    };
  }, []);

  return shell;
}
