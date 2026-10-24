/**
 * 实时流（SSE）帧的「外部 store」——把高频写入与 React 渲染频率解耦。
 *
 * 为什么要有这个文件（2026-10-23 对话页闪烁排查）：
 *
 * 之前 `live` 帧直接 `setLiveDelta(...)` 进 ChatView 的 state，SSE 每 40ms 一帧，
 * 于是**整个对话页**（头部徽标、token 统计、整条 Transcript、分组计算、自动滚动
 * 的 layout effect）每秒被重算 25 次。落库活动与实时帧又在同一棵树里抢位置，
 * 于是出现「三个点的加载动画和思考过程同时显示」「整屏反复闪」。
 *
 * 业界现在的做法（Vercel AI SDK / 各 Agent 平台一致）：**写入频率与渲染频率分开**。
 *   - 写入：SSE 回调只改一个 React 看不到的可变对象（就是这个 store）；
 *   - 渲染：组件通过 useSyncExternalStore 订阅，按帧合并（一帧最多通知一次）；
 *   - 稳定快照：getSnapshot 只在内容真的变了时返回新引用，否则返回同一个对象，
 *     否则 React 会因为 Object.is 不相等而无限重渲染。
 *
 * 这个文件保持**零 React 依赖**（纯函数 + 闭包），所以可以直接在 node 里跑单测
 * （见 live-stream-store.test.mjs）；订阅用的 hooks 在 live-stream.ts。
 */

/** 一帧实时内容：`text` 是累计全文（服务端每帧给整段，不是增量），渲染时整体替换。 */
export type LiveFrame = {
  kind: string;
  text?: string;
  tool?: string;
  worker?: string;
};

/** 调度器：默认按动画帧合并；单测里注入同步实现。 */
export type LiveScheduler = (notify: () => void) => void;

export type LiveStream = {
  /** useSyncExternalStore 的 subscribe：必须是稳定引用（挂在实例上，天然稳定）。 */
  subscribe: (listener: () => void) => () => void;
  /** 当前帧；没有帧时返回 null。内容不变时返回同一个引用。 */
  getSnapshot: () => LiveFrame | null;
  /** SSR 时没有实时流，恒为 null。 */
  getServerSnapshot: () => null;
  /** 写入一帧（累计全文）。与当前帧完全相同则直接丢弃，不触发通知。 */
  push: (frame: LiveFrame) => void;
  /** 清空（落库接管 / 本轮结束 / 切换会话）。 */
  clear: () => void;
};

/**
 * 通知频率上限：40ms ≈ 25fps。
 *
 * 推理流是最密的一条（一行一行往外吐），rAF 合并只能保证「一帧最多一次通知」，在
 * 高刷屏上仍是每秒 60~120 次重排。文本比这快没有任何信息增量，只会把主线程占满，
 * 让高度变化排队——观感就是越写越抽。这里在 rAF 之外再加一道下限：距上次通知不足
 * 40ms 的写入合并成一次尾随通知（读到的一定是最新快照，不会丢内容）。
 */
const MIN_NOTIFY_MS = 40;

function throttleSchedule(scheduler: LiveScheduler, minMs: number): LiveScheduler {
  let last = 0;
  let timer: ReturnType<typeof setTimeout> | null = null;
  return (notify) => {
    const wait = minMs - (Date.now() - last);
    if (wait <= 0) {
      last = Date.now();
      scheduler(notify);
      return;
    }
    if (timer !== null) return; // 已有尾随通知在排队：它读的是最新快照
    timer = setTimeout(() => {
      timer = null;
      last = Date.now();
      notify();
    }, wait);
  };
}

function defaultSchedule(notify: () => void): void {
  if (typeof requestAnimationFrame === "function") {
    requestAnimationFrame(() => notify());
    return;
  }
  setTimeout(notify, 16);
}

function sameFrame(a: LiveFrame | null, b: LiveFrame | null): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  return a.kind === b.kind && a.text === b.text && a.tool === b.tool && a.worker === b.worker;
}

/**
 * 创建一条实时流。
 *
 * @param scheduler 合并调度器（默认 requestAnimationFrame；测试可注入同步实现）
 */
export function createLiveStream(
  scheduler: LiveScheduler = throttleSchedule(defaultSchedule, MIN_NOTIFY_MS),
): LiveStream {
  let frame: LiveFrame | null = null;
  let scheduled = false;
  const listeners = new Set<() => void>();

  const flush = () => {
    scheduled = false;
    // 复制一份再遍历：监听器在回调里退订（组件卸载）不会打乱迭代。
    for (const listener of [...listeners]) listener();
  };

  const schedule = () => {
    if (scheduled) return;
    scheduled = true;
    scheduler(flush);
  };

  return {
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    getSnapshot: () => frame,
    getServerSnapshot: () => null,
    push(next) {
      if (sameFrame(frame, next)) return;
      frame = next;
      schedule();
    },
    clear() {
      if (frame === null) return;
      frame = null;
      schedule();
    },
  };
}

/** 实时帧的「家族」：正文/推理互斥，工具单独一族。 */
const PROSE_KINDS = new Set(["thinking", "text"]);

/**
 * liveSettledBy 判定一帧实时内容是否已被落库活动接管。
 *
 * 接管后必须撤掉实时块，否则同一段内容会出现两份（实时块在底部、落库块在上面），
 * 而且两份的高度不一样，滚动条会来回跳——这就是「区域性闪烁」的主要来源之一。
 *
 * 比早期版本多处理的两种情况：
 *   - 推理帧遇到「正文/最终结果」落库：模型已经往下走了，推理块留着就是过期内容；
 *   - 正文帧遇到「工具调用」落库：说明这段正文已经写完了（工具是下一段）。
 * usage / round 是统计与分隔行，不接管任何实时内容。
 */
export function liveSettledBy(liveKind: string, activityKind: string): boolean {
  if (!liveKind) return false;
  if (activityKind === "usage" || activityKind === "round") return false;
  const liveIsTool =
    liveKind === "tool_use" || liveKind === "tool_start" || liveKind === "tool_input" || liveKind === "progress";
  if (liveIsTool) return activityKind === "tool_use" || activityKind === "tool_result";
  if (PROSE_KINDS.has(liveKind)) {
    return PROSE_KINDS.has(activityKind) || activityKind === "result" || activityKind === "tool_use";
  }
  return false;
}
