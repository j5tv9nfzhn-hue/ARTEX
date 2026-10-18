import type * as React from "react";

import { cn } from "@/lib/utils";

// EmptyState 空态组件:插画 + 标题 + 说明 + 行动按钮 + 建议 chips。
// 设计口径(与主题重塑一致):
// - 插画一律内联 SVG 线稿,用 currentColor 绘制、主色仅作点缀(stroke=currentColor + className="text-primary"),
//   所有颜色都走主题令牌(muted-foreground / primary),不硬编码色值 —— 亮/暗与预设主题下自动成立;
// - 标题用 .font-display(Space Grotesk 点缀,中文自动沿 body 的 CJK 回退栈);
// - compact 变体给侧栏/窄面板用(更小的插画与间距)。
export type EmptyStateIllustration = "radar" | "nodes" | "terminal" | "chat" | "flag" | "shield";

export interface EmptyStateSuggestion {
  label: string;
  onClick: () => void;
}

// 统一画布 120×72,strokeWidth=2,圆头圆角,保证缩到小尺寸仍清晰。
// 插画一律装饰性:aria-hidden="true" 逐个写在 svg 上(须是 svg 的字面属性,
// 放进 svgProps 常量经 {...spread} 后不参与 biome noSvgWithoutTitle 判定)。
const svgProps = {
  viewBox: "0 0 120 72",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 2,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

// 雷达扫描:侦察 / 流量 / 探索类空态。
function RadarArt(props: React.SVGProps<SVGSVGElement>) {
  return (
    <svg {...svgProps} aria-hidden="true" {...props}>
      <circle cx="60" cy="36" r="24" />
      <circle cx="60" cy="36" r="14" className="text-primary" />
      <path d="M60 36 78 22" className="text-primary" />
      <circle cx="52" cy="24" r="1.6" fill="currentColor" stroke="none" />
      <circle cx="71" cy="45" r="1.6" fill="currentColor" stroke="none" />
      <circle cx="45" cy="47" r="1.6" fill="currentColor" stroke="none" />
      <path d="M22 66h76" className="opacity-50" />
    </svg>
  );
}

// 节点网络:资产 / Agent / MCP 类空态。
function NodesArt(props: React.SVGProps<SVGSVGElement>) {
  return (
    <svg {...svgProps} aria-hidden="true" {...props}>
      <path d="M34 24l52-4" />
      <path d="M36 29l19 18" />
      <path d="M86 25 65 49" />
      <circle cx="30" cy="24" r="6" />
      <circle cx="92" cy="20" r="6" />
      <circle cx="60" cy="54" r="6" className="text-primary" />
      <path d="M22 66h76" className="opacity-50" />
    </svg>
  );
}

// 终端光标:命令执行 / 日志 / 工具 / Skill 类空态。
function TerminalArt(props: React.SVGProps<SVGSVGElement>) {
  return (
    <svg {...svgProps} aria-hidden="true" {...props}>
      <rect x="24" y="16" width="72" height="38" rx="5" />
      <path d="M34 30l6 5-6 5" className="text-primary" />
      <path d="M48 40h18" />
      <path d="M72 40h6" className="text-primary" />
      <path d="M22 66h76" className="opacity-50" />
    </svg>
  );
}

// ARTEX 官方品牌图标（与侧边栏左上角 /logo.png 完全一致，裁切透明留白后居中高清展示）。
function ChatArt(props: React.SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="260 460 1400 980" fill="none" aria-hidden="true" {...props}>
      <image href="/logo.png" x="0" y="0" width="1920" height="1920" />
    </svg>
  );
}

// 旗标:任务 / 目标类空态。
function FlagArt(props: React.SVGProps<SVGSVGElement>) {
  return (
    <svg {...svgProps} aria-hidden="true" {...props}>
      <path d="M44 12v48" />
      <path d="M44 15c10-4 18 4 28 0v20c-10 4-18-4-28 0z" className="text-primary" />
      <path d="M32 60h18" />
      <path d="M22 66h76" className="opacity-50" />
    </svg>
  );
}

// 盾牌:拦截规则 / 审批 / 安全类空态。
function ShieldArt(props: React.SVGProps<SVGSVGElement>) {
  return (
    <svg {...svgProps} aria-hidden="true" {...props}>
      <path d="M60 10l22 7v16c0 14-9 22-22 27-13-5-22-13-22-27V17z" />
      <path d="M51 35l7 7 12-13" className="text-primary" />
      <path d="M22 66h76" className="opacity-50" />
    </svg>
  );
}

const ILLUSTRATIONS: Record<EmptyStateIllustration, React.ComponentType<React.SVGProps<SVGSVGElement>>> = {
  radar: RadarArt,
  nodes: NodesArt,
  terminal: TerminalArt,
  chat: ChatArt,
  flag: FlagArt,
  shield: ShieldArt,
};

export function EmptyState({
  illustration = "radar",
  title,
  description,
  actions,
  suggestions,
  compact = false,
  className,
}: {
  illustration?: EmptyStateIllustration;
  title: React.ReactNode;
  description?: React.ReactNode;
  // 主行动区(通常是 Button / Link),由调用方决定是否展示。
  actions?: React.ReactNode;
  // 可点击的建议 chips:点一下就执行(如填入输入框),让空态"教书"。
  suggestions?: readonly EmptyStateSuggestion[];
  // 侧栏 / 窄面板用:更小的插画与纵向间距。
  compact?: boolean;
  className?: string;
}) {
  const Art = ILLUSTRATIONS[illustration];
  let artClass = "w-24 text-muted-foreground/70";
  if (illustration === "chat") {
    artClass = compact ? "w-20" : "w-32";
  } else if (compact) {
    artClass = "w-16 text-muted-foreground/70";
  }
  return (
    <div
      data-slot="empty-state"
      className={cn(
        "flex w-full min-w-0 flex-col items-center justify-center text-center text-balance",
        compact ? "gap-1.5 px-3 py-6" : "gap-2.5 px-4 py-10",
        className,
      )}
    >
      <Art aria-hidden="true" className={cn("h-auto shrink-0", artClass)} />
      <div className={cn("font-display font-medium tracking-tight", compact ? "text-sm" : "text-base")}>{title}</div>
      {description ? (
        <div className={cn("text-muted-foreground max-w-md", compact ? "text-xs" : "text-sm/relaxed")}>
          {description}
        </div>
      ) : null}
      {actions ? <div className="mt-1 flex flex-wrap items-center justify-center gap-2">{actions}</div> : null}
      {suggestions && suggestions.length > 0 ? (
        <div className="mt-2 flex max-w-lg flex-wrap items-center justify-center gap-1.5">
          {suggestions.map((s) => (
            <button
              key={s.label}
              type="button"
              onClick={s.onClick}
              className="rounded-full border bg-background px-2.5 py-1 text-xs text-muted-foreground transition-colors hover:border-primary/50 hover:bg-accent hover:text-foreground"
            >
              {s.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
