import type { Activity } from "@/lib/types";

// 工具行在界面上只有这几种状态：ZCode 的「运行中 / 运行中带确定进度」在这里合并成
// running —— 进度是它的属性（progress 字段），不是另一个状态。
export type ToolStatus = "running" | "completed" | "failed" | "denied" | "stopped";

// 历史行没有 metadata，只能靠文案回退。这两组标记和后端产出的原文一一对应：
//   intercept.defaultMessage → 「拦截规则 [名字] 不允许执行此工具」
//   capture 的停止兜底      → 「已停止：会话结束，未收到执行结果」
//   norma harness 的拦截包装  → 「Blocked by hook: 【ARTEX 平台管控…】」/「Tool call was not permitted: …」
//   后两条只有新数据才带 metadata.tool_status=denied，老行靠这里认出来。
const DENIED_MARKERS = [
  "拦截规则 [",
  "不允许执行此工具",
  "要求用户审批，请稍候",
  "已拒绝",
  "Blocked by hook:",
  "Tool call was not permitted",
  "【ARTEX 平台管控",
];
const STOPPED_MARKERS = ["已停止", "未收到执行结果", "执行前被取消"];

export interface ToolStatusInput {
  result?: Pick<Activity, "summary" | "is_error" | "metadata"> | null;
}

// resolveToolStatus 的判定顺序是刻意的：
//   1. 后端显式标注的 metadata.tool_status 最优先 —— 中断、拦截这类结论只有后端知道，
//      前端不该靠猜；
//   2. 没有结果 → running（行还在跑，这是唯一的进行态）；
//   3. 文案命中拦截/停止标记 → denied / stopped（老数据没有 metadata 时的回退）；
//   4. 其余按 is_error 分 failed / completed。
export function resolveToolStatus({ result }: ToolStatusInput): ToolStatus {
  if (!result) return "running";
  const explicit = result.metadata?.tool_status;
  if (explicit === "completed" || explicit === "failed" || explicit === "denied" || explicit === "stopped") {
    return explicit;
  }
  const text = result.summary ?? "";
  if (DENIED_MARKERS.some((marker) => text.includes(marker))) return "denied";
  if (STOPPED_MARKERS.some((marker) => text.includes(marker))) return "stopped";
  return result.is_error ? "failed" : "completed";
}

// 状态文案的 key 用 as const 固定成字面量联合，t() 才能拿到 next-intl 的类型检查。
export const TOOL_STATUS_LABEL_KEY = {
  running: "toolStatus.running",
  completed: "toolStatus.completed",
  failed: "toolStatus.failed",
  denied: "toolStatus.denied",
  stopped: "toolStatus.stopped",
} as const;

// formatElapsed 把毫秒折成行尾的时长文案。只到秒：工具调用很少跨小时，
// 多出来的精度对「这行跑了多久」没有信息量。
export function formatElapsed(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "";
  const total = Math.round(ms / 1000);
  if (total < 60) return `${total}s`;
  const minutes = Math.floor(total / 60);
  return `${minutes}m ${String(total % 60).padStart(2, "0")}s`;
}
