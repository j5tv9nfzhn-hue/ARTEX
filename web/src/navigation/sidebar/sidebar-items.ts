import {
  Activity,
  Ban,
  BellRing,
  Bot,
  Brain,
  Bug,
  ClipboardList,
  Database,
  FolderOpen,
  FolderSync,
  HardDrive,
  LayoutDashboard,
  LayoutPanelLeft,
  type LucideIcon,
  MessageSquare,
  Network,
  Plug,
  Radio,
  ScrollText,
  Settings2,
  ShieldAlert,
  Sparkles,
  Target,
  Terminal,
  Wrench,
} from "lucide-react";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

// 组编号对应 nav.group.* 消息键；标签由渲染方经 useTranslations 翻译，
// 不在 sidebar-items.ts 数据中保存。未映射的组回退到 group.<id> 键，
// 渲染方用 t.has 判断后再回退到原始 label。
export const GROUP_MESSAGE_KEY: Record<number, string> = { 1: "function", 2: "system" };

export function navGroupMessageKey(groupId: number): string {
  return GROUP_MESSAGE_KEY[groupId] ? `group.${GROUP_MESSAGE_KEY[groupId]}` : `group.${groupId}`;
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: "功能",
    items: [
      { id: "dashboard", title: "仪表盘", url: "/dashboard", icon: LayoutDashboard },
      { id: "chat", title: "对话", url: "/chat", icon: MessageSquare },
      // 独立前端：/harness 挂在 (standalone) 路由组，不套控制台外壳，所以用新标签打开。
      // 嵌入式 iframe（预览沙箱 sandbox="allow-scripts"，无 allow-popups）里 target="_blank"
      // 会被静默吞掉 → nav-main 检测到不在顶层窗口时退回整页跳转。
      // 图标用三栏面板而不是 Terminal：Terminal 已被「工具执行」占用，两个相邻条目
      // 同图标会让人以为是同一个入口；而且工作台本身就是 左列表 / 中时间线 / 右变更 三栏。
      { id: "harness", title: "Harness 工作台", url: "/harness", icon: LayoutPanelLeft, badge: "new", newTab: true },
      { id: "tasks", title: "任务", url: "/function/tasks", icon: Target },
      { id: "findings", title: "发现", url: "/function/findings", icon: Bug },
      { id: "traffic", title: "流量", url: "/function/traffic", icon: Activity },
      { id: "commands", title: "工具执行", url: "/function/commands", icon: Terminal },
      { id: "llm-records", title: "LLM 录制", url: "/function/llm-records", icon: Radio },
      { id: "assets", title: "资产", url: "/function/assets", icon: Network },
      { id: "sync", title: "资产同步", url: "/function/sync", icon: FolderSync },
      { id: "workspace", title: "工作空间", url: "/function/workspace", icon: FolderOpen },
    ],
  },
  {
    id: 2,
    label: "系统",
    items: [
      { id: "llm", title: "LLM", url: "/system/llm", icon: Brain },
      { id: "agents", title: "Agent", url: "/system/agents", icon: Bot },
      { id: "mcp", title: "MCP", url: "/system/mcp", icon: Plug },
      { id: "skills", title: "Skill", url: "/system/skills", icon: Sparkles },
      { id: "tools", title: "工具", url: "/system/tools", icon: Wrench },
      { id: "notify", title: "通知推送", url: "/system/notify", icon: BellRing },
      { id: "intercept", title: "拦截规则", url: "/system/intercept", icon: ShieldAlert },
      { id: "asset-intercept", title: "资产拦截", url: "/system/intercept/assets", icon: Ban },
      { id: "approvals", title: "审批记录", url: "/system/intercept/approvals", icon: ClipboardList },
      { id: "logs", title: "日志", url: "/system/logs", icon: ScrollText },
      { id: "retention", title: "数据保留", url: "/system/retention", icon: Database },
      { id: "storage", title: "存储与清理", url: "/system/storage", icon: HardDrive },
      { id: "settings", title: "系统配置", url: "/system/settings", icon: Settings2 },
    ],
  },
];
