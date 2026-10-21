import { useTranslations } from "next-intl";

import { HarnessTargetsCard } from "@/components/harness-targets-card";
import { MCPExposureCard } from "@/components/mcp-exposure-card";

// 外部接入：ARTEX 作为 MCP 服务端，给 Codex / Claude Code / Cursor 这类外部 harness 用。
//
// 页面上两件事：先「扫描到谁、一键写进去」（HarnessTargetsCard），
// 再「没扫到的、或想手工配的，抄这段」（MCPExposureCard）。
export default function ExternalAccessPage() {
  const t = useTranslations("externalPage");
  return (
    <div className="flex flex-1 flex-col gap-4 md:gap-6">
      <div>
        <h1 className="font-semibold text-xl tracking-tight">{t("title")}</h1>
        <p className="text-muted-foreground text-sm">{t("subtitle")}</p>
      </div>

      <HarnessTargetsCard />

      <MCPExposureCard />

      <p className="text-muted-foreground text-xs leading-relaxed">{t("hint")}</p>
    </div>
  );
}
