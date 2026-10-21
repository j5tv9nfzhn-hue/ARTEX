"use client";

import * as React from "react";

import Link from "next/link";

import { BotIcon, LayersIcon, ShieldCheckIcon, SparklesIcon, TargetIcon } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { APP_CONFIG } from "@/config/app-config";
import { api } from "@/lib/api";

/**
 * 项目简介与架构概览。
 *
 * 纯静态内容，不参与更新检测——以前这张卡片叫 update-card，实际却只显示版本号，
 * 更新功能一上线就容易被误读成"这里应该有更新按钮"，所以拆开：版本与更新见
 * version 卡片（同目录 update-card.tsx），这里是产品说明。
 */
export function AboutCard() {
  const [version, setVersion] = React.useState(APP_CONFIG.version);

  React.useEffect(() => {
    let alive = true;
    api
      .health()
      .then((h) => {
        const v = (h.version ?? "").replace(/^v(?=\d)/, "").trim();
        if (alive && v) setVersion(v);
      })
      .catch(() => undefined);
    return () => {
      alive = false;
    };
  }, []);

  return (
    // 设置页是多列瀑布流布局，卡片自己负责行间距并禁止跨列断开（见 page.tsx 的注释）。
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <LayersIcon className="size-4" />
          项目简介与架构概览
        </CardTitle>
        <CardDescription>
          <b>ARTEX-REA</b> 是一套由大语言模型驱动的自主渗透测试与逆向工程协同平台，融合图驱动探索、证据链复测闭环与 AI
          自我进化技能沉淀。
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <Badge variant="secondary" className="font-mono">
            {APP_CONFIG.name}
          </Badge>
          <Badge variant="outline" className="font-mono">
            v{version}
          </Badge>
          <Badge variant="outline">Planner · Worker · MainAgent</Badge>
          <Badge variant="outline" className="font-mono">
            AGPL-3.0
          </Badge>
        </div>

        <div className="grid grid-cols-1 gap-2 text-muted-foreground text-xs sm:grid-cols-2">
          <div className="rounded-md border bg-muted/30 p-2.5">
            <div className="mb-0.5 font-medium text-foreground">图驱动渗透与复测</div>
            <div>Planner 拆解意图、Worker 并发取证，结合 HTTP 流量快照完成可复现漏洞验证。</div>
          </div>
          <div className="rounded-md border bg-muted/30 p-2.5">
            <div className="mb-0.5 font-medium text-foreground">软件逆向工程（REA）</div>
            <div>契约化接入 Ghidra / IDA 等静态分析工具，统一归档符号、控制流与反编译证据。</div>
          </div>
          <div className="rounded-md border bg-muted/30 p-2.5">
            <div className="mb-0.5 font-medium text-foreground">AI 自主技能沉淀</div>
            <div>从终态任务自动提炼复盘 Skill，经人工审阅门控与灰度度量后反哺后续同类任务。</div>
          </div>
          <div className="rounded-md border bg-muted/30 p-2.5">
            <div className="mb-0.5 font-medium text-foreground">纵深安全与合规门禁</div>
            <div>内置命令风险拦截、资产范围校验与人工审批工作流，确保自动化作业边界可控。</div>
          </div>
        </div>

        <div className="flex flex-wrap gap-2">
          <Button asChild variant="outline" size="sm">
            <Link href="/function/tasks">
              <TargetIcon className="size-4" />
              任务工作台
            </Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href="/system/agents">
              <BotIcon className="size-4" />
              Agent 编排
            </Link>
          </Button>
          <Button asChild variant="outline" size="sm">
            <Link href="/system/skills">
              <SparklesIcon className="size-4" />
              技能库与沉淀
            </Link>
          </Button>
          <Button asChild variant="ghost" size="sm">
            <Link href="/system/intercept">
              <ShieldCheckIcon className="size-4" />
              拦截与合规
            </Link>
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
