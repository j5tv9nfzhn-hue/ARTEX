// Harness 视图模型：把后端 Activity 投影成时间线项（Item）。
//
// 参照 Codex App Server 的 Item 语义：一个 Activity 落库后才产出「已完成」的项；
// 运行中的项（乐观用户消息、正在执行的工具）由 use-harness 本地补齐。
// 这里只做纯映射，不碰网络，方便单测与复用。

import type { Activity } from "@/lib/types";

export type PermissionMode = "plan" | "edit" | "auto";

/** 逐行审查时攒下来的评论，发送时拼进下一条消息。 */
export interface HarnessComment {
  path: string;
  line: number;
  text: string;
}

export interface BaseItem {
  /** 稳定 key：seq 一旦确定就不再变，避免列表重排时丢失展开态。 */
  id: string;
  seq: number;
  /** HH:MM，直接取 Activity.ts 的时分。 */
  at: string;
}

export interface UserItem extends BaseItem {
  type: "user";
  text: string;
}

export interface AgentItem extends BaseItem {
  type: "agent";
  text: string;
}

export interface ReasoningItem extends BaseItem {
  type: "reasoning";
  text: string;
}

export interface ToolItem extends BaseItem {
  type: "tool";
  tool: string;
  /** 一行摘要，即工具调用的参数预览。 */
  args: string;
  /** 输出正文；未返回时为空串。 */
  output: string;
  isError: boolean;
  done: boolean;
  toolUseId?: string;
  /** 若输出里夹带 unified diff，抽出来单独渲染。 */
  patch?: string;
}

export interface DiffItem extends BaseItem {
  type: "diff";
  path: string;
  patch: string;
}

export interface ResultItem extends BaseItem {
  type: "result";
  tokens: number;
  isError: boolean;
}

export interface NoticeItem extends BaseItem {
  type: "notice";
  text: string;
  kind: string;
}

export type HarnessItem = UserItem | AgentItem | ReasoningItem | ToolItem | DiffItem | ResultItem | NoticeItem;

/** 计划步骤：由 intent 活动推导，不额外建表。 */
export interface PlanStep {
  id: string;
  title: string;
  status: "done" | "active" | "todo";
}

const EDIT_TOOL = /(write|edit|patch|replace|apply|create|mkdir)/i;

export function isEditTool(name: string | undefined): boolean {
  return typeof name === "string" && EDIT_TOOL.test(name);
}

/** extractPatch 从工具输出里认出 unified diff；认不出返回 undefined。 */
export function extractPatch(text: string | undefined): string | undefined {
  if (!text) return undefined;
  const trimmed = text.trimStart();
  if (trimmed.startsWith("diff --git") || trimmed.startsWith("--- ") || trimmed.startsWith("@@ ")) {
    return text;
  }
  const idx = text.indexOf("\n@@ ");
  return idx >= 0 ? text.slice(idx + 1) : undefined;
}

/** 第一个非空字符串。空串按缺失处理 —— 后端 detail 常为空串，不能当正文。 */
function firstText(...vals: Array<string | undefined>): string {
  for (const v of vals) if (v) return v;
  return "";
}

/** 一行摘要里可能出现换行，压平成单行再交给 ToolRow 截断。 */
function flat(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

export function mapActivity(a: Activity): HarnessItem | null {
  const id = `s${a.seq}`;
  const at = (a.ts ?? "").slice(11, 16);
  const base: BaseItem = { id, seq: a.seq, at };

  switch (a.kind) {
    case "user":
      return { ...base, type: "user", text: firstText(a.detail, a.summary) };
    case "text":
      return { ...base, type: "agent", text: firstText(a.detail, a.summary) };
    case "thinking":
      return { ...base, type: "reasoning", text: firstText(a.detail, a.summary) };
    // intent 是计划项（右栏「计划」），usage 是累计 token 计数：都不该在时间线里重复出现。
    case "intent":
    case "usage":
      return null;
    case "tool_use":
      return {
        ...base,
        type: "tool",
        tool: a.tool ?? "tool",
        args: flat(a.summary ?? ""),
        output: "",
        isError: false,
        done: false,
        toolUseId: a.tool_use_id,
      };
    case "tool_result": {
      const output = firstText(a.detail, a.summary);
      return {
        ...base,
        type: "tool",
        tool: a.tool ?? "tool",
        args: "",
        output,
        isError: Boolean(a.is_error),
        done: true,
        toolUseId: a.tool_use_id,
        patch: extractPatch(output),
      };
    }
    case "result":
      return {
        ...base,
        type: "result",
        tokens: (a.input_tokens ?? 0) + (a.output_tokens ?? 0),
        isError: Boolean(a.is_error),
      };
    default:
      return { ...base, type: "notice", text: a.summary || a.kind, kind: a.kind };
  }
}

/**
 * buildPlan 把 intent 活动折叠成计划清单。
 *
 * 同一 intent_id 可能连续出现多条（重试/续写），这里按首次出现排序、用最后一条的
 * summary 作为文案；除最后一条外都视为已完成 —— 后端目前没有显式 todo 表，
 * 这是与现有数据一致的最接近表达。
 */
export function buildPlan(activities: Activity[]): PlanStep[] {
  const order: string[] = [];
  const latest = new Map<string, string>();

  for (const a of activities) {
    if (a.kind !== "intent" || !a.intent_id) continue;
    const key = a.intent_id;
    if (!latest.has(key)) order.push(key);
    const title = flat(firstText(a.summary, a.detail));
    if (title) latest.set(key, title);
  }

  return order.map((key, i) => ({
    id: key,
    title: latest.get(key) || key,
    status: i === order.length - 1 ? "active" : "done",
  }));
}
