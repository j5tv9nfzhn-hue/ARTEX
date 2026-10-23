import { createLiveStream, liveSettledBy } from "./live-stream-store.ts";
import assert from "node:assert/strict";
import test from "node:test";

/**
 * 用可控调度器替换 requestAnimationFrame：每次 push 只登记一次 flush，
 * 由测试自己决定何时「到帧」——这样才能断言「一帧内多次写入只通知一次」。
 */
function manualScheduler() {
  const queue = [];
  return {
    schedule: (notify) => {
      queue.push(notify);
    },
    flushFrame() {
      const pending = queue.splice(0, queue.length);
      for (const notify of pending) notify();
    },
    get pending() {
      return queue.length;
    },
  };
}

test("一帧内多次 push 只通知一次，快照取最后一帧", () => {
  const sched = manualScheduler();
  const stream = createLiveStream(sched.schedule);
  let notifications = 0;
  stream.subscribe(() => {
    notifications += 1;
  });

  stream.push({ kind: "thinking", text: "a" });
  stream.push({ kind: "thinking", text: "ab" });
  stream.push({ kind: "thinking", text: "abc" });
  assert.equal(notifications, 0, "到帧之前不应该通知");
  assert.equal(sched.pending, 1, "三次写入只能排一次 flush");

  sched.flushFrame();
  assert.equal(notifications, 1);
  assert.equal(stream.getSnapshot().text, "abc");
});

test("内容相同的帧不通知，快照引用保持不变", () => {
  const sched = manualScheduler();
  const stream = createLiveStream(sched.schedule);
  let notifications = 0;
  stream.subscribe(() => {
    notifications += 1;
  });

  const frame = { kind: "text", text: "同一段" };
  stream.push(frame);
  sched.flushFrame();
  const first = stream.getSnapshot();
  assert.equal(notifications, 1);

  stream.push({ kind: "text", text: "同一段" });
  assert.equal(sched.pending, 0, "内容没变不应该排帧");
  sched.flushFrame();
  assert.equal(notifications, 1);
  assert.equal(stream.getSnapshot(), first, "内容没变必须返回同一个引用");
});

test("clear 把快照置空，重复 clear 不再通知", () => {
  const sched = manualScheduler();
  const stream = createLiveStream(sched.schedule);
  let notifications = 0;
  stream.subscribe(() => {
    notifications += 1;
  });

  stream.push({ kind: "text", text: "x" });
  sched.flushFrame();
  stream.clear();
  sched.flushFrame();
  assert.equal(stream.getSnapshot(), null);
  assert.equal(notifications, 2);

  stream.clear();
  assert.equal(sched.pending, 0);
  sched.flushFrame();
  assert.equal(notifications, 2);
});

test("退订后不再收到通知", () => {
  const sched = manualScheduler();
  const stream = createLiveStream(sched.schedule);
  let notifications = 0;
  const unsubscribe = stream.subscribe(() => {
    notifications += 1;
  });

  unsubscribe();
  stream.push({ kind: "text", text: "y" });
  sched.flushFrame();
  assert.equal(notifications, 0);
});

test("liveSettledBy：正文/推理互相接管，统计行不接管", () => {
  // 推理落库 → 实时推理块撤掉；推理之后出现正文，实时块同样必须撤掉。
  assert.equal(liveSettledBy("thinking", "thinking"), true);
  assert.equal(liveSettledBy("thinking", "text"), true);
  assert.equal(liveSettledBy("thinking", "result"), true);
  assert.equal(liveSettledBy("thinking", "tool_use"), true);
  assert.equal(liveSettledBy("text", "text"), true);
  assert.equal(liveSettledBy("text", "tool_use"), true);
  // 统计与分隔行只是记账，不能把正在流的内容清掉。
  assert.equal(liveSettledBy("text", "usage"), false);
  assert.equal(liveSettledBy("thinking", "round"), false);
  // 工具帧只被工具活动接管。
  assert.equal(liveSettledBy("tool_use", "tool_use"), true);
  assert.equal(liveSettledBy("tool_start", "tool_result"), true);
  assert.equal(liveSettledBy("tool_input", "thinking"), false);
  // 空 kind（还没收到帧）不接管。
  assert.equal(liveSettledBy("", "text"), false);
});
