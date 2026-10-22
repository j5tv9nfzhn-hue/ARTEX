import { countLines, parsePartialJson, pickField, scanToolInput } from "./partial-json.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("正在流的值立即可见，并标记为 partial", () => {
  const fields = parsePartialJson('{"command": "git sta');
  assert.equal(fields.command, "git sta");
  const entries = scanToolInput('{"command": "git sta').entries;
  assert.equal(entries.length, 1);
  assert.equal(entries[0].key, "command");
  assert.equal(entries[0].partial, true);
});

test("多字段与转义", () => {
  const fields = parsePartialJson('{"file_path":"a/b.txt","content":"line1\\nline2');
  assert.equal(fields.file_path, "a/b.txt");
  assert.equal(fields.content, "line1\nline2");
});

test("数字与布尔也逐项出来", () => {
  const fields = parsePartialJson('{"timeout_ms":120000,"run_in_background":true}');
  assert.equal(fields.timeout_ms, "120000");
  assert.equal(fields.run_in_background, "true");
});

test("嵌套对象与数组带路径", () => {
  const entries = scanToolInput('{"identities":[{"name":"a"},{"name":"b"}]}').entries;
  assert.deepEqual(
    entries.map((entry) => entry.key),
    ["identities[0].name", "identities[1].name"],
  );
  assert.deepEqual(
    scanToolInput('{"tags":["a","b"]}').entries.map((entry) => entry.key),
    ["tags[0]", "tags[1]"],
  );
});

test("键到了值还没到时不产生条目", () => {
  const scan = scanToolInput('{"a":');
  assert.equal(scan.entries.length, 0);
  assert.equal(scan.done, false);
  assert.equal(scanToolInput('{"a":1,"b":').entries.length, 1);
});

test("被缓冲截断的数字算 partial", () => {
  const entry = scanToolInput('{"n":12').entries[0];
  assert.equal(entry.value, "12");
  assert.equal(entry.partial, true);
});

test("pickField 取叶子名，countLines 不计末尾换行", () => {
  const fields = parsePartialJson('{"opts":{"deep":{"x":"y"}},"content":"a\\nb\\nc"}');
  assert.equal(pickField(fields, ["x"]), "y");
  assert.equal(countLines("a\nb\nc"), 3);
  assert.equal(countLines("a\nb\n"), 2);
  assert.equal(countLines(""), 0);
});

test("大内容逐帧扫描始终能拿到已收到的部分", () => {
  const full = JSON.stringify({ file_path: "x.txt", content: "line\n".repeat(500) });
  let last = "";
  for (let cut = 1; cut <= full.length; cut += 7) {
    const value = parsePartialJson(full.slice(0, cut)).content;
    if (typeof value === "string") last = value;
  }
  assert.ok(last.length > 0);
  assert.ok(last.length < 3000);
});
