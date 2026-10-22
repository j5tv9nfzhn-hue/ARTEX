/**
 * Agent 展示状态机 —— 全站唯一真相。
 *
 * 改造前每个页面各写一套 running ? "实时" : ... 的判断，实时帧、落库活动、
 * 工具调用三方各自决定显示什么，于是出现「三个点 → 推理 → 正文 → 又退回推理」
 * 这种来回跳（Memo 01）。这里把状态收敛成一个纯函数：输入活动 + 实时帧，
 * 输出唯一阶段，界面只负责按阶段画。
 */

export type AgentPhase = "idle" | "thinking" | "writing" | "tool" | "waiting" | "error";

export type AgentPhaseInput = {
  /** 会话 / 任务是否处于运行态 */
  running: boolean;
  /** 最近一条实时帧的 kind：thinking | text | tool_use | tool_start | tool_input | progress */
  liveKind?: string;
  /** 最近一条已落库活动的 kind */
  lastKind?: string;
  /** 是否有待处理的审批 */
  waitingApproval?: boolean;
  /** 最近一条活动是否报错 */
  lastIsError?: boolean;
};

/**
 * 优先级：审批等待 > 实时帧 > 运行中无帧 > 错误 > 空闲。
 * 实时帧永远盖过落库活动：流式时以「正在发生的事」为准，历史活动不会把状态拽回去。
 */
export function deriveAgentPhase(input: AgentPhaseInput): AgentPhase {
  if (input.waitingApproval) return "waiting";
  if (input.running && input.liveKind) {
    switch (input.liveKind) {
      case "thinking":
        return "thinking";
      case "text":
        return "writing";
      case "tool_use":
      case "tool_start":
      case "tool_input":
      case "progress":
        return "tool";
      default:
        break;
    }
  }
  if (input.running) return input.lastKind === "thinking" ? "thinking" : "writing";
  if (input.lastIsError) return "error";
  return "idle";
}

/** 阶段 -> i18n key（transcript.phase.*），保证任务页与对话页文案一致。 */
export const PHASE_LABEL_KEY: Record<AgentPhase, string> = {
  idle: "phase.idle",
  thinking: "phase.thinking",
  writing: "phase.writing",
  tool: "phase.tool",
  waiting: "phase.waiting",
  error: "phase.error",
};
