// Harness 斜杠命令表与匹配逻辑。
//
// 对标 Claude Code / Codex 的 `/` 命令：只列这个工作台**真的能执行**的动作，
// 不做"点了没反应"的占位项。纯函数、无依赖，直接被 node:test 覆盖。

export interface HarnessCommand {
  /** 稳定 id，页面按它分发动作。 */
  id: string;
  /** 命令词，不含斜杠。 */
  name: string;
  /** 中文名，列表左侧显示。 */
  label: string;
  /** 一句话说明：做了什么、边界在哪。 */
  hint: string;
  group: string;
  /** 额外检索词，空格分隔。 */
  keys?: string;
}

export const HARNESS_COMMANDS: HarnessCommand[] = [
  {
    id: "new",
    name: "new",
    label: "新建会话",
    hint: "在当前 Agent 下开一条新会话",
    group: "会话",
    keys: "create conv 新 session",
  },
  {
    id: "stop",
    name: "stop",
    label: "停止本轮",
    hint: "中断正在运行的 turn（已产生的步骤保留）",
    group: "会话",
    keys: "interrupt abort 中断",
  },
  {
    id: "rename",
    name: "rename",
    label: "重命名会话",
    hint: "给当前会话起一个能认出来的标题",
    group: "会话",
    keys: "title 标题",
  },
  {
    id: "model",
    name: "model",
    label: "切换模型",
    hint: "改当前会话使用的 LLM 配置（下一轮生效）",
    group: "配置",
    keys: "llm profile 模型",
  },
  {
    id: "perm",
    name: "perm",
    label: "切换权限档",
    hint: "计划 / 编辑 / 自动，影响下一条消息附带的执行约束",
    group: "配置",
    keys: "permission plan edit auto 权限",
  },
  {
    id: "changes",
    name: "changes",
    label: "打开变更",
    hint: "右侧面板：本会话产生过的 diff",
    group: "面板",
    keys: "diff 改动",
  },
  {
    id: "plan",
    name: "plan",
    label: "打开计划",
    hint: "右侧面板：agent 的 TodoWrite 清单",
    group: "面板",
    keys: "todo 待办",
  },
  {
    id: "files",
    name: "files",
    label: "打开文件",
    hint: "右侧面板：工作区目录",
    group: "面板",
    keys: "workspace 工作区",
  },
  {
    id: "rollback",
    name: "rollback",
    label: "撤回上一条",
    hint: "隐藏你最后一条消息及其后的步骤，原文回到输入框",
    group: "会话",
    keys: "undo revert 撤销",
  },
  {
    id: "copy",
    name: "copy",
    label: "复制会话链接",
    hint: "把 /harness?conv=<id> 复制到剪贴板",
    group: "工具",
    keys: "link url 链接",
  },
  {
    id: "status",
    name: "status",
    label: "查看状态",
    hint: "运行状态、传输通道、token 预算与模型",
    group: "工具",
    keys: "info 状态",
  },
  {
    id: "help",
    name: "help",
    label: "命令帮助",
    hint: "展开全部命令与快捷键",
    group: "工具",
    keys: "? 帮助",
  },
];

export interface SlashToken {
  /** 斜杠在输入串里的下标。 */
  start: number;
  /** 斜杠之后、光标之前的查询串。 */
  query: string;
}

/**
 * slashToken 判断光标是否处在一个 `/` 命令词里。
 *
 * 只有位于行首或空白之后、且不含空格的 `/xxx` 才算命令 —— 否则
 * "路径 1/2"、URL 里的斜杠会被误判成命令。
 */
export function slashToken(value: string, caret: number): SlashToken | null {
  const before = value.slice(0, caret);
  const start = before.lastIndexOf("/");
  if (start < 0) return null;
  if (start > 0 && !/[\s]/.test(before[start - 1])) return null;
  const query = before.slice(start + 1);
  if (/\s/.test(query) || query.length > 24) return null;
  return { start, query };
}

/** fuzzyScore 做子序列匹配：-1 表示不匹配，分数越大越靠前。 */
export function fuzzyScore(haystack: string, query: string): number {
  const text = haystack.toLowerCase();
  const q = query.toLowerCase().trim();
  if (!q) return 1;
  if (text === q) return 1000;
  if (text.startsWith(q)) return 800 - text.length;
  let at = 0;
  let score = 0;
  let streak = 0;
  for (const ch of q) {
    const next = text.indexOf(ch, at);
    if (next < 0) return -1;
    streak = next === at ? streak + 1 : 0;
    score += 10 + streak * 5 - Math.min(next - at, 20);
    at = next + 1;
  }
  return score;
}

/** matchCommands 按命令词 / 中文名 / 关键词打分排序，空查询返回全部。 */
export function matchCommands(query: string, commands: HarnessCommand[] = HARNESS_COMMANDS): HarnessCommand[] {
  const q = query.trim();
  if (!q) return commands;
  const scored: Array<{ cmd: HarnessCommand; score: number }> = [];
  for (const cmd of commands) {
    const scores = [fuzzyScore(cmd.name, q), fuzzyScore(cmd.label, q), fuzzyScore(cmd.keys ?? "", q)];
    const best = Math.max(...scores);
    if (best >= 0) scored.push({ cmd, score: best });
  }
  scored.sort((a, b) => b.score - a.score);
  return scored.map((s) => s.cmd);
}
