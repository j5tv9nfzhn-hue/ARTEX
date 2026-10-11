import { DEFAULT_LOCALE, isLocale, LOCALE_LABELS, LOCALES, normalizeLocale, resolveLocale } from "./config.ts";
import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

test("仅提供简体中文一种界面语言", () => {
  assert.equal(DEFAULT_LOCALE, "zh");
  assert.deepEqual(LOCALES, ["zh"]);
  assert.deepEqual(Object.keys(LOCALE_LABELS), ["zh"]);
  assert.equal(LOCALE_LABELS.zh, "简体中文");
  assert.equal(isLocale("zh"), true);
  for (const value of ["ko", "en", "es", "", "fr"]) assert.equal(isLocale(value), false);
});

test("旧语言存储统一归一为简体中文", () => {
  for (const value of ["zh", " ZH ", "ko", " KO ", "en", "es", "fr", "", " ", undefined, null, 1]) {
    assert.equal(normalizeLocale(value), "zh");
  }
});

test("预渲染固定为简体中文", (context) => {
  const previous = process.env.NEXT_PUBLIC_LOCALE;
  context.after(() => {
    if (previous === undefined) delete process.env.NEXT_PUBLIC_LOCALE;
    else process.env.NEXT_PUBLIC_LOCALE = previous;
  });
  for (const value of [undefined, "ko", "en", "es", "invalid", "zh"]) {
    if (value === undefined) delete process.env.NEXT_PUBLIC_LOCALE;
    else process.env.NEXT_PUBLIC_LOCALE = value;
    assert.equal(resolveLocale(), "zh");
  }
});

test("中文词库不含任何韩文，且不残留其他语言词库文件", () => {
  const messagesDir = new URL("../../messages/", import.meta.url);
  assert.deepEqual(fs.readdirSync(messagesDir), ["zh.json"]);
  const chinese = JSON.parse(fs.readFileSync(new URL("zh.json", messagesDir), "utf8"));
  assert.doesNotMatch(JSON.stringify(chinese), /[\u1100-\u11ff\u3130-\u318f\ua960-\ua97f\uac00-\ud7ff\uffa0-\uffdc]/u);
});
