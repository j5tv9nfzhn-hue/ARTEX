import assert from "node:assert/strict";
import test from "node:test";

import { HARNESS_COMMANDS, fuzzyScore, matchCommands, slashToken } from "./harness/commands.ts";
import { buildPlan } from "./harness/types.ts";

test("slashToken 只认词首的斜杠", () => {
  assert.deepEqual(slashToken("/new", 4), { start: 0, query: "new" });
  assert.deepEqual(slashToken("看 /pl", 5), { start: 2, query: "pl" });
  // 路径/URL 里的斜杠不是命令
  assert.equal(slashToken("a/b", 3), null);
  assert.equal(slashToken("/new 会话", 8), null);
  // 光标不在命令词里（后面还有内容）时不算
  assert.deepEqual(slashToken("/new", 2), { start: 0, query: "n" });
});

test("matchCommands 命中中文名与关键词", () => {
  assert.equal(matchCommands("模型")[0].id, "model");
  assert.equal(matchCommands("权限")[0].id, "perm");
  assert.equal(matchCommands("todo")[0].id, "plan");
  assert.equal(matchCommands("").length, HARNESS_COMMANDS.length);
  assert.deepEqual(matchCommands("zzz-no-such-command"), []);
});

test("fuzzyScore 前缀优于子序列，-1 表示不匹配", () => {
  assert.ok(fuzzyScore("model", "mod") > fuzzyScore("model", "mdl"));
  assert.equal(fuzzyScore("model", "xyz"), -1);
  assert.equal(fuzzyScore("model", ""), 1);
});

const w = (detail, tool = "TodoWrite") => ({ kind: "tool_use", tool, detail, summary: tool });

test("buildPlan 从 TodoWrite 的 detail 还原清单与状态", () => {
  const plan = buildPlan([
    w(
      JSON.stringify({
        todos: [
          { content: "读接口", status: "completed" },
          { content: "跑基线", status: "in_progress" },
          { content: "验证差价", status: "pending" },
        ],
      }),
    ),
  ]);
  assert.deepEqual(
    plan.map((p) => [p.title, p.status]),
    [
      ["读接口", "done"],
      ["跑基线", "active"],
      ["验证差价", "todo"],
    ],
  );
});

test("buildPlan 取最近一次完整 TodoWrite，截断的那条跳过", () => {
  // 截断的 JSON 是死数据：不能猜、也不能凭它编出步骤，只能回退到上一份完整快照。
  const plan = buildPlan([
    w(JSON.stringify({ todos: [{ content: "完整步骤", status: "in_progress" }] })),
    w('TodoWrite {"todos":[{"content":"被截断的步骤","status":"in_progress"}]'),
  ]);
  assert.deepEqual(
    plan.map((p) => [p.title, p.status]),
    [["完整步骤", "active"]],
  );
});

test("buildPlan 不臆造：解析不出就返回空", () => {
  assert.deepEqual(buildPlan([]), []);
  assert.deepEqual(buildPlan([w('TodoWrite {"todos":[{"con')]), []);
  assert.deepEqual(buildPlan([w("not json at all")]), []);
});

test("buildPlan 仍优先 intent（任务模式）", () => {
  const plan = buildPlan([
    { kind: "intent", intent_id: 1, summary: "第一步" },
    { kind: "intent", intent_id: 2, summary: "第二步" },
    w(JSON.stringify({ todos: [{ content: "不应出现", status: "pending" }] })),
  ]);
  assert.deepEqual(
    plan.map((p) => [p.title, p.status]),
    [
      ["第一步", "done"],
      ["第二步", "active"],
    ],
  );
});
