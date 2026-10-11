import { DEFAULT_LOCALE, isLocale, LOCALE_LABELS, LOCALES, normalizeLocale, resolveLocale } from "./config.ts";
import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

test("默认与可选界面语言为简体中文、英语、西班牙语", () => {
  assert.equal(DEFAULT_LOCALE, "zh");
  assert.deepEqual(LOCALES, ["zh", "en", "es"]);
  assert.equal(LOCALE_LABELS.zh, "简体中文");
  assert.equal(isLocale("ko"), false);
  assert.equal(isLocale("zh"), true);
});

test("旧 ko 存储兼容迁移，大小写及空白归一化", () => {
  for (const value of ["ko", " KO ", "zh", " ZH "]) assert.equal(normalizeLocale(value), "zh");
  assert.equal(normalizeLocale(" EN "), "en");
  assert.equal(normalizeLocale("es"), "es");
  for (const value of [undefined, null, "", "fr", 1]) assert.equal(normalizeLocale(value), undefined);
});

test("预渲染默认中文且兼容旧环境变量", (context) => {
  const previous = process.env.NEXT_PUBLIC_LOCALE;
  context.after(() => {
    if (previous === undefined) delete process.env.NEXT_PUBLIC_LOCALE;
    else process.env.NEXT_PUBLIC_LOCALE = previous;
  });
  for (const [value, expected] of [
    [undefined, "zh"],
    ["ko", "zh"],
    ["en", "en"],
    ["es", "es"],
    ["invalid", "zh"],
  ]) {
    if (value === undefined) delete process.env.NEXT_PUBLIC_LOCALE;
    else process.env.NEXT_PUBLIC_LOCALE = value;
    assert.equal(resolveLocale(), expected);
  }
});

test("词库键同步、中文兼容副本完整、项目词库没有韩文", () => {
  const read = (locale) =>
    JSON.parse(fs.readFileSync(new URL(`../../messages/${locale}.json`, import.meta.url), "utf8"));
  const keys = (value, prefix = "") =>
    Object.entries(value)
      .flatMap(([key, child]) => {
        const full = prefix ? `${prefix}.${key}` : key;
        return typeof child === "object" && child !== null ? keys(child, full) : [full];
      })
      .sort();
  const chinese = read("zh");
  assert.deepEqual(read("ko"), chinese);
  for (const locale of LOCALES) {
    const messages = read(locale);
    assert.deepEqual(keys(messages), keys(chinese));
    assert.doesNotMatch(
      JSON.stringify(messages),
      /[\u1100-\u11ff\u3130-\u318f\ua960-\ua97f\uac00-\ud7ff\uffa0-\uffdc]/u,
    );
  }
});
