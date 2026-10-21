"use client";

import { useTranslations } from "next-intl";

import { MCPExposureCard } from "@/components/mcp-exposure-card";

// 外部接入：ARTEX 作为 MCP 服务端，给 Codex / Claude Code / Cursor 这类外部 harness 用。
//
// 为什么从 MCP 页拆出来：那一页管的是「ARTEX 去连别人」（作为客户端），
// 这一页管的是「别人来连 ARTEX」（作为服务端）。两者的心智、权限和风险完全不同，
// 混在一页里，用户找「怎么让 Cursor 接进来」根本不会想到去点 MCP。
export default function ExternalAccessPage() {
  const t = useTranslations("externalPage");
  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">{t("title")}</h1>
        <p className="text-muted-foreground text-sm">{t("subtitle")}</p>
      </div>

      <MCPExposureCard />

      <p className="text-muted-foreground text-xs leading-relaxed">{t("hint")}</p>
    </div>
  );
}
