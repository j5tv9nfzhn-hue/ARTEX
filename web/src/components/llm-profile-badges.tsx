"use client";

import { useTranslations } from "next-intl";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

// 输入模态 / 上下文窗口的最小只读展示与链级建议（计划 #147–150 的能力-任务匹配建议）。
// 能力数据来自 effective_input_modalities（声明优先、否则后端按模型名保守推断），
// 即运行时真实生效的能力——不是用户未声明的原始串。context_window_k 与后端
// agent.Config.CompactionWindow 同口径：未设置=200K、取值域 [32K,1M]（见
// agent/provider.go 的 defaultWindowK / minWindowK / maxWindowK），界面上的数字与
// 实际压缩阈值一致。
// 这里只做建议性提示，不做硬阻断：附件与模型能力不匹配时的硬阻断是
// server/modalities.go 的 ErrModalityUnsupported（明确报错，绝不静默丢附件）。

// CapabilityProfile 是结构化最小集：LLMProfile 与 AgentLLMProfileOption 都满足。
export type CapabilityProfile = {
  name: string;
  context_window_k?: number;
  effective_input_modalities?: string;
};

const EXTRA_MODALITIES = ["image", "audio", "pdf"] as const;
type ExtraModality = (typeof EXTRA_MODALITIES)[number];

// 后端默认/边界（agent/provider.go），用于把未配置窗口显示成实际生效值。
const DEFAULT_WINDOW_K = 200;
const MIN_WINDOW_K = 32;
const MAX_WINDOW_K = 1000;

// profileModalities 解析实际生效的「扩展模态」；text 始终隐含，不在这里返回。
export function profileModalities(profile?: CapabilityProfile): ExtraModality[] {
  return (profile?.effective_input_modalities ?? "")
    .split(",")
    .map((m) => m.trim().toLowerCase())
    .filter((m): m is ExtraModality => (EXTRA_MODALITIES as readonly string[]).includes(m));
}

// profileSupportsImage 判断配置实际能否接收图片（聊天上传 / 截图等）。
export function profileSupportsImage(profile?: CapabilityProfile): boolean {
  return profileModalities(profile).includes("image");
}

// profileWindowK 返回实际生效的上下文窗口（K tokens），与后端同口径。
export function profileWindowK(profile?: CapabilityProfile): number {
  const k = profile?.context_window_k ?? 0;
  if (k <= 0) return DEFAULT_WINDOW_K;
  return Math.min(Math.max(k, MIN_WINDOW_K), MAX_WINDOW_K);
}

// ProfileCapabilityBadges 在链编辑器的选项与链行上展示扩展模态徽章 + 显式设置的
// 上下文窗口；两者都没有时不占版面（纯文本配置不显示任何徽章）。
export function ProfileCapabilityBadges({
  profile,
  className,
}: {
  profile?: CapabilityProfile;
  className?: string;
}) {
  const t = useTranslations("llmChain");
  const mods = profileModalities(profile);
  const windowK = profile?.context_window_k ?? 0;
  if (mods.length === 0 && windowK <= 0) return null;
  return (
    <span className={cn("flex min-w-0 shrink-0 items-center gap-1", className)}>
      {mods.includes("image") && (
        <Badge variant="outline" className="px-1 py-0 text-[9px]">
          {t("modality.image")}
        </Badge>
      )}
      {mods.includes("audio") && (
        <Badge variant="outline" className="px-1 py-0 text-[9px]">
          {t("modality.audio")}
        </Badge>
      )}
      {mods.includes("pdf") && (
        <Badge variant="outline" className="px-1 py-0 text-[9px]">
          {t("modality.pdf")}
        </Badge>
      )}
      {windowK > 0 && <span className="text-muted-foreground/80 text-[10px] tabular-nums">{t("windowK", { k: windowK })}</span>}
    </span>
  );
}

// ChainCapabilityAdvice 给出「能力-任务匹配」建议（仅提示，不阻断）：
//   1) 链首不能收图片、而链上其他配置能收 → 需要看图的请求会在链首被明确拒绝
//      （不会静默丢图），把能收图的配置放前面更不容易断；
//   2) 链首窗口小于链上更大窗口 → 长上下文任务会在链首更早触发压缩。
// 两条都在信息确定时提示，避免噪声（相同对象视为同一配置，不与自己比较）。
export function ChainCapabilityAdvice({
  head,
  members,
  className,
}: {
  head?: CapabilityProfile;
  members?: (CapabilityProfile | undefined)[];
  className?: string;
}) {
  const t = useTranslations("llmChain");
  if (!head) return null;
  const others = (members ?? []).filter((m): m is CapabilityProfile => !!m && m !== head);
  const notes: string[] = [];
  if (!profileSupportsImage(head)) {
    const withImage = others.find((m) => profileSupportsImage(m));
    if (withImage) {
      notes.push(t("advice.noImage", { head: head.name, other: withImage.name }));
    }
  }
  const headK = profileWindowK(head);
  const widest = others.reduce<CapabilityProfile | undefined>(
    (best, m) => (!best || profileWindowK(m) > profileWindowK(best) ? m : best),
    undefined,
  );
  if (widest && profileWindowK(widest) > headK) {
    notes.push(t("advice.smallWindow", { head: head.name, k: headK, other: widest.name, otherK: profileWindowK(widest) }));
  }
  if (notes.length === 0) return null;
  return (
    <div className={cn("grid gap-1", className)}>
      {notes.map((n) => (
        <p key={n} className="text-muted-foreground text-xs">
          {n}
        </p>
      ))}
    </div>
  );
}
