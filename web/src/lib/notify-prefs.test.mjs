// 通知显示偏好的口径测试（纯函数，无需浏览器）。
//
// 这些断言守的是「设置面板说什么，卡片就怎么做」：夹取范围、坏数据容错、
// 触发判定、TTL 与声音分档。任何一条改了语义都会先在这里失败。

import {
  baseTtlMs,
  clampDelaySec,
  clampTtlSec,
  DEFAULT_NOTIFY_PREFS,
  DELAY_MAX_SEC,
  NOTIFY_TRIGGERS,
  parseNotifyPrefs,
  resolveTtlMs,
  serializeNotifyPrefs,
  shouldPopup,
  soundFor,
  TTL_MAX_SEC,
  TTL_MIN_SEC,
  triggerOf,
} from "./notify-prefs.ts";
import assert from "node:assert/strict";
import test from "node:test";

const finding = { kind: "finding", level: "warn", severity: "critical" };
const taskDone = { kind: "task_done", level: "success" };
const taskStart = { kind: "task_started", level: "info" };
const subagent = { kind: "subagent_done", level: "success" };
const chatFail = { kind: "chat_error", level: "error" };
const approval = { kind: "approval_pending", level: "warn" };
const timeout = { kind: "task_timeout", level: "warn" };

test("空值、坏 JSON、非对象一律回落到默认，且不共享 triggers 引用", () => {
  for (const raw of [null, undefined, "", "   ", "{", "[]", "null", "42", '"x"']) {
    const prefs = parseNotifyPrefs(raw);
    assert.deepEqual(prefs, DEFAULT_NOTIFY_PREFS);
    assert.notEqual(prefs.triggers, DEFAULT_NOTIFY_PREFS.triggers);
  }
  const a = parseNotifyPrefs(null);
  a.triggers.error = false;
  assert.equal(parseNotifyPrefs(null).triggers.error, true, "默认值被调用方改坏了");
});

test("单个坏字段只回落自己，好字段照常生效", () => {
  const prefs = parseNotifyPrefs(
    JSON.stringify({
      delaySec: 30,
      dock: "nope",
      sound: "loud",
      ttlMode: "fixed",
      ttlSec: 12,
      triggers: { error: false },
    }),
  );
  assert.equal(prefs.delaySec, 30);
  assert.equal(prefs.dock, DEFAULT_NOTIFY_PREFS.dock);
  assert.equal(prefs.sound, DEFAULT_NOTIFY_PREFS.sound);
  assert.equal(prefs.ttlMode, "fixed");
  assert.equal(prefs.ttlSec, 12);
  assert.equal(prefs.triggers.error, false);
  assert.equal(prefs.triggers.finding, true, "未提及的触发项必须保持默认开");
});

test("延迟夹到 0..上限，字符串数字也接受", () => {
  assert.equal(clampDelaySec(-5), 0);
  assert.equal(clampDelaySec(999), DELAY_MAX_SEC);
  assert.equal(clampDelaySec("15"), 15);
  assert.equal(clampDelaySec(3.9), 3, "小数截断成整数秒");
  assert.equal(clampDelaySec("abc"), DEFAULT_NOTIFY_PREFS.delaySec);
});

test("单条显示时长夹到 3..上限", () => {
  assert.equal(clampTtlSec(1), TTL_MIN_SEC);
  assert.equal(clampTtlSec(0), TTL_MIN_SEC);
  assert.equal(clampTtlSec(9999), TTL_MAX_SEC);
  assert.equal(clampTtlSec(""), DEFAULT_NOTIFY_PREFS.ttlSec);
});

test("触发口径：只有需要人处理的终态/阻塞点算触发，过程态静默", () => {
  assert.equal(triggerOf(finding), "finding");
  assert.equal(triggerOf(approval), "approval_pending");
  assert.equal(triggerOf(taskDone), "task_done");
  assert.equal(triggerOf(timeout), "task_timeout");
  assert.equal(triggerOf(chatFail), "error", "error 兜底，与 kind 无关");
  assert.equal(triggerOf(taskStart), null);
  assert.equal(triggerOf(subagent), null);
});

test("关掉某一类触发后不再弹卡片，但事件仍进收件箱", () => {
  const prefs = { ...DEFAULT_NOTIFY_PREFS, triggers: { ...DEFAULT_NOTIFY_PREFS.triggers, task_done: false } };
  assert.equal(shouldPopup(prefs, taskDone), false);
  assert.equal(shouldPopup(prefs, finding), true);
  // 过程态永远不弹，无论设置里怎么开。
  const allOn = { ...DEFAULT_NOTIFY_PREFS, triggers: Object.fromEntries(NOTIFY_TRIGGERS.map((k) => [k, true])) };
  assert.equal(shouldPopup(allOn, taskStart), false);
});

test("TTL：auto 按级别，fixed 统一", () => {
  assert.equal(baseTtlMs({ kind: "x", level: "info" }), 5000);
  assert.equal(baseTtlMs({ kind: "task_done", level: "success" }), 8000);
  assert.equal(baseTtlMs({ kind: "x", level: "warn" }), 8000);
  assert.equal(baseTtlMs({ kind: "x", level: "error" }), 10000);
  assert.equal(baseTtlMs(finding), 10000, "高危漏洞按 error 级停留");

  const fixed = { ...DEFAULT_NOTIFY_PREFS, ttlMode: "fixed", ttlSec: 20 };
  assert.equal(resolveTtlMs(fixed, { kind: "x", level: "info" }), 20000);
  assert.equal(resolveTtlMs(fixed, { kind: "x", level: "error" }), 20000);
  assert.equal(resolveTtlMs(DEFAULT_NOTIFY_PREFS, { kind: "x", level: "error" }), 10000);
});

test("声音：关闭档全静音，仅严重档只给 error/高危，完全档按事件分音型", () => {
  const off = { ...DEFAULT_NOTIFY_PREFS, sound: "off" };
  assert.equal(soundFor(off, finding), null);
  assert.equal(soundFor(off, taskDone), null);

  const urgent = { ...DEFAULT_NOTIFY_PREFS, sound: "urgent" };
  assert.equal(soundFor(urgent, finding), "error");
  assert.equal(soundFor(urgent, chatFail), "error");
  assert.equal(soundFor(urgent, taskDone), null, "普通完成不该在仅严重档响起");
  assert.equal(soundFor(urgent, timeout), null);

  const all = { ...DEFAULT_NOTIFY_PREFS, sound: "all" };
  assert.equal(soundFor(all, taskDone), "success");
  assert.equal(soundFor(all, timeout), "warn");
  assert.equal(soundFor(all, taskStart), null, "过程态即便全开也不响");
});

test("序列化—解析往返无损，且存进去的一定是合法形状", () => {
  const prefs = {
    delaySec: 7,
    dock: "top-center",
    sound: "urgent",
    ttlMode: "fixed",
    ttlSec: 15,
    triggers: Object.fromEntries(NOTIFY_TRIGGERS.map((k) => [k, k !== "finding"])),
  };
  const round = parseNotifyPrefs(serializeNotifyPrefs(prefs));
  assert.deepEqual(round, prefs);

  // 非法输入经 serialize 之后也必须是能读回来的合法值。
  const dirty = { ...prefs, delaySec: -3, ttlSec: 1e6, dock: "left-top", sound: "maybe" };
  const clean = parseNotifyPrefs(serializeNotifyPrefs(dirty));
  assert.equal(clean.delaySec, 0);
  assert.equal(clean.ttlSec, TTL_MAX_SEC);
  assert.equal(clean.dock, DEFAULT_NOTIFY_PREFS.dock);
  assert.equal(clean.sound, DEFAULT_NOTIFY_PREFS.sound);
});
