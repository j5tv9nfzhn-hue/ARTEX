"use client";

import type { ReactNode } from "react";
import * as React from "react";

import { auth } from "@/lib/auth";

// 独立前端布局：不套控制台外壳（侧栏、顶栏、通知气泡），整页就是功能本身。
// 侧栏「Harness 工作台」用 target="_blank" 打开新标签，落到这里；
// 直接粘 URL 访问也是同一套界面，不会突然长出控制台边框。
//
// 登录守卫沿用 (main)/layout.tsx 的思路：客户端确认有 token 再渲染，
// 避免未登录时先把工作台的接口请求打出去。
export default function StandaloneLayout({ children }: Readonly<{ children: ReactNode }>) {
  const [authed, setAuthed] = React.useState(false);

  React.useEffect(() => {
    if (auth.getToken()) {
      setAuthed(true);
      return;
    }
    // 未登录：清掉可能残留的 cookie，交给 proxy 送去登录页，避免来回弹跳。
    auth.clearToken();
    window.location.href = "/login";
  }, []);

  if (!authed) return null;

  return <main className="h-dvh w-full overflow-hidden bg-background">{children}</main>;
}
