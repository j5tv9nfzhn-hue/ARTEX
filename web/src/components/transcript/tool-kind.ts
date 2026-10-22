import {
  BookOpenIcon,
  FileTextIcon,
  GlobeIcon,
  KeyRoundIcon,
  ListChecksIcon,
  PencilIcon,
  PlugIcon,
  RadarIcon,
  SearchIcon,
  SparklesIcon,
  Terminal,
  Wrench,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

// 工具分类只管三件事：行首图标、类别文案、异常着色。
// 刻意不再做十几色的胶囊角标 —— 摘要行的层级靠「状态圈 + 文案 + 目标路径」表达，
// 颜色只留给失败/拦截这类真正需要注意的行。
export type ToolKindKey =
  | "exec"
  | "read"
  | "search"
  | "edit"
  | "http"
  | "recon"
  | "kb"
  | "crypto"
  | "mcp"
  | "skill"
  | "todo"
  | "tool";

export interface ToolKindMeta {
  key: ToolKindKey;
  Icon: LucideIcon;
  labelKey: `toolKind.${ToolKindKey}`;
}

const KIND_META: Record<ToolKindKey, ToolKindMeta> = {
  exec: { key: "exec", Icon: Terminal, labelKey: "toolKind.exec" },
  read: { key: "read", Icon: FileTextIcon, labelKey: "toolKind.read" },
  search: { key: "search", Icon: SearchIcon, labelKey: "toolKind.search" },
  edit: { key: "edit", Icon: PencilIcon, labelKey: "toolKind.edit" },
  http: { key: "http", Icon: GlobeIcon, labelKey: "toolKind.http" },
  recon: { key: "recon", Icon: RadarIcon, labelKey: "toolKind.recon" },
  kb: { key: "kb", Icon: BookOpenIcon, labelKey: "toolKind.kb" },
  crypto: { key: "crypto", Icon: KeyRoundIcon, labelKey: "toolKind.crypto" },
  mcp: { key: "mcp", Icon: PlugIcon, labelKey: "toolKind.mcp" },
  skill: { key: "skill", Icon: SparklesIcon, labelKey: "toolKind.skill" },
  todo: { key: "todo", Icon: ListChecksIcon, labelKey: "toolKind.todo" },
  tool: { key: "tool", Icon: Wrench, labelKey: "toolKind.tool" },
};

// 显式工具名优先；MCP 前缀与未登记的工具走兜底规则 —— 新增工具不改这张表也能正确分类。
const KIND_BY_TOOL: Record<string, ToolKindKey> = {
  Bash: "exec",
  Read: "read",
  Grep: "search",
  Glob: "search",
  Edit: "edit",
  MultiEdit: "edit",
  Write: "edit",
  apply_patch: "edit",
  http_request: "http",
  http_fuzz: "http",
  http_sequence: "http",
  web_recon: "recon",
  dns_probe: "recon",
  net_probe: "recon",
  tactical_kb: "kb",
  codec_crypto: "crypto",
  extract_data: "crypto",
  Skill: "skill",
  TodoWrite: "todo",
};

export function toolKindFor(tool: string): ToolKindMeta {
  const direct = KIND_BY_TOOL[tool];
  if (direct) return KIND_META[direct];
  if (tool.startsWith("mcp__") || tool.startsWith("mcp_")) return KIND_META.mcp;
  return KIND_META.tool;
}
