"use client";

import * as React from "react";

import Link from "next/link";

import { ArrowUpCircleIcon } from "lucide-react";

import { api } from "@/lib/api";
import type { UpdateStatus } from "@/lib/types";

/**
 * 顶栏的新版本红点。
 *
 * 只读后端缓存（/api/update/status 不发起任何网络请求），因此这个组件挂在每个
 * 页面的头部也不会替用户去问 GitHub：自动检测关掉时它永远拿到 checked=false，
 * 自然不会亮。也没必要轮询得太勤——后端 6 小时才查一次。
 */
export function UpdateBadge() {
  const [status, setStatus] = React.useState<UpdateStatus | null>(null);

  React.useEffect(() => {
    let alive = true;
    const load = () => {
      api
        .updateStatus()
        .then((s) => {
          if (alive) setStatus(s);
        })
        .catch(() => undefined);
    };
    load();
    // 5 分钟一次纯内存读取，足够跟上 6 小时的检测节奏，又不会在长时间开着的
    // 页面上一直显示过期的红点。
    const id = window.setInterval(load, 5 * 60 * 1000);
    return () => {
      alive = false;
      window.clearInterval(id);
    };
  }, []);

  if (!status) return null;
  const hasUpdate = !!status.has_update;
  const staged = !!status.staged_version && status.staged_version !== status.current;
  if (!hasUpdate && !staged) return null;

  return (
    <Link
      href="/system/settings"
      title={staged ? `v${status.staged_version} 已下载，重启后生效` : `发现新版本 v${status.latest}`}
      className="inline-flex items-center gap-1 rounded-full border border-emerald-500/40 bg-emerald-500/10 px-2 py-0.5 font-medium text-[11px] text-emerald-600 transition-colors hover:bg-emerald-500/20 dark:text-emerald-400"
    >
      <ArrowUpCircleIcon className="size-3" />
      {staged ? "重启生效" : "有新版本"}
    </Link>
  );
}
