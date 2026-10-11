import { mergeCatalogTree } from "./catalog-merge.ts";
import assert from "node:assert/strict";
import test from "node:test";

test("增量提取保留同一嵌套命名空间的人工键与 t(key) 已用键", () => {
  const existing = { nav: { home: "首页" }, app: { page: { manual: "人工文案", oldHash: "已使用文案" } } };
  const snapshot = structuredClone(existing);
  const additions = { app: { page: { newHash: "新文案" }, other: { title: "其他页面" } } };
  assert.deepEqual(mergeCatalogTree(existing, additions), {
    nav: { home: "首页" },
    app: { page: { manual: "人工文案", oldHash: "已使用文案", newHash: "新文案" }, other: { title: "其他页面" } },
  });
  assert.deepEqual(existing, snapshot);
});

test("已有外语译文保留，新键中文回退，中文源可显式刷新", () => {
  const additions = { app: { title: "中文标题", message: "剩余 {var0} 次" } };
  const existing = { app: { title: "English title" } };
  assert.equal(mergeCatalogTree(existing, additions).app.title, "English title");
  assert.equal(mergeCatalogTree(existing, additions).app.message, "剩余 {var0} 次");
  assert.equal(mergeCatalogTree(existing, additions, true).app.title, "中文标题");
});

test("字符串与命名空间结构冲突时拒绝覆盖，避免丢失现有消息", () => {
  assert.throws(() => mergeCatalogTree({ app: "原值" }, { app: { title: "新值" } }), /词库结构冲突/);
  assert.throws(() => mergeCatalogTree({ app: { title: "原值" } }, { app: "新值" }, true), /词库结构冲突/);
});
